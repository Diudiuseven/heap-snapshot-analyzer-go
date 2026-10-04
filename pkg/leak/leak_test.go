package leak

import (
	"testing"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/aggregate"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/model"
)

func key(site uint32) aggregate.Key { return aggregate.Key{Site: model.SiteID(site)} }

func series(vals ...int64) []Point {
	var out []Point
	for i, v := range vals {
		out = append(out, Point{Timestamp: int64(i + 1), Bytes: v})
	}
	return out
}

func cfg() Config {
	return Config{Window: 5, MinGrowthBytes: 100, MinGrowthRatio: 0.2, MinConsecutiveIncreases: 2}
}

func TestSteadyGrowthFlagged(t *testing.T) {
	s := map[aggregate.Key][]Point{key(1): series(1000, 1200, 1400, 1600, 1800)}
	f := Detect(s, cfg())
	if len(f) != 1 || f[0].Key != key(1) {
		t.Fatalf("got %+v", f)
	}
	if f[0].GrowthBytes != 800 {
		t.Fatalf("growth: %+v", f[0])
	}
}

func TestSingleSpikeNotFlagged(t *testing.T) {
	// One spike then flat: hysteresis requires consecutive increases.
	s := map[aggregate.Key][]Point{key(1): series(1000, 1000, 5000, 5000, 5000)}
	if f := Detect(s, cfg()); len(f) != 0 {
		t.Fatalf("spike flagged: %+v", f)
	}
}

func TestBelowAbsoluteThresholdNotFlagged(t *testing.T) {
	// 50% relative growth but only 40 bytes absolute.
	s := map[aggregate.Key][]Point{key(1): series(80, 90, 100, 110, 120)}
	if f := Detect(s, cfg()); len(f) != 0 {
		t.Fatalf("tiny bucket flagged: %+v", f)
	}
}

func TestBelowRatioThresholdNotFlagged(t *testing.T) {
	// Huge absolute growth but tiny relative (stable large bucket).
	s := map[aggregate.Key][]Point{key(1): series(1_000_000, 1_000_050, 1_000_100, 1_000_150, 1_000_200)}
	if f := Detect(s, cfg()); len(f) != 0 {
		t.Fatalf("stable large bucket flagged: %+v", f)
	}
}

func TestWindowLimitsHistory(t *testing.T) {
	// Growth happened before the window; inside the window it is flat.
	s := map[aggregate.Key][]Point{key(1): series(100, 5000, 5000, 5000, 5000, 5000, 5000)}
	if f := Detect(s, cfg()); len(f) != 0 {
		t.Fatalf("old growth flagged: %+v", f)
	}
}

func TestUnsortedInputAccepted(t *testing.T) {
	pts := []Point{{3, 1400}, {1, 1000}, {4, 1600}, {2, 1200}}
	s := map[aggregate.Key][]Point{key(1): pts}
	if f := Detect(s, cfg()); len(f) != 1 {
		t.Fatalf("unsorted series not handled: %+v", f)
	}
}

func TestDeterministicOrder(t *testing.T) {
	s := map[aggregate.Key][]Point{
		key(3): series(100, 200, 300, 400),
		key(1): series(100, 200, 300, 400),
		key(2): series(100, 200, 300, 400),
	}
	f := Detect(s, cfg())
	if len(f) != 3 || f[0].Key != key(1) || f[1].Key != key(2) || f[2].Key != key(3) {
		t.Fatalf("order: %+v", f)
	}
}
