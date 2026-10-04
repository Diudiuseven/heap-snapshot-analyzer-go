package retain_test

import (
	"testing"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/retain"
)

// diamond+cycle matching a RAM graph, built through the external paged path.
func TestExternalGraphMatchesCSR(t *testing.T) {
	const n = 6
	sites := []string{"A", "B", "B", "C", "B", "B"}
	sizes := []int64{10, 10, 10, 10, 10, 10}
	edges := [][2]int64{
		{0, 1}, {0, 2}, {1, 3}, {2, 3}, // diamond
		{4, 5}, {5, 4}, // cycle among two B nodes rooted at 4
	}
	roots := map[int64]bool{0: true, 4: true}

	feed := func(yield func(retain.ExternalNode) bool) {
		targets := make([][]int64, n)
		for _, e := range edges {
			targets[e[0]] = append(targets[e[0]], e[1])
		}
		for v := int64(0); v < n; v++ {
			if !yield(retain.ExternalNode{
				Dense: v, Size: sizes[v], Site: sites[v],
				Root: roots[v], Targets: targets[v],
			}) {
				return
			}
		}
	}
	g, err := retain.BuildExternal(n, feed, retain.Options{
		MemoryBudgetBytes: 2048, SpillDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	if g.N() != n {
		t.Fatalf("n=%d", g.N())
	}
	rep, err := retain.Compute(g, retain.Options{})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]int64{}
	for _, s := range rep.BySite {
		by[s.Site] = s.RetainedSize
	}
	// Expected (group dominator):
	// diamond: idom 1=0(A),2=0(A),3=0(A). owner(0): virtualroot->site A => A.
	//   owner(1): idom 0 site A == B? different => owner A.
	//   owner(2): A. owner(3): idom0 A vs C => A. => A gets 0,1,2,3 = 40
	// cycle: root4(B)->5 and 5->4. idom4=vroot (root), idom5=4.
	//   owner(4): vroot -> site B. owner(5): idom4 site B same as own B => walk
	//   to 4's owner = B. => B gets 4,5 = 20
	if by["A"] != 40 || by["B"] != 20 {
		t.Fatalf("external retained A=%d B=%d (%+v)", by["A"], by["B"], rep.BySite)
	}
	if rep.TotalShallow != 60 || rep.Unreachable != 0 {
		t.Fatalf("total=%d unreach=%d", rep.TotalShallow, rep.Unreachable)
	}
}

func TestExternalDanglingReported(t *testing.T) {
	feed := func(yield func(retain.ExternalNode) bool) {
		yield(retain.ExternalNode{Dense: 0, Size: 10, Site: "A", Root: true,
			Targets: []int64{999}})
	}
	g, err := retain.BuildExternal(1, feed, retain.Options{SpillDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	d := g.Dangling()
	if len(d) != 1 || d[0].RawDense != 999 {
		t.Fatalf("dangling edge not reported: %+v", d)
	}
	rep, err := retain.Compute(g, retain.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.TotalShallow != 10 {
		t.Fatalf("dangling target must not corrupt analysis, total=%d", rep.TotalShallow)
	}
}
