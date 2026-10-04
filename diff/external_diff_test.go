package diff_test

import (
	"bytes"
	"testing"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/diff"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/index"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/snapshot"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/weight"
)

func TestExternalStreamingDiff(t *testing.T) {
	mk := func(name string, ids []uint64, gap int64) *index.Index {
		var b bytes.Buffer
		w := snapshot.NewJSONLWriter(&b)
		_ = w.WriteMeta(snapshot.Meta{SnapshotID: name})
		_ = w.WriteType(snapshot.TypeInfo{ID: 1, Name: "A", Symbol: true})
		_ = w.WriteSite(snapshot.AllocSite{Key: "k", Symbol: true})
		if gap > 0 {
			_ = w.WriteGap(snapshot.GapRegion{Bytes: gap})
		}
		for i, id := range ids {
			obj := snapshot.ObjectRecord{ID: snapshot.ID(id), TypeID: 1, SiteKey: "k",
				Size: int64(10 + (i % 7))}
			if i%3 == 0 {
				ww, _ := weight.New(2, 1)
				obj.Sample = &snapshot.Sample{Weight: &ww}
			}
			_ = w.WriteObject(obj)
		}
		_ = w.Flush()
		ix, _, err := index.Build(snapshot.NewJSONLReader(bytes.NewReader(b.Bytes())),
			index.Options{MemoryBudgetBytes: 2048, SpillDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		return ix
	}
	before := mk("b", []uint64{1, 2, 3, 4, 5}, 100)
	after := mk("a", []uint64{1, 3, 4, 5, 6, 7}, 200)

	ram := mk
	_ = ram
	rep, err := diff.Compare(before, after, diff.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Entries) != 1 {
		t.Fatalf("want 1 entry got %d", len(rep.Entries))
	}
	e := rep.Entries[0]
	// ID 2 removed; 6,7 added; 1,3,4,5 retained
	if e.ObsRemoved != 1 || e.ObsAdded != 2 {
		t.Fatalf("obs add/rm = %d/%d", e.ObsAdded, e.ObsRemoved)
	}
	var sawGap bool
	for _, sp := range rep.Special {
		if sp.Kind == "missing_sampling" && sp.NetBytes == 100 {
			sawGap = true
		}
	}
	if !sawGap {
		t.Fatalf("gap special missing: %+v", rep.Special)
	}
}
