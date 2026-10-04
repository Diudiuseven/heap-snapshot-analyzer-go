package leak_test

import (
	"testing"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/leak"
)

func pts(bytes ...int64) []leak.Point {
	out := make([]leak.Point, len(bytes))
	for i, b := range bytes {
		out[i] = leak.Point{SnapshotID: "s" + string(rune('0'+i)), TakenAtMs: int64(i), Bytes: b}
	}
	return out
}

func opts() leak.Options {
	o := leak.DefaultOptions()
	o.WindowSize = 4
	o.MinSnapshots = 4
	o.MinRisingRatio = 0.75
	o.MinGrowthBytes = 10
	o.MinGrowthRatio = 0.05
	o.MinConsecutiveWindows = 2
	o.DipReclaimRatio = 0.5
	return o
}

func TestLeakSuspect(t *testing.T) {
	s := leak.Series{Site: "s1", Points: pts(100, 120, 150, 190, 240, 300)}
	v := leak.Evaluate(s, opts())
	if v.Kind != leak.KindLeakSuspect {
		t.Fatalf("want leak_suspect, got %s: %s", v.Kind, v.Reason)
	}
}

func TestSteadyMicroGrowthFiltered(t *testing.T) {
	// tiny wobbles below absolute/relative thresholds: must not alert
	s := leak.Series{Site: "s2", Points: pts(100000, 100001, 100000, 100002, 100001, 100003)}
	v := leak.Evaluate(s, opts())
	if v.Kind == leak.KindLeakSuspect || v.Kind == leak.KindGrowing {
		t.Fatalf("micro-growth must be filtered, got %s (%s)", v.Kind, v.Reason)
	}
	if v.Kind != leak.KindSteady {
		t.Fatalf("want steady, got %s", v.Kind)
	}
}

func TestDipResets(t *testing.T) {
	// grows, then a >50% reclamation dip -> not a leak even if it regrows
	s := leak.Series{Site: "s3", Points: pts(100, 200, 400, 800, 200, 300, 500, 900)}
	v := leak.Evaluate(s, opts())
	if v.Kind == leak.KindLeakSuspect {
		t.Fatalf("dip must prevent leak verdict, got %s (%s)", v.Kind, v.Reason)
	}
	foundDip := false
	for _, w := range v.Windows {
		if w.HasDip {
			foundDip = true
		}
	}
	if !foundDip {
		t.Fatal("expected at least one window to record a dip")
	}
}

func TestGrowingNotSustained(t *testing.T) {
	// one passing window possible, but not two consecutive (trailing decline)
	s := leak.Series{Site: "s4", Points: pts(100, 130, 170, 220, 150, 140)}
	v := leak.Evaluate(s, opts())
	if v.Kind != leak.KindGrowing && v.Kind != leak.KindSteady {
		t.Fatalf("unexpected kind %s", v.Kind)
	}
}

func TestUnknownOnShortOrMissing(t *testing.T) {
	s := leak.Series{Site: "s5", Points: pts(100, 200, 300)}
	if v := leak.Evaluate(s, opts()); v.Kind != leak.KindUnknown {
		t.Fatalf("short series must be unknown, got %s", v.Kind)
	}
	p := pts(100, 200, 300, 400)
	p[2].Missing = true
	s2 := leak.Series{Site: "s5", Points: p}
	if v := leak.Evaluate(s2, opts()); v.Kind != leak.KindUnknown {
		t.Fatalf("missing-sampling point must force unknown, got %s (%s)", v.Kind, v.Reason)
	}
}

func TestVerdictExplainability(t *testing.T) {
	s := leak.Series{Site: "s6", Points: pts(100, 100, 100, 100, 100, 100)}
	v := leak.Evaluate(s, opts())
	if v.Kind != leak.KindSteady || v.Reason == "" || len(v.Windows) == 0 {
		t.Fatalf("steady verdict must carry reasons and windows: %+v", v)
	}
	for _, w := range v.Windows {
		if w.Passes || len(w.Reasons) == 0 {
			t.Fatalf("failed window must explain itself: %+v", w)
		}
	}
}
