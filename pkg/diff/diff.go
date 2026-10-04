// Package diff compares two or more snapshots and attributes growth
// to concrete allocation sites.
//
// Attribution rules:
//   - Buckets present only in the newer snapshot are New.
//   - Buckets present only in the older snapshot are Collected.
//   - Buckets present in both are Growing / Shrinking / Stable.
//   - Bytes whose allocation site is unknown (missing symbols or
//     sampling gaps) are accumulated into UnattributedOld /
//     UnattributedNew and reported separately; they are never folded
//     into any attributed entry.
package diff

import (
	"sort"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/aggregate"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/model"
)

// Kind classifies a diff entry.
type Kind int

const (
	New       Kind = iota // present only in the newer snapshot
	Collected             // present only in the older snapshot
	Growing               // present in both, increased
	Shrinking             // present in both, decreased
	Stable                // present in both, unchanged
)

func (k Kind) String() string {
	switch k {
	case New:
		return "new"
	case Collected:
		return "collected"
	case Growing:
		return "growing"
	case Shrinking:
		return "shrinking"
	default:
		return "stable"
	}
}

// Entry is one attributed diff row.
type Entry struct {
	Key      aggregate.Key
	Kind     Kind
	OldBytes int64
	NewBytes int64
	Delta    int64
}

// Report is the result of comparing two snapshots.
type Report struct {
	Entries []Entry
	// UnattributedOld/UnattributedNew hold bytes that could not be
	// attributed to any concrete site (missing site or sampling
	// gaps). They are reported separately and never folded into
	// other entries.
	UnattributedOld int64
	UnattributedNew int64
}

// Compare diffs two snapshots by their aggregate tables.
func Compare(oldS, newS *model.Snapshot) *Report {
	return CompareTables(aggregate.Aggregate(oldS), aggregate.Aggregate(newS))
}

// CompareTables diffs two pre-aggregated tables.
func CompareTables(oldT, newT *aggregate.Table) *Report {
	rep := &Report{}
	keys := map[aggregate.Key]struct{}{}
	for k := range oldT.Cells {
		keys[k] = struct{}{}
	}
	for k := range newT.Cells {
		keys[k] = struct{}{}
	}
	sorted := make([]aggregate.Key, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Site != sorted[j].Site {
			return sorted[i].Site < sorted[j].Site
		}
		return sorted[i].Type < sorted[j].Type
	})
	for _, k := range sorted {
		oc, inOld := oldT.Cells[k]
		nc, inNew := newT.Cells[k]
		// Unattributed bytes are split out, never merged into
		// attributed entries.
		if k.Site == model.UnknownSite {
			rep.UnattributedOld += oc.Bytes
			rep.UnattributedNew += nc.Bytes
			continue
		}
		e := Entry{Key: k, OldBytes: oc.Bytes, NewBytes: nc.Bytes, Delta: nc.Bytes - oc.Bytes}
		switch {
		case !inOld:
			e.Kind = New
		case !inNew:
			e.Kind = Collected
		case e.Delta > 0:
			e.Kind = Growing
		case e.Delta < 0:
			e.Kind = Shrinking
		default:
			e.Kind = Stable
		}
		rep.Entries = append(rep.Entries, e)
	}
	return rep
}
