package spill

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"sort"
	"sync"
)

// pager is a fixed-page-size backing file with an LRU cache of pinned pages.
// It is shared by all paged arrays of one Store so the budget is global.
type pager struct {
	mu      sync.Mutex
	f       *os.File
	pageSz  int // entries per page
	maxPage int // maximum number of RAM-resident pages
	cache   map[int64][]byte
	lru     []int64 // page ids, most recently used last
	dirty   map[int64]bool
	nextID  int
}

func newPager(dir string, width int, pageBytes int64, maxPages int) (*pager, error) {
	f, err := os.CreateTemp(dir, "pages-*.bin")
	if err != nil {
		return nil, err
	}
	entries := int(pageBytes) / width
	if entries < 16 {
		entries = 16
	}
	if maxPages < 2 {
		maxPages = 4
	}
	return &pager{f: f, pageSz: entries, maxPage: maxPages,
		cache: map[int64][]byte{}, dirty: map[int64]bool{}}, nil
}

func (p *pager) touch(id int64) {
	for i, x := range p.lru {
		if x == id {
			p.lru = append(p.lru[:i], p.lru[i+1:]...)
			break
		}
	}
	p.lru = append(p.lru, id)
}

func (p *pager) evictLocked() error {
	for len(p.cache) >= p.maxPage {
		if len(p.lru) == 0 {
			return errors.New("pager: no page to evict")
		}
		victim := p.lru[0]
		p.lru = p.lru[1:]
		if err := p.flushLocked(victim); err != nil {
			return err
		}
		delete(p.cache, victim)
		delete(p.dirty, victim)
	}
	return nil
}

func (p *pager) flushLocked(id int64) error {
	if !p.dirty[id] {
		return nil
	}
	off := id * int64(len(p.cache[id]))
	_, err := p.f.WriteAt(p.cache[id], off)
	return err
}

// getPage returns a cached page, loading it from disk if needed.
func (p *pager) getPage(id int64, fill []byte) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if b, ok := p.cache[id]; ok {
		p.touch(id)
		return b, nil
	}
	if err := p.evictLocked(); err != nil {
		return nil, err
	}
	b := make([]byte, p.pageSz*len(fill))
	off := id * int64(len(b))
	n, err := p.f.ReadAt(b, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if n < len(b) {
		// untouched region: initialize with fill value
		for i := 0; i < p.pageSz; i++ {
			copy(b[i*len(fill):(i+1)*len(fill)], fill)
		}
	}
	p.cache[id] = b
	p.touch(id)
	return b, nil
}

// PagedInt64 is a dense, optionally disk-backed int64 array. Below the memory
// budget it lives in RAM; large arrays page fixed-size blocks to the spill
// store with an LRU. Not concurrency-safe; the retain pipeline serializes.
type PagedInt64 struct {
	pager *pager
	n     int64
	width int
	fillB []byte
}

// NewPagedInt64 returns an array of length n initialized to fill.
func NewPagedInt64(n int64, fill int64, s *Store, budget *Budget) (*PagedInt64, error) {
	if s == nil {
		var err error
		s, err = NewStore("", budget)
		if err != nil {
			return nil, err
		}
	}
	b := int64(1 << 16)
	if budget != nil && budget.Bytes > 0 {
		b = budget.Bytes / 4
		if b < 1<<16 {
			b = 1 << 16
		}
	}
	pg, err := newPager(s.dir, 8, b, 8)
	if err != nil {
		return nil, err
	}
	var fb [8]byte
	binary.LittleEndian.PutUint64(fb[:], uint64(fill))
	return &PagedInt64{pager: pg, n: n, width: 8, fillB: fb[:]}, nil
}

func (p *PagedInt64) bounds(i int64) error {
	if i < 0 || i >= p.n {
		return errors.New("paged: index out of range")
	}
	return nil
}

// Get returns element i.
func (p *PagedInt64) Get(i int64) (int64, error) {
	if err := p.bounds(i); err != nil {
		return 0, err
	}
	id := i / int64(p.pager.pageSz)
	page, err := p.pager.getPage(id, p.fillB)
	if err != nil {
		return 0, err
	}
	off := (i % int64(p.pager.pageSz)) * 8
	return int64(binary.LittleEndian.Uint64(page[off : off+8])), nil
}

// Set assigns element i.
func (p *PagedInt64) Set(i, v int64) error {
	if err := p.bounds(i); err != nil {
		return err
	}
	id := i / int64(p.pager.pageSz)
	page, err := p.pager.getPage(id, p.fillB)
	if err != nil {
		return err
	}
	off := (i % int64(p.pager.pageSz)) * 8
	binary.LittleEndian.PutUint64(page[off:off+8], uint64(v))
	p.pager.mu.Lock()
	p.pager.dirty[id] = true
	p.pager.mu.Unlock()
	return nil
}

// GetBits returns the raw 64 bits of element i.
func (p *PagedInt64) GetBits(i int64) (uint64, error) {
	v, err := p.Get(i)
	return uint64(v), err
}

// SetBits writes raw 64 bits to element i.
func (p *PagedInt64) SetBits(i int64, v uint64) error { return p.Set(i, int64(v)) }

// Len returns the array length.
func (p *PagedInt64) Len() int64 { return p.n }

// Close flushes dirty pages and removes the backing file.
func (p *PagedInt64) Close() error {
	p.pager.mu.Lock()
	ids := make([]int64, 0, len(p.pager.dirty))
	for id := range p.pager.dirty {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		if err := p.pager.flushLocked(id); err != nil {
			p.pager.mu.Unlock()
			return err
		}
	}
	name := p.pager.f.Name()
	p.pager.mu.Unlock()
	p.pager.f.Close()
	return os.Remove(name)
}

// PagedFloat64 mirrors PagedInt64 for float64.
type PagedFloat64 struct{ p *PagedInt64 }

// NewPagedFloat64 returns a paged float64 array.
func NewPagedFloat64(n int64, fill float64, s *Store, budget *Budget) (*PagedFloat64, error) {
	pi, err := NewPagedInt64(n, 0, s, budget)
	if err != nil {
		return nil, err
	}
	pf := &PagedFloat64{pi}
	if fill != 0 {
		for i := int64(0); i < n; i++ {
			if err := pf.Set(i, fill); err != nil {
				return nil, err
			}
		}
	}
	return pf, nil
}

// Get returns element i.
func (p *PagedFloat64) Get(i int64) (float64, error) {
	bits, err := p.p.GetBits(i)
	return math.Float64frombits(bits), err
}

// Set assigns element i.
func (p *PagedFloat64) Set(i int64, v float64) error {
	return p.p.SetBits(i, math.Float64bits(v))
}

// Len returns the array length.
func (p *PagedFloat64) Len() int64 { return p.p.Len() }

// Close releases resources.
func (p *PagedFloat64) Close() error { return p.p.Close() }
