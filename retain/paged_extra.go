package retain

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
	"sort"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/spill"
)

// pagedBool is a dense bool array backed by a spill file with one cached
// page. Accesses in the dominator passes are node-sequential-ish; the small
// single-page cache keeps live memory bounded while supporting random access.
type pagedBool struct {
	f       *os.File
	page    int
	cacheID int64
	cache   []byte
	dirty   bool
	path    string
	n       int64
}

func newPagedBool(n int64, st *spill.Store) (*pagedBool, error) {
	f, err := os.CreateTemp(st.Dir(), "bool-*.bin")
	if err != nil {
		return nil, err
	}
	p := &pagedBool{f: f, page: 4096, cacheID: -1,
		cache: make([]byte, 4096), n: n, path: f.Name()}
	return p, nil
}

func (p *pagedBool) flush() error {
	if !p.dirty || p.cacheID < 0 {
		return nil
	}
	_, err := p.f.WriteAt(p.cache, p.cacheID*int64(p.page))
	p.dirty = false
	return err
}

func (p *pagedBool) load(page int64) error {
	if err := p.flush(); err != nil {
		return err
	}
	for i := range p.cache {
		p.cache[i] = 0
	}
	_, err := p.f.ReadAt(p.cache, page*int64(p.page))
	if err == io.EOF {
		err = nil
	}
	p.cacheID = page
	p.dirty = false
	return err
}

func (p *pagedBool) get(i int64) (bool, error) {
	page := i / int64(p.page)
	off := i % int64(p.page)
	if page != p.cacheID {
		if err := p.load(page); err != nil {
			return false, err
		}
	}
	return p.cache[off] == 1, nil
}

func (p *pagedBool) set(i int64, v bool) error {
	page := i / int64(p.page)
	off := i % int64(p.page)
	if page != p.cacheID {
		if err := p.load(page); err != nil {
			return err
		}
	}
	b := byte(0)
	if v {
		b = 1
	}
	p.cache[off] = b
	p.dirty = true
	return nil
}

func (p *pagedBool) close() error {
	err1 := p.flush()
	err2 := p.f.Close()
	err3 := os.Remove(p.path)
	if err1 != nil {
		return err1
	}
	if err2 != nil {
		return err2
	}
	return err3
}

// stringPaged stores one string per node in a spill file, block-indexed. The
// number of distinct sites is tiny; we keep a string id table in RAM and the
// per-node ids paged as int64 via spill.
type stringPaged struct {
	ids   *spill.PagedInt64 // node -> interned id (-1 empty)
	table []string
}

func newStringPaged(n int64, st *spill.Store) (*stringPaged, error) {
	ids, err := spill.NewPagedInt64(n, -1, st, nil)
	if err != nil {
		return nil, err
	}
	return &stringPaged{ids: ids}, nil
}

func (p *stringPaged) set(i int64, s string) error {
	if s == "" {
		return p.ids.Set(i, -1)
	}
	for k, t := range p.table {
		if t == s {
			return p.ids.Set(i, int64(k))
		}
	}
	id := int64(len(p.table))
	p.table = append(p.table, s)
	return p.ids.Set(i, id)
}

func (p *stringPaged) get(i int64) (string, error) {
	id, err := p.ids.Get(i)
	if err != nil {
		return "", err
	}
	if id < 0 || int(id) >= len(p.table) {
		return "", nil
	}
	return p.table[id], nil
}

func (p *stringPaged) close() error { return p.ids.Close() }

// edgeArray is an append-sorted, file-backed int64 adjacency array. During
// construction edges live in spill-sorted runs; after buildCSR they occupy a
// binary file that supports random get().
type edgeArray struct {
	f    *os.File
	path string
	len  int64
}

func (a *edgeArray) get(i int64) (int64, error) {
	var b [8]byte
	_, err := a.f.ReadAt(b[:], i*8)
	if err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint64(b[:])), nil
}

func (a *edgeArray) close() error {
	if a.f != nil {
		a.f.Close()
	}
	if a.path != "" {
		return os.Remove(a.path)
	}
	return nil
}

