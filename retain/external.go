package retain

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/spill"
)

// ExternalGraph is a Graph backed by disk-spilled CSR plus paged node arrays,
// used when a snapshot is larger than the memory budget.
//
// Build is two-pass over the spilled edge file:
//
//  1. first pass: map producer target IDs to dense ids (objects are dense in
//     arrival order; unknown targets become dangling edges and are reported,
//     never crashing the analysis);
//  2. degree counts then adjacency fill via the spill store, producing
//     forward and reverse adjacency in bounded memory.
type ExternalGraph struct {
	store     *spill.Store
	n         int64
	offOut    *spill.PagedInt64 // length n+1
	offIn     *spill.PagedInt64 // length n+1
	adjOut    *edgeArray        // CSR forward edges
	adjIn     *edgeArray        // CSR reverse edges
	sizes     *spill.PagedInt64
	roots     *pagedBool
	sites     *stringPaged
	dangle    []DanglingEdge
	edgeCount int64
}

// ExternalNode feeds one object to the external builder.
type ExternalNode struct {
	Dense   int64
	Size    int64
	Site    string
	Root    bool
	Targets []int64 // dense target ids (caller resolves producer IDs)
}

// ExternalFeed streams nodes and edges. Targets use dense ids; the caller
// (index integration) performs the producer->dense translation.
type ExternalFeed func(yield func(ExternalNode) bool)

// BuildExternal constructs an ExternalGraph. nodes must be dense 0..n-1.
func BuildExternal(n int64, feed ExternalFeed, opts Options) (*ExternalGraph, error) {
	budget := &spill.Budget{Bytes: opts.MemoryBudgetBytes}
	st, err := spill.NewStore(opts.SpillDir, budget)
	if err != nil {
		return nil, err
	}
	g := &ExternalGraph{store: st, n: n}

	sizes, err := spill.NewPagedInt64(n+1, 0, st, budget)
	if err != nil {
		return nil, err
	}
	roots, err := newPagedBool(n+1, st)
	if err != nil {
		return nil, err
	}
	sites, err := newStringPaged(n+1, st)
	if err != nil {
		return nil, err
	}
	g.sizes, g.roots, g.sites = sizes, roots, sites

	// Spill edges as raw (from,to) dense pairs while filling node metadata.
	ef, err := os.Create(filepath.Join(st.Dir(), "ext-edges.bin"))
	if err != nil {
		return nil, err
	}
	var edgeCount int64
	feed(func(nd ExternalNode) bool {
		if nd.Dense < 0 || nd.Dense >= n {
			err = errors.New("retain: external node id out of range")
			return false
		}
		if e := g.sizes.Set(nd.Dense, nd.Size); e != nil {
			err = e
			return false
		}
		if e := g.roots.set(nd.Dense, nd.Root); e != nil {
			err = e
			return false
		}
		if e := g.sites.set(nd.Dense, nd.Site); e != nil {
			err = e
			return false
		}
		for _, to := range nd.Targets {
			if to < 0 || to >= n {
				g.dangle = append(g.dangle, DanglingEdge{From: nd.Dense, RawDense: to})
				continue
			}
			var b [16]byte
			binary.BigEndian.PutUint64(b[:8], uint64(nd.Dense))
			binary.BigEndian.PutUint64(b[8:], uint64(to))
			if _, e := ef.Write(b[:]); e != nil {
				err = e
				return false
			}
			edgeCount++
		}
		return true
	})
	if cerr := ef.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}

	// Build CSR using an external sort of edges by (to,from) for reverse and
	// (from,to) for forward. Both are derived from the same edge file.
	outOff, outAdj, err := buildCSR(n, filepath.Join(st.Dir(), "ext-edges.bin"), true, st, budget)
	if err != nil {
		return nil, err
	}
	inOff, inAdj, err := buildCSR(n, filepath.Join(st.Dir(), "ext-edges.bin"), false, st, budget)
	if err != nil {
		return nil, err
	}
	g.offOut, g.adjOut = outOff, outAdj
	g.offIn, g.adjIn = inOff, inAdj
	g.edgeCount = edgeCount
	return g, nil
}

// DanglingEdge is an edge whose target could not be resolved.
type DanglingEdge struct {
	From     int64
	RawTok   string
	RawDense int64
}

// Dangling reports unresolved target edges from the build.
func (g *ExternalGraph) Dangling() []DanglingEdge {
	out := make([]DanglingEdge, len(g.dangle))
	copy(out, g.dangle)
	return out
}

func (g *ExternalGraph) N() int64 { return g.n }
func (g *ExternalGraph) IsRoot(v int64) bool {
	ok, _ := g.roots.get(v)
	return ok
}
func (g *ExternalGraph) NodeSize(v int64) int64 {
	x, _ := g.sizes.Get(v)
	return x
}
func (g *ExternalGraph) NodeSite(v int64) string {
	s, _ := g.sites.get(v)
	return s
}
func (g *ExternalGraph) Successors(v int64, fn func(int64) bool) error {
	return g.iterCSR(g.offOut, g.adjOut, v, fn)
}
func (g *ExternalGraph) Predecessors(v int64, fn func(int64) bool) error {
	return g.iterCSR(g.offIn, g.adjIn, v, fn)
}

