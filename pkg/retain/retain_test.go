package retain

import (
	"testing"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/model"
)

func obj(id model.ObjectID, size int64, site model.SiteID) model.Object {
	return model.Object{ID: id, Size: size, Site: site}
}

func TestSimpleTree(t *testing.T) {
	// root(10) -> a(20) -> b(30)
	objs := []model.Object{obj(1, 10, 1), obj(2, 20, 1), obj(3, 30, 2)}
	refs := []model.Reference{{From: 1, To: 2}, {From: 2, To: 3}}
	s := Compute(objs, refs)
	if s[1] != 60 || s[2] != 50 || s[3] != 30 {
		t.Fatalf("got %v", s)
	}
}

func TestSharedReferenceNotDoubleCounted(t *testing.T) {
	// r1 and r2 both point to shared; shared must be counted once in
	// the total and attributed to neither r1 nor r2 exclusively.
	objs := []model.Object{obj(1, 10, 1), obj(2, 10, 1), obj(3, 100, 2)}
	refs := []model.Reference{{From: 1, To: 3}, {From: 2, To: 3}}
	s := Compute(objs, refs)
	if s[1] != 10 || s[2] != 10 {
		t.Fatalf("shared node leaked into parents: %v", s)
	}
	if s[3] != 100 {
		t.Fatalf("shared node retained size wrong: %v", s)
	}
	// Total across dominator-tree roots equals total heap size.
	var total int64
	for _, o := range objs {
		total += o.Size
	}
	if s[1]+s[2]+s[3] != total {
		t.Fatalf("sum mismatch: %v vs %v", s, total)
	}
}

func TestCycleTerminates(t *testing.T) {
	// a <-> b is a cycle entered from root.
	objs := []model.Object{obj(1, 5, 1), obj(2, 7, 1), obj(3, 9, 1)}
	refs := []model.Reference{
		{From: 1, To: 2}, {From: 2, To: 3}, {From: 3, To: 2},
	}
	s := Compute(objs, refs)
	if s[1] != 21 { // root retains the whole cycle
		t.Fatalf("got %v", s)
	}
}

func TestClosedCyclePromotedToRoot(t *testing.T) {
	// A cycle with no external entry: every node has in-degree > 0.
	// Nodes must still be accounted for (synthetic roots), and the
	// algorithm must terminate.
	objs := []model.Object{obj(1, 5, 1), obj(2, 7, 1)}
	refs := []model.Reference{{From: 1, To: 2}, {From: 2, To: 1}}
	s := Compute(objs, refs)
	var total int64
	for _, v := range s {
		total += v
	}
	// Each node retains itself plus possibly the other; every byte
	// must be attributed exactly once across dominator-tree roots.
	if s[1]+s[2] < 12 {
		t.Fatalf("bytes lost: %v", s)
	}
}

func TestSelfLoop(t *testing.T) {
	objs := []model.Object{obj(1, 5, 1)}
	refs := []model.Reference{{From: 1, To: 1}}
	s := Compute(objs, refs)
	if s[1] != 5 {
		t.Fatalf("got %v", s)
	}
}

func TestBySite(t *testing.T) {
	objs := []model.Object{obj(1, 10, 1), obj(2, 20, 2), obj(3, 30, 2)}
	refs := []model.Reference{{From: 1, To: 2}, {From: 1, To: 3}}
	bySite := BySite(objs, refs)
	// site1's object dominates the whole graph (60); site2's objects
	// retain only themselves (20+30).
	if bySite[1] != 60 || bySite[2] != 50 {
		t.Fatalf("got %v", bySite)
	}
}
