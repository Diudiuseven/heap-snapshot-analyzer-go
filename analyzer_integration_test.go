package analyzer_test

import (
	"bytes"
	"testing"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/index"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/leak"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/retain"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/snapshot"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/store"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/weight"
)

// snapshotGen generates a time series where site "leak" grows every snapshot,
// site "churn" adds and reclaims (net ~0), and a sampling gap is emitted.
func snapshotGen(t *testing.T, id string, at int64, leakSize int64, gap bool) *snapshot.JSONLReader {
	var b bytes.Buffer
	w := snapshot.NewJSONLWriter(&b)
	_ = w.WriteMeta(snapshot.Meta{SnapshotID: id, TakenAtMs: at, Format: "jsonl/v1"})
	_ = w.WriteType(snapshot.TypeInfo{ID: 1, Name: "Leak", Symbol: true})
	_ = w.WriteType(snapshot.TypeInfo{ID: 2, Name: "Churn", Symbol: true})
	_ = w.WriteSite(snapshot.AllocSite{Key: "leak", Symbol: true})
	_ = w.WriteSite(snapshot.AllocSite{Key: "churn", Symbol: true})
	if gap {
		_ = w.WriteGap(snapshot.GapRegion{Bytes: 64, Note: "skip"})
	}
	var idn snapshot.ID
	emit := func(site string, typeID uint64, size int64, weightN int64, root bool, to snapshot.ID) {
		idn++
		o := snapshot.ObjectRecord{ID: idn, TypeID: typeID, SiteKey: site, Size: size}
		if root {
			o.IsRoot = true
			o.RootKind = "stack"
		}
		if weightN > 1 {
			ww, _ := weight.New(weightN, 1)
			o.Sample = &snapshot.Sample{Weight: &ww}
		}
		if to != 0 {
			o.Strong = []snapshot.Edge{{To: to, Strong: true}}
		}
		_ = w.WriteObject(o)
	}
	// leak chain: root -> a -> b -> c, growing size
	var prev snapshot.ID
	for i := int64(0); i < leakSize; i++ {
		root := i == 0
		var to snapshot.ID
		if i > 0 {
			to = prev
		}
		emit("leak", 1, 100+int64(idn)%50, 1, root, to)
		prev = idn
	}
	// churn: two short-lived objects each snapshot
	emit("churn", 2, 10, 2, false, 0)
	emit("churn", 2, 10, 2, false, 0)
	_ = w.Flush()
	return snapshot.NewJSONLReader(bytes.NewReader(b.Bytes()))
}

func TestEndToEndLeakAttribution(t *testing.T) {
	a, err := analyzer.New(store.Config{MemoryBudgetBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	var ids []string
	sizes := []int64{5, 12, 22, 34, 48, 64}
	for i, sz := range sizes {
		id := "snap-" + string(rune('0'+i))
		ix, rep, err := a.AnalyzeOne(
			snapshotGen(t, id, int64(i), sz, i == 2 || i == len(sizes)-1),
			index.Options{}, retain.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if rep.TotalShallow <= 0 {
			t.Fatal("retained total should be positive")
		}
		_ = ix
		ids = append(ids, id)
	}

	// diff between first and last must attribute growth to the leak site
	var dRep struct{ entries int }
	err = a.Store().View(func(v *store.Snapshot) error {
		r, err := v.Diff(ids[0], ids[len(ids)-1], struct{}{})
		if err != nil {
			return err
		}
		dRep.entries = len(r.Entries)
		var leakGrowth int64
		for _, e := range r.Entries {
			if e.Site == "s:leak" {
				leakGrowth += e.GrowthBytes
			}
		}
		var sawGap bool
		for _, sp := range r.Special {
			if sp.Kind == "missing_sampling" {
				sawGap = true
			}
		}
		if leakGrowth <= 0 {
			t.Fatalf("growth must land on leak site, got %+v", r.Entries)
		}
		if !sawGap {
			t.Fatal("sampling gap must be reported separately")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = dRep

	verdicts, err := a.DetectLeaks(ids, leak.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	var leakVerdict *leak.Verdict
	for i := range verdicts {
		if verdicts[i].Site == "s:leak" {
			leakVerdict = &verdicts[i]
		}
	}
	if leakVerdict == nil {
		t.Fatalf("no verdict for leak site: %+v", verdicts)
	}
	if leakVerdict.Kind != leak.KindLeakSuspect {
		t.Fatalf("leak site verdict=%s (%s)", leakVerdict.Kind, leakVerdict.Reason)
	}
}
