package retain

import (
	"errors"
	"sort"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/spill"
)

// iarray abstracts the int64 arrays of Lengauer-Tarjan so the same algorithm
// runs on plain slices (small inputs) or paged, disk-backed arrays (large
// inputs). Both implementations are indexable [0,N].
type iarray interface {
	get(i int64) (int64, error)
	set(i, v int64) error
	len() int64
}

type sliceArray struct{ s []int64 }

func (a sliceArray) get(i int64) (int64, error) { return a.s[i], nil }
func (a sliceArray) set(i, v int64) error       { a.s[i] = v; return nil }
func (a sliceArray) len() int64                 { return int64(len(a.s)) }

type pagedArray struct{ p *spill.PagedInt64 }

func (a pagedArray) get(i int64) (int64, error) { return a.p.Get(i) }
func (a pagedArray) set(i, v int64) error       { return a.p.Set(i, v) }
func (a pagedArray) len() int64                 { return a.p.Len() }

// arraySet builds the working arrays. When the graph opts into paging (it
// implements pagedArrayProvider) the LT arrays are disk-backed within the same
// memory budget; otherwise they are ordinary slices.
type pagedArrayProvider interface {
	newPaged(n, fill int64) (iarray, error)
}

func newArrays(g Graph, n int64) ([]iarray, func() error, bool, error) {
	names := 7 // num, vertex, semi, parent, anc, idom, label
	closeFn := func() error { return nil }
	paged := false
	if prov, ok := g.(pagedArrayProvider); ok {
		arrs := make([]iarray, names)
		var closers []func() error
		for i := range arrs {
			fill := int64(0)
			a, err := prov.newPaged(n, fill)
			if err != nil {
				return nil, closeFn, true, err
			}
			arrs[i] = a
			if pa, ok := a.(pagedArray); ok {
				closers = append(closers, pa.p.Close)
			}
		}
		paged = true
		closeFn = func() error {
			var first error
			for _, c := range closers {
				if err := c(); err != nil && first == nil {
					first = err
				}
			}
			return first
		}
		return arrs, closeFn, paged, nil
	}
	arrs := make([]iarray, names)
	for i := range arrs {
		arrs[i] = sliceArray{make([]int64, n)}
	}
	return arrs, closeFn, false, nil
}

