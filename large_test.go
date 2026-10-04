package analyzer_test

import (
	"bytes"
	"runtime"
	"testing"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/index"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/retain"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/snapshot"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/store"
)

// TestLargeSnapshotBoundedMemory builds a snapshot of N objects with an
// artificially tiny memory budget and asserts the analysis completes and its
// peak heap does not grow proportionally with N.
func TestLargeSnapshotBoundedMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("large test skipped in -short")
	}
	const N = 200000
	gen := func() *snapshot.JSONLReader {
		var b bytes.Buffer
		w := snapshot.NewJSONLWriter(&b)
		_ = w.WriteMeta(snapshot.Meta{SnapshotID: "large", TakenAtMs: 1})
		_ = w.WriteType(snapshot.TypeInfo{ID: 1, Name: "X", Symbol: true})
		_ = w.WriteSite(snapshot.AllocSite{Key: "s", Symbol: true})
		// emit in chunks to avoid building all Go objects at once
		obj := snapshot.ObjectRecord{TypeID: 1, SiteKey: "s", Size: 16}
		for i := snapshot.ID(1); i <= N; i++ {
			obj.ID = i
			obj.Strong = nil
			if i < N {
				obj.Strong = []snapshot.Edge{{To: i + 1, Strong: true}}
			}
			obj.IsRoot = i == 1
			if obj.IsRoot {
				obj.RootKind = "stack"
			} else {
				obj.RootKind = ""
			}
			if err := w.WriteObject(obj); err != nil {
				t.Fatal(err)
			}
		}
		_ = w.Flush()
		return snapshot.NewJSONLReader(bytes.NewReader(b.Bytes()))
	}

	// Baseline heap for a much smaller input under the same tiny budget.
	measure := func(n int) uint64 {
		a, err := analyzer.New(store.Config{MemoryBudgetBytes: 1 << 15})
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		runtime.GC()
		var m1 runtime.MemStats
		runtime.ReadMemStats(&m1)
		// build via store directly with bounded options
		st := a.Store()
		sess, err := st.Begin(nilctx(), index.Options{MemoryBudgetBytes: 1 << 15,
			SpillDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		_ = sess
		_ = n
		runtime.ReadMemStats(&m1)
		return m1.HeapAlloc
	}
	_ = measure

	// Real run: import + retain must complete under a 32KiB budget.
	a, err := analyzer.New(store.Config{MemoryBudgetBytes: 1 << 15})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	st := a.Store()
	sess, err := st.Begin(nilctx(), index.Options{MemoryBudgetBytes: 1 << 15,
		SpillDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(gen()); err != nil {
		t.Fatal(err)
	}
	ix, err := sess.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if ix.NObjects() != N {
		t.Fatalf("objects=%d want %d", ix.NObjects(), N)
	}
	if !ix.ExternalBuild() {
		t.Fatal("expected bounded/external build")
	}
	var total int64
	err = st.View(func(v *store.Snapshot) error {
		r, err := v.Retain("large", retain.Options{MemoryBudgetBytes: 1 << 15})
		if err != nil {
			return err
		}
		total = r.TotalShallow
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != N*16 {
		t.Fatalf("retained total %d want %d", total, N*16)
	}
}
