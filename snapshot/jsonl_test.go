package snapshot_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/snapshot"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/weight"
)

func sampleStream() string {
	var b bytes.Buffer
	w := snapshot.NewJSONLWriter(&b)
	_ = w.WriteMeta(snapshot.Meta{SnapshotID: "s1", TakenAtMs: 100, Format: "jsonl/v1"})
	_ = w.WriteType(snapshot.TypeInfo{ID: 1, Name: "[]byte", Symbol: true})
	_ = w.WriteSite(snapshot.AllocSite{Key: "k1", Symbol: true,
		Stack: []snapshot.Frame{{Func: "alloc", File: "x.go", Line: 7}}})
	_ = w.WriteGap(snapshot.GapRegion{Bytes: 128, Note: "skip"})
	o := snapshot.ObjectRecord{ID: 10, TypeID: 1, SiteKey: "k1", Size: 40, IsRoot: true, RootKind: "stack"}
	ww, _ := weight.New(3, 1)
	o.Sample = &snapshot.Sample{Weight: &ww}
	o.Strong = []snapshot.Edge{{To: 11, Kind: snapshot.EdgeField, Field: "p", Strong: true}}
	o.Unres = []snapshot.RawRef{{Token: "0xabc", Offset: 5, Original: "raw"}}
	_ = w.WriteObject(o)
	_ = w.WriteObject(snapshot.ObjectRecord{ID: 11, TypeID: 1, Size: 24,
		Sample: &snapshot.Sample{}})
	_ = w.Flush()
	return b.String()
}

func TestJSONLRoundtrip(t *testing.T) {
	rd := snapshot.NewJSONLReader(strings.NewReader(sampleStream()))
	b := snapshot.NewBuilder()
	if err := b.Consume(rd); err != nil {
		t.Fatal(err)
	}
	snap := b.Build()
	if snap.Meta.SnapshotID != "s1" || len(snap.Objs) != 2 || len(snap.Gaps) != 1 {
		t.Fatalf("unexpected snapshot: %+v", snap.Meta)
	}
	o0 := snap.Objs[0]
	if o0.Sample == nil || o0.Sample.Weight == nil || !o0.Sample.Weight.IsOne() == false {
		t.Fatal("weight 3/1 should be present")
	}
	if o0.Sample.Weight.String() != "3" {
		t.Fatalf("weight = %s", o0.Sample.Weight)
	}
	if o0.Strong[0].To != 11 || len(o0.Unres) != 1 {
		t.Fatal("edge/unres lost")
	}
	if snap.Objs[1].Sample != nil && snap.Objs[1].Sample.Weight != nil {
		t.Fatal("omitted weight should be nil")
	}
}

func TestJSONLErrorLine(t *testing.T) {
	bad := "{\"t\":\"meta\",\"snapshot_id\":\"x\"}\n{not json}\n"
	rd := snapshot.NewJSONLReader(strings.NewReader(bad))
	var err error
	for {
		_, err = rd.Next()
		if err != nil {
			break
		}
	}
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("expected line-2 error, got %v", err)
	}
}

func TestJSONLEmptyEOF(t *testing.T) {
	rd := snapshot.NewJSONLReader(strings.NewReader("# comment\n\n"))
	if _, err := rd.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF, got %v", err)
	}
}
