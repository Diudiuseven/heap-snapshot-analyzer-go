package retain_test

import (
	"testing"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/retain"
)

// diamond:
//
//	root(0,A,10) -> 1(B,10) -> 3(shared C,10)
//	             \-> 2(B,10) -> 3
//
// 3 is dominated by 0 (shared); removing either 1 or 2 keeps 3 alive, but
// removing 0 kills everything.
func TestDiamondSharedNoDoubleCount(t *testing.T) {
	g := retain.NewRAMCSR(4)
	sites := []string{"A", "B", "B", "C"}
	for i, s := range sites {
		g.SetSite(int64(i), s)
		g.SetSize(int64(i), 10)
	}
	g.SetRoot(0, true)
	g.AddEdge(0, 1)
	g.AddEdge(0, 2)
	g.AddEdge(1, 3)
	g.AddEdge(2, 3)
	csr, err := g.Build()
	if err != nil {
		t.Fatal(err)
	}
	idom, err := retain.Dominators(csr, retain.Options{})
	if err != nil {
		t.Fatal(err)
	}
	vroot := int64(4)
	if idom[0] != vroot {
		t.Fatalf("idom[0]=%d want vroot", idom[0])
	}
	if idom[1] != 0 || idom[2] != 0 {
		t.Fatalf("idom 1/2 = %d/%d want 0", idom[1], idom[2])
	}
	if idom[3] != 0 {
		t.Fatalf("shared node 3 must be dominated by root node 0, got %d", idom[3])
	}
	rep, err := retain.Compute(csr, retain.Options{})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]int64{}
	for _, s := range rep.BySite {
		by[s.Site] = s.RetainedSize
	}
	// Site A's root node dominates everything: 40 bytes total.
	if by["A"] != 40 {
		t.Fatalf("A retained=%d want 40 (%+v)", by["A"], rep.BySite)
	}
	if _, hasB := by["B"]; hasB {
		t.Fatalf("B nodes are inside A subtree; B must not own bytes, got %d", by["B"])
	}
	if rep.TotalShallow != 40 {
		t.Fatalf("total shallow %d", rep.TotalShallow)
	}
}

// cycle: root 0(A) -> 1(B) <-> 2(B); 1 -> 3(C)
// Nodes 1 and 2 are mutually reachable; dominator is 0 for both.
func TestCycleNoInfiniteNoDoubleCount(t *testing.T) {
	g := retain.NewRAMCSR(4)
	g.SetSite(0, "A")
	g.SetSite(1, "B")
	g.SetSite(2, "B")
	g.SetSite(3, "C")
	for i := 0; i < 4; i++ {
		g.SetSize(int64(i), 10)
	}
	g.SetRoot(0, true)
	g.AddEdge(0, 1)
	g.AddEdge(1, 2)
	g.AddEdge(2, 1)
	g.AddEdge(1, 3)
	csr, _ := g.Build()
	rep, err := retain.Compute(csr, retain.Options{})
	if err != nil {
		t.Fatalf("cycle must terminate: %v", err)
	}
	by := map[string]int64{}
	for _, s := range rep.BySite {
		by[s.Site] = s.RetainedSize
	}
	// Remove B {1,2}: node 3(C) was reachable only through 1, so B retains
	// C (10). Remove A {0}: nodes 0,1,2 die -> A retains 30.
	if by["A"] != 30 || by["B"] != 10 {
		t.Fatalf("want A=30 B=10, got %+v", rep.BySite)
	}
	if rep.Unattributed != 0 || rep.Unreachable != 0 {
		t.Fatalf("unexpected unattr/unreach: %+v", rep)
	}
	sum := by["A"] + by["B"]
	if sum != 40 {
		t.Fatalf("total retained %d want 40 (no double count)", sum)
	}
}

// exclusive ownership: two roots from different sites, each owning a private
// subtree. Root 0(A) -> 2(C), root 1(B) -> 3(D).
func TestExclusiveOwnership(t *testing.T) {
	g := retain.NewRAMCSR(4)
	sites := []string{"A", "B", "C", "D"}
	for i, s := range sites {
		g.SetSite(int64(i), s)
		g.SetSize(int64(i), 10)
	}
	g.SetRoot(0, true)
	g.SetRoot(1, true)
	g.AddEdge(0, 2)
	g.AddEdge(1, 3)
	csr, _ := g.Build()
	rep, err := retain.Compute(csr, retain.Options{})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]int64{}
	dom := map[string]int64{}
	for _, s := range rep.BySite {
		by[s.Site] = s.RetainedSize
		dom[s.Site] = s.Dominated
	}
	if by["A"] != 20 || by["B"] != 20 {
		t.Fatalf("want A=20 B=20, got %+v", rep.BySite)
	}
	if by["C"] != 0 || dom["C"] != 0 {
		t.Fatalf("C is inside A subtree and must not own bytes: %+v", rep.BySite)
	}
	// total accounted once
	var sum int64
	for _, s := range rep.BySite {
		sum += s.RetainedSize
	}
	sum += rep.Unattributed
	if sum != 40 {
		t.Fatalf("retained total %d want 40", sum)
	}
}

// unreachable node reported separately, never attributed
func TestUnreachable(t *testing.T) {
	g := retain.NewRAMCSR(2)
	g.SetSite(0, "A")
	g.SetSite(1, "B")
	g.SetSize(0, 10)
	g.SetSize(1, 5)
	g.SetRoot(0, true)
	// no edge 0->1
	csr, _ := g.Build()
	rep, err := retain.Compute(csr, retain.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Unreachable != 5 {
		t.Fatalf("unreachable=%d want 5", rep.Unreachable)
	}
	for _, s := range rep.BySite {
		if s.Site == "B" {
			t.Fatal("B is unreachable, must not be attributed")
		}
	}
}

// weak-only retention: an object only referenced weakly is unreachable from
// the strong graph (Graph only models strong edges by contract).
func TestDeterminism(t *testing.T) {
	mk := func() *retain.CSRGraph {
		g := retain.NewRAMCSR(5)
		for i := 0; i < 5; i++ {
			g.SetSize(int64(i), 1)
			g.SetSite(int64(i), "A")
		}
		g.SetRoot(0, true)
		g.AddEdge(0, 4)
		g.AddEdge(0, 1)
		g.AddEdge(1, 3)
		g.AddEdge(4, 2)
		c, _ := g.Build()
		return c
	}
	r1, _ := retain.Dominators(mk(), retain.Options{})
	r2, _ := retain.Dominators(mk(), retain.Options{})
	if len(r1) != len(r2) {
		t.Fatal("length differs")
	}
	for i := range r1 {
		if r1[i] != r2[i] {
			t.Fatalf("nondeterministic idom at %d: %d vs %d", i, r1[i], r2[i])
		}
	}
}