func (g *ExternalGraph) iterCSR(off *spill.PagedInt64, adj *edgeArray, v int64, fn func(int64) bool) error {
	lo, err := off.Get(v)
	if err != nil {
		return err
	}
	hi, err := off.Get(v + 1)
	if err != nil {
		return err
	}
	for e := lo; e < hi; e++ {
		to, err := adj.get(e)
		if err != nil {
			return err
		}
		if !fn(to) {
			return nil
		}
	}
	return nil
}

// Close releases paged arrays and spill files.
func (g *ExternalGraph) Close() error {
	for _, c := range []func() error{
		func() error { return g.offOut.Close() },
		func() error { return g.offIn.Close() },
		func() error { return g.sizes.Close() },
		func() error { return g.roots.close() },
		func() error { return g.sites.close() },
		func() error { return g.adjOut.close() },
		func() error { return g.adjIn.close() },
	} {
		_ = c()
	}
	return g.store.Cleanup()
}

var _ io.Reader
var _ = sort.Slice

// newPaged supplies paged LT working arrays so the dominator pass keeps peak
// memory bounded for snapshots larger than RAM.
func (g *ExternalGraph) newPaged(n, fill int64) (iarray, error) {
	p, err := spill.NewPagedInt64(n, fill, g.store, g.store.Budget())
	if err != nil {
		return nil, err
	}
	return pagedArray{p: p}, nil
}

// NodeMeta is one dense node's attributes.
type NodeMeta struct {
	Dense int64
	Size  int64
	Site  string
	Root  bool
}

// Edge is one already-resolved strong edge in dense coordinates.
type Edge struct{ From, To int64 }

// BuildStreaming constructs a paged ExternalGraph from streaming node and
// edge channels. Nodes must arrive dense 0..n-1 (any order is accepted as
// long as Dense is unique in range); edges may arrive at any time and are
// spilled to disk for bounded, externally sorted CSR construction.
func BuildStreaming(n int64, opts Options, nodes <-chan NodeMeta, edges <-chan Edge) (*ExternalGraph, error) {
	budget := &spill.Budget{Bytes: opts.MemoryBudgetBytes}
	st, err := spill.NewStore(opts.SpillDir, budget)
	if err != nil {
		return nil, err
	}
	g := &ExternalGraph{store: st, n: n}

	sizes, err := spill.NewPagedInt64(n+1, 0, st, budget)
	if err != nil {
		return nil, err
	}
	roots, err := newPagedBool(n+1, st)
	if err != nil {
		return nil, err
	}
	sites, err := newStringPaged(n+1, st)
	if err != nil {
		return nil, err
	}
	g.sizes, g.roots, g.sites = sizes, roots, sites

	ef, err := os.Create(filepath.Join(st.Dir(), "ext-edges.bin"))
	if err != nil {
		return nil, err
	}
	var edgeCount int64
	writeEdge := func(from, to int64) error {
		var b [16]byte
		binary.BigEndian.PutUint64(b[:8], uint64(from))
		binary.BigEndian.PutUint64(b[8:], uint64(to))
		_, e := ef.Write(b[:])
		return e
	}
	doneNodes := make(chan error, 1)
	go func() {
		var ferr error
		for nd := range nodes {
			if nd.Dense < 0 || nd.Dense >= n {
				ferr = errors.New("retain: node id out of range")
				break
			}
			if e := g.sizes.Set(nd.Dense, nd.Size); e != nil {
				ferr = e
				break
			}
			if e := g.roots.set(nd.Dense, nd.Root); e != nil {
				ferr = e
				break
			}
			if e := g.sites.set(nd.Dense, nd.Site); e != nil {
				ferr = e
				break
			}
		}
		doneNodes <- ferr
	}()
	for e := range edges {
		if e.To < 0 || e.To >= n {
			g.dangle = append(g.dangle, DanglingEdge{From: e.From, RawDense: e.To})
			continue
		}
		if err := writeEdge(e.From, e.To); err != nil {
			ef.Close()
			return nil, err
		}
		edgeCount++
	}
	if err := ef.Close(); err != nil {
		return nil, err
	}
	if err := <-doneNodes; err != nil {
		return nil, err
	}

	outOff, outAdj, err := buildCSR(n, filepath.Join(st.Dir(), "ext-edges.bin"), true, st, budget)
	if err != nil {
		return nil, err
	}
	inOff, inAdj, err := buildCSR(n, filepath.Join(st.Dir(), "ext-edges.bin"), false, st, budget)
	if err != nil {
		return nil, err
	}
	g.offOut, g.adjOut = outOff, outAdj
	g.offIn, g.adjIn = inOff, inAdj
	g.edgeCount = edgeCount
	return g, nil
}
