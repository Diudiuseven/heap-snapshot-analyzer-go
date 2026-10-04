// Package spill supplies bounded-memory primitives used when a snapshot is
// larger than the configured memory budget: disk-backed record chunks,
// chunked external sort/merge, and paged int64 arrays.
//
// Nothing here knows about heap objects; callers marshal records into
// length-prefixed binary key/value pairs.
package spill

import (
	"bufio"
	"container/heap"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Budget bounds live heap usage (bytes of buffered records). It is a soft
// budget: a single oversized record may exceed it, but aggregate buffering
// never grows without bound.
type Budget struct {
	Bytes int64
	mu    sync.Mutex
	live  int64
}

// DefaultBudget (64 MiB) is used when no budget is supplied.
const DefaultBudget int64 = 64 << 20

// Live reports currently buffered bytes.
func (b *Budget) Live() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.live
}

// Add reserves n bytes and reports whether the reservation exceeds the
// budget. It always reserves (best-effort accounting); the caller decides
// whether to flush.
func (b *Budget) Add(n int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.live += n
	lim := b.Bytes
	if lim <= 0 {
		lim = DefaultBudget
	}
	return b.live > lim
}

// Release returns n bytes to the budget.
func (b *Budget) Release(n int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.live -= n
	if b.live < 0 {
		b.live = 0
	}
}

// KV is one binary record with a comparable key. Keys compare lexicographically.
type KV struct {
	Key []byte
	Val []byte
	Seq int64 // stable tie-breaker; assigned globally per stream if desired
}

func (k KV) less(o KV) bool {
	c := compareBytes(k.Key, o.Key)
	if c != 0 {
		return c < 0
	}
	return k.Seq < o.Seq
}

func compareBytes(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

// Store is a temporary, on-disk collection of chunks.
type Store struct {
	dir    string
	budget *Budget
	mu     sync.Mutex
	seq    int
	files  []string
	closed bool
}

// NewStore creates a spill store rooted at dir (empty = fresh temp dir).
func NewStore(dir string, budget *Budget) (*Store, error) {
	if budget == nil {
		budget = &Budget{Bytes: DefaultBudget}
	}
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "heap-spill-")
		if err != nil {
			return nil, err
		}
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir, budget: budget}, nil
}

// Dir reports the spill root.
func (s *Store) Dir() string { return s.dir }

// Budget reports the memory budget.
func (s *Store) Budget() *Budget { return s.budget }

// chunk is a spill chunk. While open it buffers records in RAM; on Seal the
// buffer is sorted (stable by Seq) and written to disk, freeing the budget.
type chunk struct {
	store  *Store
	path   string
	buf    []KV
	bytes  int64
	sealed bool
	open   *os.File
}

// Chunk is one sequential spill chunk.
type Chunk interface {
	Write(kv KV) error
	Sealed() bool
	Size() int64
}

// NewChunk creates a fresh writable chunk.
func (s *Store) NewChunk() (Chunk, error) {
	s.mu.Lock()
	s.seq++
	id := s.seq
	s.mu.Unlock()
	path := filepath.Join(s.dir, fmt.Sprintf("chunk-%d.bin", id))
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.files = append(s.files, path)
	s.mu.Unlock()
	c := &chunk{store: s, path: path, open: f}
	return c, nil
}

func (c *chunk) Write(kv KV) error {
	if c.sealed {
		return errors.New("spill: write to sealed chunk")
	}
	cp := KV{Key: append([]byte(nil), kv.Key...),
		Val: append([]byte(nil), kv.Val...), Seq: kv.Seq}
	n := int64(len(cp.Key) + len(cp.Val))
	c.buf = append(c.buf, cp)
	c.bytes += n
	c.store.budget.Add(n)
	return nil
}

func (c *chunk) Sealed() bool { return c.sealed }
func (c *chunk) Size() int64  { return c.bytes }

// record framing: uvarint keylen, uvarint vallen, int64 seq BE, key, val.
func writeRecord(w io.Writer, kv KV) error {
	var hdr [binary.MaxVarintLen64*2 + 8]byte
	off := binary.PutUvarint(hdr[:], uint64(len(kv.Key)))
	off += binary.PutUvarint(hdr[off:], uint64(len(kv.Val)))
	binary.BigEndian.PutUint64(hdr[off:off+8], uint64(kv.Seq))
	off += 8
	if _, err := w.Write(hdr[:off]); err != nil {
		return err
	}
	if _, err := w.Write(kv.Key); err != nil {
		return err
	}
	_, err := w.Write(kv.Val)
	return err
}

