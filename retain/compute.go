package retain

import "sort"

// Compute runs dominator analysis and aggregates retained size per site.
//
// Attribution is the standard dominator-based "group retained size": treat
// all objects of one site as a group. A node v is exclusively retained by
// site S iff removing every object of S (keeping all other sites and GC
// roots) makes v unreachable. On the dominator tree this is exactly:
//
//	owner(v) = nearest ancestor on the idom chain whose site != site(v);
//	           if that ancestor is the virtual root, owner = site(v)
//	           (a directly-rooted site owns itself);
//	           if no such site exists but v is reachable, it is unattributed.
//
// Each reachable node is credited exactly once because every node has one
// owner. Shared references land in the single gating site; reference cycles
// are harmless because the dominator tree is acyclic. Objects unreachable
// from any GC root are reported as Unreachable, never folded into a site.
func Compute(g Graph, opts Options) (*Report, error) {
	idom, err := dominators(g)
	if err != nil {
		return nil, err
	}
	n := g.N()
	root := n

	siteOf := make([]string, n)
	reachable := make([]bool, n)
	rep := &Report{}
	for v := int64(0); v < n; v++ {
		siteOf[v] = g.NodeSite(v)
		rep.TotalShallow += g.NodeSize(v)
	}
	for v := int64(0); v < n; v++ {
		if idom[v] != -1 {
			reachable[v] = true
		} else {
			rep.Unreachable += g.NodeSize(v)
		}
	}

	// ownerSite returns the owning site id ("" = unattributed) for a node.
	// Memoized because the idom walk can be long; total work is O(n alpha).
	owner := make([]string, n)
	done := make([]bool, n)
	var walk func(v int64) string
	walk = func(v int64) string {
		if done[v] {
			return owner[v]
		}
		done[v] = true
		d := idom[v]
		mySite := siteOf[v]
		if d == root {
			owner[v] = mySite // directly rooted: owns itself ("" if no site)
			return owner[v]
		}
		if d == -1 {
			owner[v] = ""
			return ""
		}
		if siteOf[d] != mySite {
			if siteOf[d] == "" {
				owner[v] = walk(d)
			} else {
				owner[v] = siteOf[d]
			}
		} else {
			owner[v] = walk(d)
		}
		return owner[v]
	}

	agg := map[string]*SiteRetain{}
	for v := int64(0); v < n; v++ {
		if !reachable[v] {
			continue
		}
		own := walk(v)
		if own == "" {
			rep.Unattributed += g.NodeSize(v)
			continue
		}
		a := agg[own]
		if a == nil {
			a = &SiteRetain{Site: own}
			agg[own] = a
		}
		a.RetainedSize += g.NodeSize(v)
		a.Dominated++
	}
	for _, a := range agg {
		rep.BySite = append(rep.BySite, *a)
	}
	sort.Slice(rep.BySite, func(i, j int) bool { return rep.BySite[i].Site < rep.BySite[j].Site })
	return rep, nil
}
