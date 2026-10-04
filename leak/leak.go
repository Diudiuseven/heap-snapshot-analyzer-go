// Package leak decides whether an allocation site is leaking from a time
// series of snapshots.
//
// Policy (every threshold configurable; see Options). For one site, a WINDOW
// of WindowSize consecutive snapshots is evaluated:
//
//   - risingRatio = rising adjacent pairs / total pairs;
//   - growth = bytes(last)-bytes(first); growthRatio = growth/bytes(first);
//   - a window PASSES when risingRatio >= MinRisingRatio, growth >=
//     MinGrowthBytes, growthRatio >= MinGrowthRatio, and it contains no
//     "reclamation dip" (any pair whose drop is >= DipReclaimRatio of the
//     higher value). A dip means memory was substantially returned, which
//     contradicts a leak; the window fails with an explicit reason.
//
// A SERIES is judged across sliding consecutive windows:
//
//   - fewer than MinSnapshots points, or any point flagged Missing that would
//     be needed for the decision, => unknown (never a clean "not leaking");
//   - >= MinConsecutiveWindows consecutive passing windows => leak_suspect;
//   - at least one passing window but not enough consecutive => growing;
//   - otherwise => steady.
//
// False-positive control: absolute/relative floors and the dip reset prevent
// noisy micro-growth and normal cache churn from being promoted. False-
// negative control: insufficient or gap-tainted evidence yields "unknown"
// rather than "steady", and every Verdict carries per-window Reasons so the
// exact evidence (and why it failed) is inspectable.
package leak

import (
	"sort"
	"strings"
)

// Kind is a leak verdict.
type Kind string

const (
	KindUnknown     Kind = "unknown"
	KindSteady      Kind = "steady"
	KindGrowing     Kind = "growing"
	KindLeakSuspect Kind = "leak_suspect"
)

// Options configures the detector.
type Options struct {
	WindowSize            int
	MinSnapshots          int
	MinRisingRatio        float64
	MinGrowthBytes        int64
	MinGrowthRatio        float64
	MinConsecutiveWindows int
	DipReclaimRatio       float64
}

// DefaultOptions returns documented conservative defaults.
func DefaultOptions() Options {
	return Options{
		WindowSize:            4,
		MinSnapshots:          4,
		MinRisingRatio:        0.75,
		MinGrowthBytes:        1 << 10, // 1 KiB
		MinGrowthRatio:        0.10,
		MinConsecutiveWindows: 2,
		DipReclaimRatio:       0.50,
	}
}

// Point is one time-series observation of a site.
type Point struct {
	SnapshotID string
	TakenAtMs  int64
	Bytes      int64
	Count      int64
	Missing    bool
}

// Series is one site's ordered points.
type Series struct {
	Site   string
	Points []Point
}

// WindowEval records the evidence of one window.
type WindowEval struct {
	StartIndex  int
	StartID     string
	EndID       string
	RisingPairs int
	TotalPairs  int
	GrowthBytes int64
	GrowthRatio float64
	HasDip      bool
	Passes      bool
	Reasons     []string
}

// Verdict explains a decision in full.
type Verdict struct {
	Site    string
	Kind    Kind
	Windows []WindowEval
	Reason  string
	Streak  int
}

func (o Options) withDefaults() Options {
	d := DefaultOptions()
	if o.WindowSize <= 0 {
		o.WindowSize = d.WindowSize
	}
	if o.MinSnapshots <= 0 {
		o.MinSnapshots = d.MinSnapshots
	}
	if o.MinSnapshots < o.WindowSize {
		o.MinSnapshots = o.WindowSize
	}
	if o.MinRisingRatio <= 0 {
		o.MinRisingRatio = d.MinRisingRatio
	}
	if o.MinGrowthBytes <= 0 {
		o.MinGrowthBytes = d.MinGrowthBytes
	}
	if o.MinGrowthRatio <= 0 {
		o.MinGrowthRatio = d.MinGrowthRatio
	}
	if o.MinConsecutiveWindows <= 0 {
		o.MinConsecutiveWindows = d.MinConsecutiveWindows
	}
	if o.DipReclaimRatio <= 0 {
		o.DipReclaimRatio = d.DipReclaimRatio
	}
	return o
}

