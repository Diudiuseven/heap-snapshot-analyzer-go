package diff

import (
	"testing"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/aggregate"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/model"
)

func snap(objs ...model.Object) *model.Snapshot {
	return &model.Snapshot{Objects: objs}
}

func find(rep *Report, site model.SiteID) *Entry {
	for i := range rep.Entries {
		if rep.Entries[i].Key.Site == site {
			return &rep.Entries[i]
		}
	}
	return nil
}

func TestCompareClassifies(t *testing.T) {
	oldS := snap(
		model.Object{ID: 1, Size: 100, Site: 1}, // grows
		model.Object{ID: 2, Size: 50, Site: 2},  // collected
		model.Object{ID: 3, Size: 40, Site: 4},  // stable
		model.Object{ID: 4, Size: 30, Site: 5},  // shrinks
	)
	newS := snap(
		model.Object{ID: 1, Size: 150, Site: 1}, // growing
		model.Object{ID: 5, Size: 70, Site: 3},  // new
		model.Object{ID: 3, Size: 40, Site: 4},  // stable
		model.Object{ID: 4, Size: 10, Site: 5},  // shrinking
	)
	rep := Compare(oldS, newS)
	if e := find(rep, 1); e == nil || e.Kind != Growing || e.Delta != 50 {
		t.Fatalf("growing: %+v", e)
	}
	if e := find(rep, 2); e == nil || e.Kind != Collected || e.OldBytes != 50 {
		t.Fatalf("collected: %+v", e)
	}
	if e := find(rep, 3); e == nil || e.Kind != New || e.NewBytes != 70 {
		t.Fatalf("new: %+v", e)
	}
	if e := find(rep, 4); e == nil || e.Kind != Stable {
		t.Fatalf("stable: %+v", e)
	}
	if e := find(rep, 5); e == nil || e.Kind != Shrinking || e.Delta != -20 {
		t.Fatalf("shrinking: %+v", e)
	}
}

func TestUnattributedSeparated(t *testing.T) {
	oldS := snap(
		model.Object{ID: 1, Size: 100, Site: 1},
		model.Object{ID: 2, Size: 60, Site: model.UnknownSite},
	)
	newS := snap(
		model.Object{ID: 1, Size: 100, Site: 1},
		model.Object{ID: 3, Size: 90, Site: model.UnknownSite},
	)
	rep := Compare(oldS, newS)
	if rep.UnattributedOld != 60 || rep.UnattributedNew != 90 {
		t.Fatalf("unattributed: %+v", rep)
	}
	for _, e := range rep.Entries {
		if e.Key.Site == model.UnknownSite {
			t.Fatalf("unattributed leaked into entries: %+v", e)
		}
	}
}

func TestDeterministicEntryOrder(t *testing.T) {
	a := aggregate.New()
	b := aggregate.New()
	for i := uint32(10); i > 0; i-- {
		a.Add(model.Object{ID: model.ObjectID(i), Size: 1, Site: model.SiteID(i)})
		b.Add(model.Object{ID: model.ObjectID(i), Size: 2, Site: model.SiteID(i)})
	}
	r1 := CompareTables(a, b)
	r2 := CompareTables(b, a)
	for i := range r1.Entries {
		if r1.Entries[i].Key != r2.Entries[i].Key {
			t.Fatalf("entry order not canonical")
		}
	}
}