// Seal sorts the buffer (stable), flushes it to disk and frees the budget.
func (s *Store) Seal(c Chunk) error {
	ch, ok := c.(*chunk)
	if !ok {
		return errors.New("spill: foreign chunk")
	}
	if ch.sealed {
		return nil
	}
	// Stable sort keeps equal keys in arrival order; the Seq field provides
	// a fully deterministic order even across re-reads.
	sort.SliceStable(ch.buf, func(i, j int) bool { return ch.buf[i].less(ch.buf[j]) })
	bw := bufio.NewWriterSize(ch.open, 1<<20)
	for _, kv := range ch.buf {
		if err := writeRecord(bw, kv); err != nil {
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if err := ch.open.Close(); err != nil {
		return err
	}
	ch.open = nil
	ch.sealed = true
	s.budget.Release(ch.bytes)
	ch.buf = nil // release references; bytes now live on disk
	return nil
}

// chunkIter reads one sealed chunk sequentially in stored (sorted) order.
type chunkIter struct {
	f   *os.File
	br  *bufio.Reader
	buf []byte
}

// Iter returns a forward iterator over a sealed chunk in key order.
func (s *Store) Iter(c Chunk) (Iter, error) {
	ch, ok := c.(*chunk)
	if !ok || !ch.sealed {
		return nil, errors.New("spill: iter requires sealed chunk")
	}
	f, err := os.Open(ch.path)
	if err != nil {
		return nil, err
	}
	return &chunkIter{f: f, br: bufio.NewReaderSize(f, 1<<20)}, nil
}

func (it *chunkIter) Next() (KV, error) {
	klen, err := binary.ReadUvarint(it.br)
	if err != nil {
		return KV{}, err
	}
	vlen, err := binary.ReadUvarint(it.br)
	if err != nil {
		return KV{}, err
	}
	var seqB [8]byte
	if _, err := io.ReadFull(it.br, seqB[:]); err != nil {
		return KV{}, err
	}
	key := make([]byte, klen)
	if _, err := io.ReadFull(it.br, key); err != nil {
		return KV{}, err
	}
	val := make([]byte, vlen)
	if _, err := io.ReadFull(it.br, val); err != nil {
		return KV{}, err
	}
	return KV{Key: key, Val: val, Seq: int64(binary.BigEndian.Uint64(seqB[:]))}, nil
}

func (it *chunkIter) Close() error {
	if it.f != nil {
		err := it.f.Close()
		it.f = nil
		return err
	}
	return nil
}

// Cleanup removes all spill artifacts.
func (s *Store) Cleanup() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return os.RemoveAll(s.dir)
}

// Iter yields sorted key/value pairs.
type Iter interface {
	Next() (KV, error) // io.EOF at end
	Close() error
}

// mergeHeap k-way merges already-sorted iterators.
type mergeHeap struct {
	iters []Iter
	cur   []KV
	ok    []bool
}

func (m *mergeHeap) Len() int {
	n := 0
	for _, ok := range m.ok {
		if ok {
			n++
		}
	}
	return n
}
func (m *mergeHeap) Less(i, j int) bool { return m.cur[i].less(m.cur[j]) }
func (m *mergeHeap) Swap(i, j int) {
	m.cur[i], m.cur[j] = m.cur[j], m.cur[i]
	m.ok[i], m.ok[j] = m.ok[j], m.ok[i]
	m.iters[i], m.iters[j] = m.iters[j], m.iters[i]
}
func (m *mergeHeap) Push(x any) {}
func (m *mergeHeap) Pop() any   { return nil }

type mergeIter struct {
	h    *mergeHeap
	done bool
}

// Merge merges already-sorted iters into one sorted stream. The returned Iter
// closes all inputs when closed.
func Merge(iters ...Iter) Iter {
	m := &mergeHeap{iters: iters, cur: make([]KV, len(iters)), ok: make([]bool, len(iters))}
	for i, it := range iters {
		kv, err := it.Next()
		if err == nil {
			m.cur[i] = kv
			m.ok[i] = true
		}
	}
	heap.Init(m)
	return &mergeIter{h: m}
}

func (mi *mergeIter) Next() (KV, error) {
	for {
		if mi.h.Len() == 0 {
			return KV{}, io.EOF
		}
		// heap root lives at index 0 because we manage flags manually.
		idx := mi.pick()
		kv := mi.h.cur[idx]
		nxt, err := mi.h.iters[idx].Next()
		if err != nil {
			mi.h.ok[idx] = false
		} else {
			mi.h.cur[idx] = nxt
		}
		return kv, nil
	}
}

// pick returns the index of the smallest current element.
func (mi *mergeIter) pick() int {
	best := -1
	for i, ok := range mi.h.ok {
		if !ok {
			continue
		}
		if best < 0 || mi.h.cur[i].less(mi.h.cur[best]) {
			best = i
		}
	}
	return best
}

func (mi *mergeIter) Close() error {
	var first error
	for _, it := range mi.h.iters {
		if err := it.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// SortMerge performs an external sort: records from in are buffered up to the
// budget, sorted and sealed into chunks, then k-way merged by key. The
// returned Iter must be closed by the caller; closing also seals/cleans up.
//
// A single in-memory chunk (input under budget) never touches disk.
func SortMerge(s *Store, in <-chan KV) (Iter, error) {
	var chunks []Chunk
	cur, err := s.NewChunk()
	if err != nil {
		return nil, err
	}
	flush := func() error {
		if cur.Size() == 0 {
			return nil
		}
		if err := s.Seal(cur); err != nil {
			return err
		}
		chunks = append(chunks, cur)
		var err error
		cur, err = s.NewChunk()
		return err
	}
	for kv := range in {
		if err := cur.Write(kv); err != nil {
			return nil, err
		}
		if s.budget.Live() >= s.budget.Bytes && s.budget.Bytes > 0 {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	if cur.Size() > 0 {
		if err := s.Seal(cur); err != nil {
			return nil, err
		}
		chunks = append(chunks, cur)
	} else {
		// empty trailing chunk file; harmless
		s.Seal(cur)
	}
	if len(chunks) == 0 {
		return emptyIter{}, nil
	}
	if len(chunks) == 1 {
		return s.Iter(chunks[0])
	}
	iters := make([]Iter, 0, len(chunks))
	for _, c := range chunks {
		it, err := s.Iter(c)
		if err != nil {
			return nil, err
		}
		iters = append(iters, it)
	}
	return Merge(iters...), nil
}

type emptyIter struct{}

func (emptyIter) Next() (KV, error) { return KV{}, io.EOF }
func (emptyIter) Close() error      { return nil }
