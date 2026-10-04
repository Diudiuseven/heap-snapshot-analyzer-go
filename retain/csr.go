package retain

// CSRGraph is an in-memory compressed-sparse-row Graph with both forward and
// reverse adjacency, used for small snapshots and deterministic tests.
type CSRGraph struct {
	n      int64
	sizes  []int64
	sites  []string
	roots  []bool
	offOut []int64
	adjOut []int64
	offIn  []int64
	adjIn  []int64
}

// RAMCSR is a builder for CSRGraph. Add edges freely (forward references are
// fine), then Build compacts them into CSR.
type RAMCSR struct {
	n     int64
	sizes []int64
	sites []string
	roots []bool
	out   [][]int64
	in    [][]int64
}

// NewRAMCSR allocates an empty graph with n nodes (no edges).
func NewRAMCSR(n int64) *RAMCSR {
	g := &RAMCSR{n: n}
	g.sizes = make([]int64, n)
	g.sites = make([]string, n)
	g.roots = make([]bool, n)
	g.out = make([][]int64, n)
	g.in = make([][]int64, n)
	return g
}

// AddEdge adds a strong edge from -> to. Duplicates are preserved (self
// edges are allowed and handled by the dominator pass).
func (g *RAMCSR) AddEdge(from, to int64) {
	g.out[from] = append(g.out[from], to)
	g.in[to] = append(g.in[to], from)
}

// SetRoot marks a node as a GC root.
func (g *RAMCSR) SetRoot(node int64, root bool) { g.roots[node] = root }

// SetSize sets shallow size.
func (g *RAMCSR) SetSize(node int64, size int64) { g.sizes[node] = size }

// SetSite assigns the canonical allocation site.
func (g *RAMCSR) SetSite(node int64, site string) { g.sites[node] = site }

// N returns node count.
func (g *RAMCSR) N() int64 { return g.n }

// Build freezes adjacency slices into a compact CSR. Edges are sorted and
// deduplicated per source for deterministic traversal order.
func (g *RAMCSR) Build() (*CSRGraph, error) {
	c := &CSRGraph{n: g.n, sizes: g.sizes, sites: g.sites, roots: g.roots}
	c.offOut = make([]int64, g.n+1)
	c.offIn = make([]int64, g.n+1)
	for i := int64(0); i < g.n; i++ {
		es := dedupSort(g.out[i])
		c.adjOut = append(c.adjOut, es...)
		c.offOut[i+1] = int64(len(c.adjOut))
		rs := dedupSort(g.in[i])
		c.adjIn = append(c.adjIn, rs...)
		c.offIn[i+1] = int64(len(c.adjIn))
	}
	return c, nil
}

func dedupSort(xs []int64) []int64 {
	// insertion-free: use simple in-place sort then dedupe
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
			xs[j-1], xs[j] = xs[j], xs[j-1]
		}
	}
	if len(xs) < 2 {
		return xs
	}
	w := 1
	for r := 1; r < len(xs); r++ {
		if xs[r] != xs[w-1] {
			xs[w] = xs[r]
			w++
		}
	}
	return xs[:w]
}

func (g *CSRGraph) N() int64                   { return g.n }
func (g *CSRGraph) IsRoot(node int64) bool     { return g.roots[node] }
func (g *CSRGraph) NodeSize(node int64) int64  { return g.sizes[node] }
func (g *CSRGraph) NodeSite(node int64) string { return g.sites[node] }
func (g *CSRGraph) edgeCount() int64           { return g.offOut[g.n] }
func (g *CSRGraph) rootCount() int64 {
	var rc int64
	for _, r := range g.roots {
		if r {
			rc++
		}
	}
	return rc
}

// Successors streams strong outgoing edges into fn; stop if fn returns false.
func (g *CSRGraph) Successors(node int64, fn func(to int64) bool) error {
	lo, hi := g.offOut[node], g.offOut[node+1]
	for e := lo; e < hi; e++ {
		if !fn(g.adjOut[e]) {
			return nil
		}
	}
	return nil
}

// Predecessors streams strong incoming edges into fn.
func (g *CSRGraph) Predecessors(node int64, fn func(from int64) bool) error {
	lo, hi := g.offIn[node], g.offIn[node+1]
	for e := lo; e < hi; e++ {
		if !fn(g.adjIn[e]) {
			return nil
		}
	}
	return nil
}
