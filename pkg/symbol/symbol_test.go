package symbol

import (
	"testing"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/model"
)

func TestResolveMissing(t *testing.T) {
	tb := New()
	tb.Frames[0x1000] = model.Frame{Func: "main.known", File: "a.go", Line: 1}
	frames := tb.Resolve([]uint64{0x1000, 0xdead})
	if frames[0].Func != "main.known" {
		t.Fatalf("%+v", frames[0])
	}
	if !IsMissing(frames[1]) {
		t.Fatalf("missing symbol not distinguishable: %+v", frames[1])
	}
	if frames[1].Func != "<missing:0xdead>" {
		t.Fatalf("placeholder lost the raw PC: %+v", frames[1])
	}
}

func TestMergeFillsGapsOnly(t *testing.T) {
	a := New()
	a.Frames[1] = model.Frame{Func: "fromA"}
	b := New()
	b.Frames[1] = model.Frame{Func: "fromB"}
	b.Frames[2] = model.Frame{Func: "onlyB"}
	m := Merge(a, b)
	if m.Frames[1].Func != "fromA" {
		t.Fatalf("existing entry overwritten: %+v", m.Frames[1])
	}
	if m.Frames[2].Func != "onlyB" {
		t.Fatalf("gap not filled: %+v", m.Frames[2])
	}
	// Missing in both stays missing, distinguishably.
	frames := m.Resolve([]uint64{9})
	if !IsMissing(frames[0]) {
		t.Fatalf("%+v", frames[0])
	}
}
