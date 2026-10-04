// Package retain computes retained (dominated) sizes on the object
// graph.
//
// Method: build the immediate-dominator tree with the
// Lengauer–Tarjan algorithm, then retained size of a node is the sum
// of its own (estimated) size plus the retained sizes of its children
// in the dominator tree. Because every node appears in exactly one
// dominator subtree, shared references are never double-counted, and
// cycles are handled naturally (a cycle is dominated by its entry
// point). Roots are objects with no incoming references; nodes
// unreachable from any root (e.g. closed cycles) are promoted to
// synthetic roots so nothing is silently dropped.
package retain

import (
	"sort"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/estimate"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/model"
)

// Sizes maps each object to the bytes exclusively retained by it.
type Sizes map[model.ObjectID]int64

// Compute returns per-object retained sizes.
func Compute(objs []model.Object, refs []model.Reference) Sizes {
	n := len(objs)
	if n == 0 {
		return Sizes{}
	}
	idx := make(map[model.ObjectID]int, n)
	size := make([]int64, n)
	ids := make([]model.ObjectID, n)
	for i, o := range objs {
		idx[o.ID] = i
		ids[i] = o.ID
		b, _ := estimate.Object(o)
		size[i] = b
	}
	succ := make([][]int, n)
	indeg := make([]int, n)
	for _, r := range refs {
		f, okF := idx[r.From]
		t, okT := idx[r.To]
		if !okF || !okT || f == t {
			continue
		}
		succ[f] = append(succ[f], t)
		indeg[t]++
	}
	// Roots: in-degree 0 nodes, in deterministic id order.
	var roots []int
	for i := 0; i < n; i++ {
		if indeg[i] == 0 {
			roots = append(roots, i)
		}
	}
	sort.Ints(roots)

	idom := dominators(n, succ, roots)

	// retained[i] = size[i] + sum(retained[c]) over idom children c.
	children := make([][]int, n)
	for i := 0; i < n; i++ {
		if p := idom[i]; p >= 0 {
			children[p] = append(children[p], i)
		}
	}
	retained := make([]int64, n)
	// Iterative post-order over the idom tree (a tree, so no cycles).
	order := make([]int, 0, n)
	visited := make([]bool, n)
	stack := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if idom[i] < 0 && !visited[i] { // tree roots
			stack = append(stack, i)
			for len(stack) > 0 {
				v := stack[len(stack)-1]
				if !visited[v] {
					visited[v] = true
					order = append(order, v)
					stack = append(stack, children[v]...)
				} else {
					stack = stack[:len(stack)-1]
				}
			}
		}
	}
	for i := len(order) - 1; i >= 0; i-- {
		v := order[i]
		total := size[v]
		for _, c := range children[v] {
			total += retained[c]
		}
		retained[v] = total
	}

	out := make(Sizes, n)
	for i := 0; i < n; i++ {
		out[ids[i]] = retained[i]
	}
	return out
}

// BySite aggregates retained sizes per allocation site.
func BySite(objs []model.Object, refs []model.Reference) map[model.SiteID]int64 {
	sizes := Compute(objs, refs)
	out := map[model.SiteID]int64{}
	for _, o := range objs {
		out[o.Site] += sizes[o.ID]
	}
	return out
}

// dominators computes immediate dominators via Lengauer–Tarjan.
// Returns idom per node (-1 for roots of the dominator forest).
// A virtual start node edges to every root so that nodes reachable
// from multiple roots are dominated only by the virtual root, not by
// whichever root the DFS happened to visit first. Nodes unreachable
// from any root (e.g. closed cycles) are promoted to extra roots, so
// every node is covered and the algorithm always terminates.
func dominators(n int, succ [][]int, roots []int) []int {
	// Reachability from the given roots; unreached nodes become
	// extra roots.
	reached := make([]bool, n)
	stack := append([]int(nil), roots...)
	for len(stack) > 0 {
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if reached[v] {
			continue
		}
		reached[v] = true
		stack = append(stack, succ[v]...)
	}
	allRoots := append([]int(nil), roots...)
	for i := 0; i < n; i++ {
		if !reached[i] {
			allRoots = append(allRoots, i)
		}
	}

	// Virtual start node = index n.
	vroot := n
	succV := make([][]int, n+1)
	copy(succV, succ)
	succV[vroot] = allRoots

	// Iterative DFS from the virtual root.
	dfsNum := make([]int, n+1) // 0 = unvisited; numbers start at 1
	vertex := make([]int, 1, n+2)
	vertex[0] = -1 // 1-based
	parent := make([]int, n+1)
	for i := range parent {
		parent[i] = -1
	}
	type frame struct{ v, ci int }
	dfsNum[vroot] = 1
	vertex = append(vertex, vroot)
	dfs := []frame{{v: vroot}}
	for len(dfs) > 0 {
		f := &dfs[len(dfs)-1]
		if f.ci < len(succV[f.v]) {
			w := succV[f.v][f.ci]
			f.ci++
			if dfsNum[w] == 0 {
				dfsNum[w] = len(vertex)
				vertex = append(vertex, w)
				parent[w] = f.v
				dfs = append(dfs, frame{v: w})
			}
		} else {
			dfs = dfs[:len(dfs)-1]
		}
	}

	N := len(vertex) - 1
	semi := make([]int, n+1)
	idom := make([]int, n+1)
	ancestor := make([]int, n+1)
	label := make([]int, n+1)
	bucket := make([][]int, n+1)
	for i := 0; i <= n; i++ {
		semi[i] = dfsNum[i]
		label[i] = i
		idom[i] = -1
		ancestor[i] = -1
	}

	var compress func(v int)
	compress = func(v int) {
		if a := ancestor[v]; a >= 0 && ancestor[a] >= 0 {
			compress(a)
			if semi[label[a]] < semi[label[v]] {
				label[v] = label[a]
			}
			ancestor[v] = ancestor[a]
		}
	}
	eval := func(v int) int {
		if ancestor[v] < 0 {
			return v
		}
		compress(v)
		return label[v]
	}

	// Predecessors.
	pred := make([][]int, n+1)
	for v := 0; v <= n; v++ {
		if dfsNum[v] == 0 {
			continue
		}
		for _, w := range succV[v] {
			if dfsNum[w] != 0 {
				pred[w] = append(pred[w], v)
			}
		}
	}

	for i := N; i >= 2; i-- {
		w := vertex[i]
		for _, v := range pred[w] {
			u := eval(v)
			if semi[u] < semi[w] {
				semi[w] = semi[u]
			}
		}
		bucket[vertex[semi[w]]] = append(bucket[vertex[semi[w]]], w)
		ancestor[w] = parent[w] // link(parent[w], w)
		for _, v := range bucket[parent[w]] {
			u := eval(v)
			if semi[u] < semi[v] {
				idom[v] = u
			} else {
				idom[v] = parent[w]
			}
		}
		bucket[parent[w]] = nil
	}
	for i := 2; i <= N; i++ {
		w := vertex[i]
		if idom[w] >= 0 && idom[w] != vertex[semi[w]] {
			idom[w] = idom[idom[w]]
		}
	}

	// Map virtual root back to -1 (forest root).
	out := make([]int, n)
	for i := 0; i < n; i++ {
		if idom[i] < 0 || idom[i] == vroot {
			out[i] = -1
		} else {
			out[i] = idom[i]
		}
	}
	return out
}
