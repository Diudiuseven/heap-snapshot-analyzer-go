package index_test

import (
	"bytes"
	"testing"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/index"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/snapshot"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/weight"
)

// TestExternalMatchesRAM builds the same logical snapshot in RAM and in
// bounded mode with a tiny budget and requires identical cell estimates.
func TestExternalMatchesRAM(t *testing.T) {
	var buf bytes.Buffer
	w := snapshot.NewJSONLWriter(&buf)
	_ = w.WriteMeta(snapshot.Meta{SnapshotID: "big", TakenAtMs: 1})
	_ = w.WriteType(snapshot.TypeInfo{ID: 1, Name: "A", Symbol: true})
	_ = w.WriteType(snapshot.TypeInfo{ID: 2, Name: "B", Symbol: true})
	_ = w.WriteSite(snapshot.AllocSite{Key: "s1", Symbol: true})
	_ = w.WriteSite(snapshot.AllocSite{Key: "s2", Symbol: true})
	for i := 0; i < 5000; i++ {
		obj := snapshot.ObjectRecord{
			ID:      snapshot.ID(i + 1),
			TypeID:  uint64(1 + i%2),
			SiteKey: [...]string{"s1", "s2"}[i%2],
			Size:    int64(8 + i%32),
		}
		if i%5 == 0 {
			ww, _ := weight.New(3, 1)
			obj.Sample = &snapshot.Sample{Weight: &ww}
		}
		if i < 4999 {
			obj.Strong = []snapshot.Edge{{To: snapshot.ID(i + 2), Strong: true}}
		}
		_ = w.WriteObject(obj)
	}
	_ = w.WriteGap(snapshot.GapRegion{Bytes: 77})
	_ = w.Flush()

	newReader := func() *snapshot.JSONLReader { return snapshot.NewJSONLReader(bytes.NewReader(buf.Bytes())) }
	ram, _, err := index.Build(newReader(), index.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ext, stats, err := index.Build(newReader(), index.Options{
		MemoryBudgetBytes: 4096, SpillDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Spilled {
		t.Fatal("expected spilled build")
	}
	if stats.Objects != 5000 || stats.Edges != 4999 {
		t.Fatalf("stats %+v", stats)
	}
	rc, rb, _ := ram.EstimatedTotals()
	ec, eb, _ := ext.EstimatedTotals()
	if rc != ec || rb != eb {
		t.Fatalf("totals differ RAM(%d,%d) EXT(%d,%d)", rc, rb, ec, eb)
	}
	if len(ram.Cells()) != len(ext.Cells()) {
		t.Fatalf("cell count %d vs %d", len(ram.Cells()), len(ext.Cells()))
	}
	for i := range ram.Cells() {
		a, b := ram.Cells()[i], ext.Cells()[i]
		if a.Key != b.Key || a.CountEst != b.CountEst || a.BytesEst != b.BytesEst ||
			a.Observed != b.Observed {
			t.Fatalf("cell %d mismatch %+v vs %+v", i, a, b)
		}
	}
	if len(ext.Gaps()) != 1 || ext.Gaps()[0].Bytes != 77 {
		t.Fatalf("gap lost in external: %+v", ext.Gaps())
	}

	// object iterator sorted by cell
	it, err := ext.IterObjectsByCell()
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	var n int64
	var prev index.ObjInfo
	first := true
	for {
		o, err := it.Next()
		if err != nil {
			break
		}
		if !first {
			if o.Site < prev.Site || (o.Site == prev.Site && (o.Type < prev.Type ||
				(o.Type == prev.Type && o.ID < prev.ID))) {
				t.Fatal("external object iterator not sorted")
			}
		}
		prev = o
		first = false
		n++
	}
	if n != 5000 {
		t.Fatalf("iterated %d objects", n)
	}

	// point lookup via scan
	if _, ok := ext.Object(snapshot.ID(2500)); !ok {
		t.Fatal("object lookup failed in external mode")
	}
}
