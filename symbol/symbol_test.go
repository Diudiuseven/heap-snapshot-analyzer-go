package symbol

import (
	"testing"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/snapshot"
)

func TestSiteKeyStability(t *testing.T) {
	s1 := snapshot.AllocSite{Stack: []snapshot.Frame{{Func: "f", File: "a.go", Line: 3}}}
	s2 := snapshot.AllocSite{Stack: []snapshot.Frame{{Func: "f", File: "a.go", Line: 3}}}
	s3 := snapshot.AllocSite{Stack: []snapshot.Frame{{Func: "f", File: "a.go", Line: 4}}}
	if StableSiteKey(s1) != StableSiteKey(s2) {
		t.Fatal("identical frames must fingerprint equal")
	}
	if StableSiteKey(s1) == StableSiteKey(s3) {
		t.Fatal("different frames must fingerprint differ")
	}
}

func TestMissingSitesNotMerged(t *testing.T) {
	tb := New()
	m1 := snapshot.AllocSite{Symbol: false} // no key, no frames
	m2 := snapshot.AllocSite{Symbol: false}
	id1 := tb.RegisterSite("snap-a", m1)
	id2 := tb.RegisterSite("snap-b", m2)
	if id1 == id2 {
		t.Fatalf("unidentifiable missing sites must not be merged: %s", id1)
	}
	ms := tb.MissingSites()
	if len(ms) != 2 {
		t.Fatalf("want 2 missing sites, got %v", ms)
	}
}

func TestMissingWithKeyStable(t *testing.T) {
	tb := New()
	id1 := tb.RegisterSite("a", snapshot.AllocSite{Key: "trace-9", Symbol: false})
	id2 := tb.RegisterSite("b", snapshot.AllocSite{Key: "trace-9", Symbol: false})
	if id1 != id2 {
		t.Fatal("same producer key must stay the same canonical site")
	}
	s, _ := tb.Site(id1)
	if !s.Missing {
		t.Fatal("site must remain flagged missing until symbols arrive")
	}
	// later source provides real symbols: identity is enriched, not replaced
	id3 := tb.RegisterSite("c", snapshot.AllocSite{
		Key: "trace-9", Symbol: true,
		Stack: []snapshot.Frame{{Func: "alloc"}},
	})
	if id3 != id1 {
		t.Fatal("resolved site must keep canonical identity")
	}
	s, _ = tb.Site(id1)
	if s.Missing || len(s.Frames) != 1 {
		t.Fatalf("site should be resolved, got %+v", s)
	}
}

func TestConflictRecorded(t *testing.T) {
	tb := New()
	tb.RegisterSite("a", snapshot.AllocSite{Key: "k", Symbol: true,
		Stack: []snapshot.Frame{{Func: "f1"}}})
	tb.RegisterSite("b", snapshot.AllocSite{Key: "k", Symbol: true,
		Stack: []snapshot.Frame{{Func: "f2"}}})
	s, _ := tb.Site("s:k")
	if len(s.Conflicts) != 1 {
		t.Fatalf("want 1 conflict, got %d", len(s.Conflicts))
	}
}

func TestMergeTables(t *testing.T) {
	a := New()
	b := New()
	a.RegisterSite("a", snapshot.AllocSite{Key: "k1", Symbol: true,
		Stack: []snapshot.Frame{{Func: "f"}}})
	b.RegisterSite("b", snapshot.AllocSite{Key: "k1", Symbol: true,
		Stack: []snapshot.Frame{{Func: "f"}}})
	b.RegisterType("b", snapshot.TypeInfo{Name: "[]byte", Symbol: true})
	conflicts := a.Merge(b)
	if len(conflicts) != 0 {
		t.Fatalf("identical site should not conflict: %v", conflicts)
	}
	if _, ok := a.Type("t:[]byte"); !ok {
		t.Fatal("type missing after merge")
	}
	s, _ := a.Site("s:k1")
	if !s.Sources["a"] || !s.Sources["b"] {
		t.Fatalf("sources not merged: %v", s.Sources)
	}
}
