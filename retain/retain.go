// Package retain computes retained (dominated) sizes from the object graph.
//
// Retained size of an allocation site is the shallow size that would become
// unreachable if the site's objects vanished. It is computed exactly via a
// dominator tree over the reachability graph rooted at a synthetic virtual
// root connected to every GC root:
//
//   - Shared references: an object with multiple retainers is counted only in
//     the subtree of its dominator, never twice.
//   - Cycles: dominators are well defined on cyclic graphs; members of an
//     SCC dominate each other only when reachable solely through the
//     component, so cycles cannot cause infinite traversal or double count.
//   - Site attribution: each node's shallow bytes are charged to the first
//     site encountered walking up the dominator tree (the site that
//     exclusively retains it). Nodes whose idom is the virtual root are
//     charged to that node's own site if it has one; nodes dominated directly
//     by the virtual root with no site go into Unattributed, never into a
//     site.
package retain

// Graph is the read model the dominator pass consumes. Nodes are dense
// indexes [0,n); the implementation introduces a virtual root at index n.
type Graph interface {
	N() int64
	IsRoot(node int64) bool
	NodeSize(node int64) int64
	NodeSite(node int64) string
	// Successors streams strong outgoing edges. Stop if fn returns false.
	Successors(node int64, fn func(to int64) bool) error
	// Predecessors streams strong incoming edges. Stop if fn returns false.
	Predecessors(node int64, fn func(from int64) bool) error
}

// Options controls retained-size computation.
type Options struct {
	SpillDir          string
	MemoryBudgetBytes int64
}

// SiteRetain is the retained attribution for one allocation site.
type SiteRetain struct {
	Site         string
	RetainedSize int64 // bytes exclusively retained by this site
	Dominated    int64 // number of dominated nodes
}

// Report is the full retained-size result.
type Report struct {
	BySite       []SiteRetain // sorted by site id
	Unattributed int64        // bytes dominated only by the virtual root, no site
	TotalShallow int64
	// Unreachable bytes are objects not reachable from any GC root. They are
	// reported separately (not attributable) rather than dropped silently.
	Unreachable int64
}

// GraphStats reports graph-level counters.
type GraphStats struct {
	Nodes int64
	Edges int64
	Roots int64
}

// Stats inspects a *CSRGraph; for other Graph implementations it counts by
// scanning.
func Stats(g Graph) (GraphStats, error) {
	if c, ok := g.(*CSRGraph); ok {
		return GraphStats{Nodes: c.N(), Edges: c.edgeCount(), Roots: c.rootCount()}, nil
	}
	st := GraphStats{Nodes: g.N()}
	for v := int64(0); v < g.N(); v++ {
		if g.IsRoot(v) {
			st.Roots++
		}
		if err := g.Successors(v, func(int64) bool { st.Edges++; return true }); err != nil {
			return st, err
		}
	}
	return st, nil
}
