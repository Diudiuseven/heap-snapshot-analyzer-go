package symbol

import (
	"testing"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/snapshot"
)

func TestMultiSourceConflictKeepsBoth(t *testing.T) {
	a := New()
	b := New()
	a.RegisterSite("src-A", snapshot.AllocSite{Key: "t1", Symbol: true,
		Stack: []snapshot.Frame{{Func: "alpha", File: "a.go", Line: 1}}})
	b.RegisterSite("src-B", snapshot.AllocSite{Key: "t1", Symbol: true,
		Stack: []snapshot.Frame{{Func: "beta", File: "b.go", Line: 2}}})

	conflicts := a.Merge(b)
	if len(conflicts) != 1 || conflicts[0].Key != "t1" {
		t.Fatalf("expected 1 conflict for t1, got %+v", conflicts)
	}
	s, ok := a.Site("s:t1")
	if !ok {
		t.Fatal("site missing")
	}
	if len(s.Conflicts) != 1 {
		t.Fatalf("conflicting variant must be retained, got %d conflicts", len(s.Conflicts))
	}
	if s.Frames[0].Func != "alpha" {
		t.Fatalf("first-source frames must not be overwritten, got %q", s.Frames[0].Func)
	}
	if s.Conflicts[0].Stack[0].Func != "beta" {
		t.Fatalf("conflicting variant frames not retained, got %+v", s.Conflicts)
	}
	if !s.Sources["src-A"] || !s.Sources["src-B"] {
		t.Fatalf("both sources must be recorded: %v", s.Sources)
	}
}

func TestMissingTypeNotMerged(t *testing.T) {
	tb := New()
	id1 := tb.RegisterType("a", snapshot.TypeInfo{Symbol: false})
	id2 := tb.RegisterType("b", snapshot.TypeInfo{Symbol: false})
	if id1 == id2 {
		t.Fatal("distinct unnamed/missing types must not merge")
	}
	for _, id := range []string{id1, id2} {
		ty, _ := tb.Type(id)
		if !ty.Missing {
			t.Fatal("unnamed type must be flagged missing")
		}
	}
}
