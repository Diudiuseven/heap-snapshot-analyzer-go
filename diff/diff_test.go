package diff_test

import (
	"bytes"
	"testing"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/diff"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/index"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/snapshot"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/weight"
)

type o = snapshot.ObjectRecord

func buildIdx(t *testing.T, id string, objs []snapshot.ObjectRecord, gap int64) *index.Index {
	t.Helper()
	var b bytes.Buffer
	w := snapshot.NewJSONLWriter(&b)
	_ = w.WriteMeta(snapshot.Meta{SnapshotID: id, TakenAtMs: 1})
	_ = w.WriteType(snapshot.TypeInfo{ID: 1, Name: "A", Symbol: true})
	_ = w.WriteSite(snapshot.AllocSite{Key: "k", Symbol: true})
	if gap > 0 {
		_ = w.WriteGap(snapshot.GapRegion{Bytes: gap})
	}
	for _, obj := range objs {
		_ = w.WriteObject(obj)
	}
	_ = w.Flush()
	ix, _, err := index.Build(snapshot.NewJSONLReader(&b), index.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func wobj(id snapshot.ID, site string, size int64, wn int64) snapshot.ObjectRecord {
	obj := snapshot.ObjectRecord{ID: id, TypeID: 1, SiteKey: site, Size: size}
	if wn > 1 {
		ww, _ := weight.New(wn, 1)
		obj.Sample = &snapshot.Sample{Weight: &ww}
	}
	return obj
}

func TestAddRemoveRetain(t *testing.T) {
	before := buildIdx(t, "b", []o{
		wobj(1, "k", 10, 1),
		wobj(2, "k", 10, 1), // removed
		wobj(3, "k", 10, 1), // retained same size
	}, 0)
	after := buildIdx(t, "a", []o{
		wobj(1, "k", 10, 1),
		wobj(3, "k", 20, 1), // retained, grew
		wobj(4, "k", 10, 1), // added
	}, 0)
	rep, err := diff.Compare(before, after, diff.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(rep.Entries))
	}
	e := rep.Entries[0]
	if e.AddedBytes != 10 || e.RemovedBytes != 10 || e.RetainedBefore != 20 || e.RetainedAfter != 30 {
		t.Fatalf("unexpected entry: %+v", e)
	}
	// net = added(10) + retained after(30) - removed(10) - retained before(20) = 10
	if e.NetBytes != 10 || e.GrowthBytes != 10 {
		t.Fatalf("net/growth = %d/%d", e.NetBytes, e.GrowthBytes)
	}
}

func TestSampledEstimatesAndOrder(t *testing.T) {
	// before: object 1 weight2 size10 => est 20
	// after: object 1 weight2 size10 (same), object 2 weight2 size10 added => +20
	before := buildIdx(t, "b", []o{wobj(1, "k", 10, 2)}, 0)
	after := buildIdx(t, "a", []o{wobj(1, "k", 10, 2), wobj(2, "k", 10, 2)}, 0)
	rep, err := diff.Compare(before, after, diff.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e := rep.Entries[0]
	if e.AddedBytes != 20 || e.NetBytes != 20 {
		t.Fatalf("sampled added est = %d, net = %d", e.AddedBytes, e.NetBytes)
	}
	if !e.Sampled {
		t.Fatal("sampled flag must propagate")
	}
}

func TestSpecialsSeparate(t *testing.T) {
	// one known-site object + one object with undeclared missing site key
	mk := func(id string, gap int64, withMissing bool, unres bool) *index.Index {
		var b bytes.Buffer
		w := snapshot.NewJSONLWriter(&b)
		_ = w.WriteMeta(snapshot.Meta{SnapshotID: id})
		_ = w.WriteType(snapshot.TypeInfo{ID: 1, Name: "A", Symbol: true})
		_ = w.WriteSite(snapshot.AllocSite{Key: "k", Symbol: true})
		_ = w.WriteObject(wobj(1, "k", 10, 1))
		if withMissing {
			obj := wobj(2, "", 7, 1)
			if unres {
				obj.Unres = []snapshot.RawRef{{Token: "0xff"}}
			}
			_ = w.WriteObject(obj)
		}
		if gap > 0 {
			_ = w.WriteGap(snapshot.GapRegion{Bytes: gap})
		}
		_ = w.Flush()
		ix, _, err := index.Build(snapshot.NewJSONLReader(&b), index.Options{})
		if err != nil {
			t.Fatal(err)
		}
		return ix
	}
	before := mk("b", 100, false, false)
	after := mk("a", 250, true, true)
	rep, err := diff.Compare(before, after, diff.Options{})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]diff.Special{}
	for _, sp := range rep.Special {
		kinds[sp.Kind] = sp
	}
	g, ok := kinds["missing_sampling"]
	if !ok || g.BytesBefore != 100 || g.BytesAfter != 250 || g.NetBytes != 150 {
		t.Fatalf("missing_sampling wrong: %+v", kinds)
	}
	u, ok := kinds["unattributed"]
	if !ok || u.BytesAfter != 7 {
		t.Fatalf("unattributed wrong: %+v", kinds)
	}
	// gap/unattributed must not appear as normal cells
	for _, e := range rep.Entries {
		if e.Site == "" {
			t.Fatalf("missing/unattributed leaked into a cell: %+v", e)
		}
	}
}

func TestGrowthMustLandOnSite(t *testing.T) {
	before := buildIdx(t, "b", []o{wobj(1, "k", 10, 1)}, 0)
	after := buildIdx(t, "a", []o{
		wobj(1, "k", 10, 1),
		wobj(2, "k", 40, 1),
	}, 0)
	rep, _ := diff.Compare(before, after, diff.Options{})
	var totalGrowth int64
	for _, e := range rep.Entries {
		totalGrowth += e.GrowthBytes
		if e.Site == "" {
			t.Fatal("growth attributed to empty site")
		}
	}
	if totalGrowth != 40 {
		t.Fatalf("growth total %d want 40", totalGrowth)
	}
}
