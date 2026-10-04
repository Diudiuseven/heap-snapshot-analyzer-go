// Package leak implements time-series based leak detection with
// configurable thresholds and observation windows.
//
// Semantics (see README for the full discussion):
//   - Only the last Config.Window observations of each bucket are
//     considered; older history cannot trigger a finding.
//   - A bucket is flagged only if BOTH the absolute growth
//     (MinGrowthBytes) and, when non-zero, the relative growth
//     (MinGrowthRatio) over the window are met. Requiring both
//     reduces false positives on tiny buckets (ratio check alone
//     flags noise) and on huge-but-stable buckets (absolute check
//     alone flags normal growth).
//   - MinConsecutiveIncreases adds hysteresis: the series must rise
//     monotonically for at least that many consecutive steps ending
//     at the newest observation, so a single GC-delay spike does not
//     fire an alert (false-positive control). The trade-off is a
//     delayed detection of leaks that pause mid-window (documented
//     false-negative source).
package leak

import (
	"sort"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/aggregate"
)

// Config controls leak detection.
type Config struct {
	// Window is the number of most recent observations considered.
	// <= 0 means "use all observations".
	Window int
	// MinGrowthBytes is the minimum absolute growth over the window.
	MinGrowthBytes int64
	// MinGrowthRatio is the minimum relative growth over the window
	// (e.g. 0.5 = 50% growth). 0 disables the ratio check.
	MinGrowthRatio float64
	// MinConsecutiveIncreases requires at least this many
	// consecutive increases inside the window (hysteresis against
	// noisy single-sample spikes). <= 0 means 1.
	MinConsecutiveIncreases int
}

// Point is one observation of a bucket at a time.
type Point struct {
	Timestamp int64
	Bytes     int64
}

// Finding marks one bucket as leaking.
type Finding struct {
	Key          aggregate.Key
	FirstBytes   int64
	LastBytes    int64
	GrowthBytes  int64
	GrowthRatio  float64
	Observations int
}

// Detect evaluates per-bucket time series and returns leak findings
// in deterministic (key) order.
func Detect(series map[aggregate.Key][]Point, cfg Config) []Finding {
	if cfg.MinConsecutiveIncreases <= 0 {
		cfg.MinConsecutiveIncreases = 1
	}
	var out []Finding
	for k, pts := range series {
		if f, ok := evaluate(k, pts, cfg); ok {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key.Site != out[j].Key.Site {
			return out[i].Key.Site < out[j].Key.Site
		}
		return out[i].Key.Type < out[j].Key.Type
	})
	return out
}

func evaluate(k aggregate.Key, pts []Point, cfg Config) (Finding, bool) {
	if len(pts) == 0 {
		return Finding{}, false
	}
	// Canonical order: sort by timestamp so callers may append in
	// any order.
	sorted := append([]Point(nil), pts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Timestamp < sorted[j].Timestamp })
	if cfg.Window > 0 && len(sorted) > cfg.Window {
		sorted = sorted[len(sorted)-cfg.Window:]
	}
	if len(sorted) < 2 {
		return Finding{}, false
	}
	first, last := sorted[0].Bytes, sorted[len(sorted)-1].Bytes
	growth := last - first
	if growth < cfg.MinGrowthBytes {
		return Finding{}, false
	}
	var ratio float64
	if first > 0 {
		ratio = float64(growth) / float64(first)
	} else if growth > 0 {
		ratio = float64(growth) // from zero: treat bytes as the ratio signal
	}
	if cfg.MinGrowthRatio > 0 && ratio < cfg.MinGrowthRatio {
		return Finding{}, false
	}
	// Hysteresis: count consecutive increases ending at the newest
	// observation.
	consec := 0
	for i := len(sorted) - 1; i > 0; i-- {
		if sorted[i].Bytes > sorted[i-1].Bytes {
			consec++
		} else {
			break
		}
	}
	if consec < cfg.MinConsecutiveIncreases {
		return Finding{}, false
	}
	return Finding{
		Key:          k,
		FirstBytes:   first,
		LastBytes:    last,
		GrowthBytes:  growth,
		GrowthRatio:  ratio,
		Observations: len(sorted),
	}, true
}
