// Package diff attributes changes between two snapshots.
//
// Objects are matched by producer object ID. Per (allocation site, type)
// cell it separates observed categories and estimates totals:
//
//   - Added:    IDs present only in the newer snapshot;
//   - Removed:  IDs present only in the older snapshot (reclaimed);
//   - Retained: IDs present in both, with a shallow-size delta (a positive
//     aggregated delta is "sustained growth" for that cell);
//
// Estimated bytes/counts use the exact sampling weights of each side and are
// rounded once, so numbers are order independent.
//
// Two extra categories are ALWAYS emitted separately and never counted inside
// a normal cell:
//
//   - missing_sampling: bytes the producer explicitly could not attribute
//     (gap records and object-declared gap bytes);
//   - unresolved: references the producer could not resolve into an object;
//   - unattributed: objects present but lacking a resolvable allocation site.
package diff

import (
	"io"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/index"
)

// ChangeKind enumerates per-cell change categories.
type ChangeKind string

const (
	ChangeAdded    ChangeKind = "added"
	ChangeRemoved  ChangeKind = "removed"
	ChangeRetained ChangeKind = "retained"
)

// Entry is one attributed change cell.
type Entry struct {
	Site           string
	Type           string
	Kind           ChangeKind
	AddedCount     int64 // estimated new-object count
	AddedBytes     int64 // estimated shallow bytes of new objects
	RemovedCount   int64 // estimated reclaimed-object count
	RemovedBytes   int64 // estimated shallow bytes of removed objects
	GrowthBytes    int64 // positive estimated net change (added+delta-removed)
	NetBytes       int64 // signed estimated net change
	RetainedBefore int64 // estimated bytes of surviving objects before
	RetainedAfter  int64 // estimated bytes of surviving objects after
	ObsAdded       int64
	ObsRemoved     int64
	Sampled        bool
}

// Special carries categories that must never share a key with a real cell.
type Special struct {
	Kind        string // "missing_sampling" | "unattributed" | "unresolved"
	BytesBefore int64
	BytesAfter  int64
	NetBytes    int64
	Count       int64
	Note        string
}

// Report is the full comparison result.
type Report struct {
	BeforeID string
	AfterID  string
	Entries  []Entry
	Special  []Special
}

// Options controls comparison.
type Options struct{}