// buildCSR reads the raw edge file (16-byte big-endian from,to pairs) and
// builds an offset array (paged) plus a fully sorted adjacency file.
//
// forward=true sorts by (from,to) producing successors; forward=false sorts
// by (to,from) producing predecessors (the stored adjacency value is `from`).
//
// Edges are streamed into spill.SortMerge so peak memory stays bounded.
func buildCSR(n int64, edgeFile string, forward bool, st *spill.Store, budget *spill.Budget) (*spill.PagedInt64, *edgeArray, error) {
	in, err := os.Open(edgeFile)
	if err != nil {
		return nil, nil, err
	}
	defer in.Close()

	kvc := make(chan spill.KV)
	done := make(chan sortRes, 1)
	go func() {
		it, err := spill.SortMerge(st, kvc)
		done <- sortRes{it: it, err: err}
	}()

	go func() {
		defer close(kvc)
		br := bufio.NewReaderSize(in, 1<<16)
		var b [16]byte
		var seq int64
		for {
			_, rerr := io.ReadFull(br, b[:])
			if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
				return
			}
			if rerr != nil {
				return
			}
			from := int64(binary.BigEndian.Uint64(b[:8]))
			to := int64(binary.BigEndian.Uint64(b[8:]))
			seq++
			if forward {
				key := make([]byte, 16)
				binary.BigEndian.PutUint64(key[:8], uint64(from))
				binary.BigEndian.PutUint64(key[8:], uint64(to))
				var val [8]byte
				binary.BigEndian.PutUint64(val[:], uint64(to))
				kvc <- spill.KV{Key: key, Val: val[:], Seq: seq}
			} else {
				key := make([]byte, 16)
				binary.BigEndian.PutUint64(key[:8], uint64(to))
				binary.BigEndian.PutUint64(key[8:], uint64(from))
				var val [8]byte
				binary.BigEndian.PutUint64(val[:], uint64(from))
				kvc <- spill.KV{Key: key, Val: val[:], Seq: seq}
			}
		}
	}()

	res := <-done
	if res.err != nil {
		return nil, nil, res.err
	}
	defer res.it.Close()

	off, err := spill.NewPagedInt64(n+1, 0, st, budget)
	if err != nil {
		return nil, nil, err
	}
	af, err := os.CreateTemp(st.Dir(), "adj-*.bin")
	if err != nil {
		return nil, nil, err
	}
	adj := &edgeArray{f: af, path: af.Name()}
	bw := bufio.NewWriterSize(af, 1<<16)

	// Fill offsets and adjacency in node order; input iter is key-sorted.
	var pos int64
	curNode := int64(0)
	// nodes before first edge start at 0
	prevNode := int64(-1)
	var counts []int64 // per-node degree accumulated in bounded chunks is avoided:
	// instead set offsets on the fly using a two-region approach: we need
	// off[node]=start. Because iter is sorted, set off when node changes.
	_ = counts
	setOff := func(node int64, value int64) error { return off.Set(node, value) }

	// Initialize off[0]=0 and walk.
	if err := setOff(0, 0); err != nil {
		return nil, nil, err
	}
	for {
		kv, err := res.it.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		node := int64(binary.BigEndian.Uint64(kv.Key[:8]))
		val := kv.Val[:8]
		if node != prevNode {
			// close gaps for nodes with zero edges
			for gap := prevNode + 1; gap <= node; gap++ {
				if err := setOff(gap, pos); err != nil {
					return nil, nil, err
				}
			}
			prevNode = node
			curNode = node
		}
		if _, err := bw.Write(val); err != nil {
			return nil, nil, err
		}
		pos++
	}
	// close trailing offsets
	for gap := prevNode + 1; gap <= n; gap++ {
		if err := setOff(gap, pos); err != nil {
			return nil, nil, err
		}
	}
	if err := bw.Flush(); err != nil {
		return nil, nil, err
	}
	adj.len = pos
	_ = curNode
	return off, adj, nil
}

type sortRes struct {
	it  spill.Iter
	err error
}

var _ = sort.Slice