// dominators computes immediate dominators with Lengauer-Tarjan over the
// reachability graph augmented by a virtual root at g.N() connected to every
// GC root. Internal arrays are indexed by DFS number 1..N (0 is sentinel).
//
// Determinism: successors are scanned in ascending node order. The working
// arrays are slices for small graphs and paged disk arrays for graphs that
// opt in, keeping peak memory bounded on inputs larger than RAM.
func dominators(g Graph) ([]int64, error) {
	n := g.N()
	root := n
	V := n + 1

	arrs, closeArr, _, err := newArrays(g, V+1)
	if err != nil {
		return nil, err
	}
	defer closeArr()
	numA, vertexA, semiA, parentA, ancA, idomA, labelA := arrs[0], arrs[1], arrs[2], arrs[3], arrs[4], arrs[5], arrs[6]

	// Additional LT state (size/child/bucket). size/child also paged; bucket
	// is a slice-of-slice only in RAM mode. In paged mode bucket lists are
	// kept in spill too (see bucketStore).
	var sizeA, childA iarray
	var bucket bucketStore
	closeExtra := func() error { return nil }
	if prov, ok := g.(pagedArrayProvider); ok {
		sa, err := prov.newPaged(V+1, 1)
		if err != nil {
			return nil, err
		}
		ca, err := prov.newPaged(V+1, 0)
		if err != nil {
			return nil, err
		}
		// fill used semidominator/label/index cells lazily below; size already
		// initialized to 1 by the provider.
		sizeA, childA = sa, ca
		bs, err := newDiskBucketStore(prov)
		if err != nil {
			return nil, err
		}
		bucket = bs
		closeExtra = func() error {
			_ = sa.(pagedArray).p.Close()
			_ = ca.(pagedArray).p.Close()
			return bs.close()
		}
	} else {
		sizeA = sliceArray{make([]int64, V+1)}
		childA = sliceArray{make([]int64, V+1)}
		for i := int64(0); i <= V; i++ {
			sizeA.set(i, 1)
		}
		bucket = memBucket{b: make([][]int64, V+1)}
	}
	defer closeExtra()

	succList := make([][]int64, V) // adjacency in DFS cache; only reachable part
	collect := func(v int64) ([]int64, error) {
		var out []int64
		if v == root {
			for u := int64(0); u < n; u++ {
				if g.IsRoot(u) {
					out = append(out, u)
				}
			}
		} else {
			if err := g.Successors(v, func(to int64) bool {
				if to >= 0 && to < n {
					out = append(out, to)
				}
				return true
			}); err != nil {
				return nil, err
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return dedupInt64(out), nil
	}

	var N int64
	succList[root], _ = collect(root)
	N = 1
	numA.set(root, 1)
	vertexA.set(1, root)
	type frame struct {
		node int64
		next int
	}
	stack := []frame{{node: root, next: 0}}
	for len(stack) > 0 {
		f := &stack[len(stack)-1]
		adj := succList[f.node]
		if f.next >= len(adj) {
			stack = stack[:len(stack)-1]
			continue
		}
		w := adj[f.next]
		f.next++
		nw, _ := numA.get(w)
		if nw != 0 {
			continue
		}
		N++
		numA.set(w, N)
		vertexA.set(N, w)
		parentA.set(N, numP(f.node, numA))
		var err error
		succList[w], err = collect(w)
		if err != nil {
			return nil, err
		}
		stack = append(stack, frame{node: w, next: 0})
	}
	for i := int64(1); i <= N; i++ {
		semiA.set(i, i)
		labelA.set(i, i)
		sizeA.set(i, 1)
	}

	geti := func(a iarray, i int64) int64 {
		v, _ := a.get(i)
		return v
	}

	// iterative compress with explicit path
	compress := func(v int64) {
		var path []int64
		u := v
		for geti(ancA, geti(ancA, u)) != 0 {
			path = append(path, u)
			u = geti(ancA, u)
		}
		for i := len(path) - 1; i >= 0; i-- {
			x := path[i]
			ax := geti(ancA, x)
			if geti(semiA, geti(labelA, ax)) < geti(semiA, geti(labelA, x)) {
				labelA.set(x, geti(labelA, ax))
			}
			ancA.set(x, geti(ancA, ax))
		}
	}
	eval := func(v int64) int64 {
		if geti(ancA, v) == 0 {
			return geti(labelA, v)
		}
		compress(v)
		if geti(semiA, geti(labelA, geti(ancA, v))) >= geti(semiA, geti(labelA, v)) {
			return geti(labelA, v)
		}
		return geti(labelA, geti(ancA, v))
	}
	link := func(v, w int64) {
		// Tarjan's balanced link (global labeling), transcribed literally.
		s := w
		for geti(semiA, geti(labelA, w)) < geti(semiA, geti(labelA, geti(childA, s))) {
			if geti(sizeA, s)+geti(sizeA, geti(childA, geti(childA, s))) >=
				2*geti(sizeA, geti(childA, s)) {
				ancA.set(geti(childA, s), s)
				childA.set(s, geti(childA, geti(childA, s)))
			} else {
				sizeA.set(geti(childA, s), geti(sizeA, s))
				s = geti(childA, s)
			}
		}
		labelA.set(s, geti(labelA, w))
		sizeA.set(v, geti(sizeA, s)+geti(sizeA, w))
		if geti(sizeA, v) < 2*geti(sizeA, w) {
			// s, child[v] = child[s], s
			tmp := s
			s = geti(childA, tmp)
			childA.set(v, tmp)
		}
		for t := geti(childA, v); t != 0; t = geti(childA, t) {
			ancA.set(t, v)
		}
	}

	for i := N; i >= 2; i-- {
		w, _ := vertexA.get(i)
		var preds []int64
		if w != root {
			if err := g.Predecessors(w, func(from int64) bool {
				if from >= 0 && from < n {
					preds = append(preds, from)
				}
				return true
			}); err != nil {
				return nil, err
			}
		}
		if g.IsRoot(w) {
			preds = append(preds, root)
		}
		for _, pnode := range preds {
			pn, _ := numA.get(pnode)
			if pn == 0 {
				continue
			}
			u := eval(pn)
			su := geti(semiA, u)
			si := geti(semiA, i)
			if su < si {
				semiA.set(i, su)
			}
		}
		s := geti(semiA, i)
		bucket.add(s, i)
		pi := geti(parentA, i)
		link(pi, i)
		for _, v := range bucket.take(pi) {
			u := eval(v)
			if geti(semiA, u) < geti(semiA, v) {
				idomA.set(v, u)
			} else {
				idomA.set(v, pi)
			}
		}
	}
	for i := int64(2); i <= N; i++ {
		id := geti(idomA, i)
		s := geti(semiA, i)
		if id != s {
			idomA.set(i, geti(idomA, id))
		}
	}

	out := make([]int64, V)
	for i := range out {
		out[i] = -1
	}
	for d := int64(2); d <= N; d++ {
		node, _ := vertexA.get(d)
		idd := geti(idomA, d) // idom stored as a DFS number
		if idd <= 0 {
			out[node] = -1
			continue
		}
		idomNode, _ := vertexA.get(idd)
		out[node] = idomNode
	}
	return out, nil
}

func numP(node int64, numA iarray) int64 {
	v, _ := numA.get(node)
	return v
}

func dedupInt64(xs []int64) []int64 {
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

// Dominators is the exported entry point (used by tests).
func Dominators(g Graph, opts Options) ([]int64, error) {
	if g == nil {
		return nil, errors.New("retain: nil graph")
	}
	return dominators(g)
}