func evalWindow(s Series, start, size int, o Options) WindowEval {
	w := WindowEval{StartIndex: start, StartID: s.Points[start].SnapshotID,
		EndID: s.Points[start+size-1].SnapshotID, TotalPairs: size - 1}
	for i := start; i < start+size-1; i++ {
		cur, nxt := s.Points[i], s.Points[i+1]
		if nxt.Bytes > cur.Bytes {
			w.RisingPairs++
		}
		hi := cur.Bytes
		if nxt.Bytes > hi {
			hi = nxt.Bytes
		}
		if hi > 0 && nxt.Bytes < cur.Bytes {
			drop := cur.Bytes - nxt.Bytes
			if float64(drop)/float64(hi) >= o.DipReclaimRatio {
				w.HasDip = true
			}
		}
	}
	first := s.Points[start].Bytes
	last := s.Points[start+size-1].Bytes
	w.GrowthBytes = last - first
	if first > 0 {
		w.GrowthRatio = float64(w.GrowthBytes) / float64(first)
	}
	ratio := 0.0
	if w.TotalPairs > 0 {
		ratio = float64(w.RisingPairs) / float64(w.TotalPairs)
	}
	fails := []string{}
	if ratio < o.MinRisingRatio {
		fails = append(fails, "rising ratio below threshold")
	}
	if w.GrowthBytes < o.MinGrowthBytes {
		fails = append(fails, "absolute growth below threshold")
	}
	if w.GrowthRatio < o.MinGrowthRatio {
		fails = append(fails, "relative growth below threshold")
	}
	if w.HasDip {
		fails = append(fails, "reclamation dip observed")
	}
	w.Passes = len(fails) == 0
	if w.Passes {
		w.Reasons = []string{"all growth criteria met"}
	} else {
		w.Reasons = fails
	}
	return w
}

// Evaluate judges a single site series.
func Evaluate(s Series, opts Options) Verdict {
	o := opts.withDefaults()
	v := Verdict{Site: s.Site, Kind: KindUnknown}
	pts := s.Points
	sort.SliceStable(pts, func(i, j int) bool { return pts[i].TakenAtMs < pts[j].TakenAtMs })
	s.Points = pts

	if len(pts) < o.MinSnapshots {
		v.Reason = "insufficient observations"
		return v
	}
	for _, p := range pts {
		if p.Missing {
			v.Reason = "series contains missing-sampling observations"
			return v
		}
	}

	// sliding windows of WindowSize across all points
	var windows []WindowEval
	for start := 0; start+o.WindowSize <= len(pts); start++ {
		windows = append(windows, evalWindow(s, start, o.WindowSize, o))
	}
	v.Windows = windows

	bestStreak, curStreak := 0, 0
	anyPass := false
	for _, w := range windows {
		if w.Passes {
			anyPass = true
			curStreak++
			if curStreak > bestStreak {
				bestStreak = curStreak
			}
		} else {
			curStreak = 0
		}
	}
	v.Streak = bestStreak
	switch {
	case bestStreak >= o.MinConsecutiveWindows:
		v.Kind = KindLeakSuspect
		v.Reason = "growth sustained across required consecutive windows"
	case anyPass:
		v.Kind = KindGrowing
		v.Reason = "growth observed in at least one window but not sustained"
	default:
		v.Kind = KindSteady
		var rs []string
		for i, w := range windows {
			if !w.Passes {
				rs = append(rs, "window"+itoa(i)+": "+strings.Join(w.Reasons, "; "))
			}
		}
		v.Reason = "no window passed (" + strings.Join(rs, " | ") + ")"
	}
	return v
}

// EvaluateAll judges every series and groups verdicts by kind.
func EvaluateAll(series []Series, opts Options) (suspects, growing, steady, unknown []Verdict) {
	for _, s := range series {
		v := Evaluate(s, opts)
		switch v.Kind {
		case KindLeakSuspect:
			suspects = append(suspects, v)
		case KindGrowing:
			growing = append(growing, v)
		case KindSteady:
			steady = append(steady, v)
		default:
			unknown = append(unknown, v)
		}
	}
	sort.Slice(suspects, func(i, j int) bool { return suspects[i].Site < suspects[j].Site })
	return
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}
