package index_test

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/index"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/snapshot"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/weight"
)

type objSpec struct {
	id      snapshot.ID
	typeID  uint64
	siteKey string
	size    int64
	weight  []int64 // n,d; nil = enumerated
	root    bool
}

func buildStream(t *testing.T, specs []objSpec, shuffle bool) *index.Index {
	t.Helper()
	// deterministic shuffle (order-independence)
	order := make([]int, len(specs))
	for i := range order {
		order[i] = i
	}
	if shuffle {
		rng := rand.New(rand.NewSource(7))
		rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
	}
	var b bytes.Buffer
	w := snapshot.NewJSONLWriter(&b)
	_ = w.WriteMeta(snapshot.Meta{SnapshotID: "s", TakenAtMs: 1})
	_ = w.WriteType(snapshot.TypeInfo{ID: 1, Name: "A", Symbol: true})
	_ = w.WriteSite(snapshot.AllocSite{Key: "k", Symbol: true})
	_ = w.WriteGap(snapshot.GapRegion{Bytes: 500, Note: "g"})
	for _, pi := range order {
		s := specs[pi]
		o := snapshot.ObjectRecord{ID: s.id, TypeID: s.typeID, SiteKey: s.siteKey,
			Size: s.size, IsRoot: s.root, RootKind: rootKind(s.root)}
		if s.weight != nil {
			ww, _ := weight.New(s.weight[0], s.weight[1])
			o.Sample = &snapshot.Sample{Weight: &ww}
		}
		_ = w.WriteObject(o)
	}
	_ = w.Flush()
	ix, _, err := index.Build(snapshot.NewJSONLReader(&b), index.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func rootKind(r bool) string {
	if r {
		return "stack"
	}
	return ""
}

func TestSamplingEstimateAndOrder(t *testing.T) {
	// 2 enumerated A@k objects of size 10 (weight 1 each)
	// 3 sampled observations with weight 2 and size 10 => count +6, bytes +60
	specs := []objSpec{
		{id: 1, typeID: 1, siteKey: "k", size: 10},
		{id: 2, typeID: 1, siteKey: "k", size: 10},
		{id: 3, typeID: 1, siteKey: "k", size: 10, weight: []int64{2, 1}},
		{id: 4, typeID: 1, siteKey: "k", size: 10, weight: []int64{2, 1}},
		{id: 5, typeID: 1, siteKey: "k", size: 10, weight: []int64{2, 1}},
	}
	a := buildStream(t, specs, false)
	b := buildStream(t, specs, true)
	ca := a.Cells()
	cb := b.Cells()
	if len(ca) != 1 || len(cb) != 1 {
		t.Fatalf("want 1 cell, got %d/%d", len(ca), len(cb))
	}
	if ca[0].CountEst != 8 || ca[0].BytesEst != 80 {
		t.Fatalf("estimate = count %d bytes %d, want 8/80", ca[0].CountEst, ca[0].BytesEst)
	}
	if cb[0].CountEst != ca[0].CountEst || cb[0].BytesEst != ca[0].BytesEst ||
		cb[0].CountExact == nil || string(cb[0].CountExact) != string(ca[0].CountExact) {
		t.Fatalf("shuffled build differs: %+v vs %+v", ca[0], cb[0])
	}
	cnt, by, obs := a.EstimatedTotals()
	if cnt != 8 || by != 80 || obs != 50 {
		t.Fatalf("totals %d/%d/%d", cnt, by, obs)
	}
	if len(a.Gaps()) != 1 || a.Gaps()[0].Bytes != 500 {
		t.Fatalf("gap must be separate, got %+v", a.Gaps())
	}
}

func TestMissingSymbolsDistinguishable(t *testing.T) {
	var buf bytes.Buffer
	w := snapshot.NewJSONLWriter(&buf)
	_ = w.WriteMeta(snapshot.Meta{SnapshotID: "s", TakenAtMs: 1})
	_ = w.WriteType(snapshot.TypeInfo{ID: 9, Symbol: false}) // no name
	// object references an undeclared site key with no symbols
	_ = w.WriteObject(snapshot.ObjectRecord{ID: 1, TypeID: 9, SiteKey: "?", Size: 8})
	_ = w.WriteObject(snapshot.ObjectRecord{ID: 2, TypeID: 9, SiteKey: "?", Size: 8,
		Unres: []snapshot.RawRef{{Token: "0xdead", Offset: 3, Original: "R"}}})
	_ = w.Flush()
	ix, _, err := index.Build(snapshot.NewJSONLReader(&buf), index.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cells := ix.Cells()
	for _, c := range cells {
		s, _ := ix.Symbols().Site(c.Key.Site)
		ty, _ := ix.Symbols().Type(c.Key.Type)
		if !s.Missing || !ty.Missing {
			t.Fatalf("both site and type should be flagged missing: %+v %+v", s, ty)
		}
	}
	u := ix.Unresolved()
	if len(u) != 1 || u[0].From != 2 || u[0].Token != "0xdead" {
		t.Fatalf("unresolved not preserved: %+v", u)
	}
}

func TestDuplicateObjectRejected(t *testing.T) {
	specs := []objSpec{
		{id: 1, typeID: 1, siteKey: "k", size: 1},
		{id: 1, typeID: 1, siteKey: "k", size: 1},
	}
	var b bytes.Buffer
	w := snapshot.NewJSONLWriter(&b)
	_ = w.WriteMeta(snapshot.Meta{SnapshotID: "s"})
	_ = w.WriteType(snapshot.TypeInfo{ID: 1, Name: "A", Symbol: true})
	_ = w.WriteSite(snapshot.AllocSite{Key: "k", Symbol: true})
	for _, s := range specs {
		_ = w.WriteObject(snapshot.ObjectRecord{ID: s.id, TypeID: 1, SiteKey: "k", Size: s.size})
	}
	_ = w.Flush()
	if _, _, err := index.Build(snapshot.NewJSONLReader(&b), index.Options{}); err == nil {
		t.Fatal("duplicate object id must be rejected")
	}
}