// Compare computes the attributed diff between two built indexes by merge-
// joining objects by producer ID. Both sides are read as ordered streams, so
// memory usage is independent of snapshot size even in bounded (spilled) mode.
func Compare(before, after *index.Index, opts Options) (*Report, error) {
	rep := &Report{BeforeID: before.SnapshotID(), AfterID: after.SnapshotID()}
	acc := newDeltaAccumulator()
	obsAdd := map[index.CellKey]int64{}
	obsRm := map[index.CellKey]int64{}
	sampledSet := map[index.CellKey]bool{}

	ib, err := before.IterObjectsByID()
	if err != nil {
		return nil, err
	}
	defer ib.Close()
	ia, err := after.IterObjectsByID()
	if err != nil {
		return nil, err
	}
	defer ia.Close()

	b, errB := ib.Next()
	a, errA := ia.Next()
	for {
		switch {
		case errB == io.EOF && errA == io.EOF:
			goto done
		case errB != nil && errB != io.EOF:
			return nil, errB
		case errA != nil && errA != io.EOF:
			return nil, errA
		}
		if errB == io.EOF {
			// remaining after-side objects are all added
			for errA == nil {
				k := index.CellKey{Site: a.Site, Type: a.Type}
				acc.added(k, a.Weight, a.Size)
				obsAdd[k]++
				if !a.Weight.IsOne() {
					sampledSet[k] = true
				}
				a, errA = ia.Next()
			}
			continue
		}
		if errA == io.EOF {
			for errB == nil {
				k := index.CellKey{Site: b.Site, Type: b.Type}
				acc.removed(k, b.Weight, b.Size)
				obsRm[k]++
				if !b.Weight.IsOne() {
					sampledSet[k] = true
				}
				b, errB = ib.Next()
			}
			continue
		}
		bk := index.CellKey{Site: b.Site, Type: b.Type}
		ak := index.CellKey{Site: a.Site, Type: a.Type}
		switch {
		case b.ID < a.ID:
			acc.removed(bk, b.Weight, b.Size)
			obsRm[bk]++
			if !b.Weight.IsOne() {
				sampledSet[bk] = true
			}
			b, errB = ib.Next()
		case b.ID > a.ID:
			acc.added(ak, a.Weight, a.Size)
			obsAdd[ak]++
			if !a.Weight.IsOne() {
				sampledSet[ak] = true
			}
			a, errA = ia.Next()
		default: // same id
			if bk == ak {
				acc.retained(bk, b.Weight, b.Size, a.Weight, a.Size)
				if !b.Weight.IsOne() || !a.Weight.IsOne() {
					sampledSet[bk] = true
				}
			} else {
				// object moved between cells: count as removed+added
				acc.removed(bk, b.Weight, b.Size)
				acc.added(ak, a.Weight, a.Size)
				obsRm[bk]++
				obsAdd[ak]++
				if !b.Weight.IsOne() {
					sampledSet[bk] = true
				}
				if !a.Weight.IsOne() {
					sampledSet[ak] = true
				}
			}
			b, errB = ib.Next()
			a, errA = ia.Next()
		}
	}
done:
	keys := acc.allKeys()
	for _, k := range keys {
		sums := acc.finish(k)
		d := deltasKind(obsAdd[k], obsRm[k], sums)
		net := sums.addedBytes + sums.retAfter - sums.removedBytes - sums.retBefore
		e := Entry{
			Site: k.Site, Type: k.Type, Kind: d,
			AddedCount: sums.addedCount, AddedBytes: sums.addedBytes,
			RemovedCount: sums.removedCount, RemovedBytes: sums.removedBytes,
			RetainedBefore: sums.retBefore, RetainedAfter: sums.retAfter,
			NetBytes: net, GrowthBytes: net, ObsAdded: obsAdd[k],
			ObsRemoved: obsRm[k], Sampled: sampledSet[k],
		}
		if net < 0 {
			e.GrowthBytes = 0
		}
		rep.Entries = append(rep.Entries, e)
	}

	// Specials.
	gapB := gapBytes(before)
	gapA := gapBytes(after)
	if gapB != 0 || gapA != 0 {
		rep.Special = append(rep.Special, Special{
			Kind: "missing_sampling", BytesBefore: gapB, BytesAfter: gapA,
			NetBytes: gapA - gapB,
		})
	}
	unB, unA := unresolvedCounts(before), unresolvedCounts(after)
	if unB != 0 || unA != 0 {
		rep.Special = append(rep.Special, Special{
			Kind: "unresolved", Count: unA, BytesBefore: unB, BytesAfter: unA,
			NetBytes: unA - unB,
		})
	}
	ub, ua := unattributedBytes(before), unattributedBytes(after)
	if ub != 0 || ua != 0 {
		rep.Special = append(rep.Special, Special{
			Kind: "unattributed", BytesBefore: ub, BytesAfter: ua,
			NetBytes: ua - ub,
		})
	}
	return rep, nil
}

func gapBytes(ix *index.Index) int64 {
	var b int64
	for _, g := range ix.Gaps() {
		b += g.Bytes
	}
	return b
}

func unresolvedCounts(ix *index.Index) int64 {
	var c int64
	for _, u := range ix.Unresolved() {
		c += u.Count
	}
	return c
}

// unattributedBytes sums shallow bytes of cells whose site is flagged missing
// AND whose canonical id denotes an unidentifiable site ("missing:" prefix).
// Known-but-unsymbolized sites (key present) still attribute to a stable site.
func unattributedBytes(ix *index.Index) int64 {
	var b int64
	for _, c := range ix.Cells() {
		s, ok := ix.Symbols().Site(c.Key.Site)
		if ok && s.Missing && isUnidentifiable(s.Canonical) {
			b += c.BytesEst
		}
	}
	return b
}

func isUnidentifiable(canonical string) bool {
	return len(canonical) >= 8 && canonical[:8] == "missing:"
}
