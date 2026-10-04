// Package aggregate aggregates estimated object sizes by allocation
// site and type.
//
// Determinism: cells accumulate int64 estimates produced by
// pkg/estimate. Integer addition is order-independent, so merging
// partial tables built by any number of concurrent workers yields a
// bit-identical result.
package aggregate

import (
	"sort"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/estimate"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/model"
)

// Key identifies one aggregation bucket.
type Key struct {
	Site model.SiteID
	Type model.TypeID
}

// Cell holds aggregated estimates for one bucket.
type Cell struct {
	Bytes int64
	Count int64
}

// Table is a deterministic aggregation result.
type Table struct {
	Cells map[Key]Cell
}

// New creates an empty table.
func New() *Table { return &Table{Cells: map[Key]Cell{}} }

// Add folds one object into the table.
func (t *Table) Add(o model.Object) {
	b, c := estimate.Object(o)
	k := Key{Site: o.Site, Type: o.Type}
	cell := t.Cells[k]
	cell.Bytes += b
	cell.Count += c
	t.Cells[k] = cell
}

// Merge folds other into t. Merging is commutative and associative.
func (t *Table) Merge(other *Table) {
	for k, c := range other.Cells {
		cell := t.Cells[k]
		cell.Bytes += c.Bytes
		cell.Count += c.Count
		t.Cells[k] = cell
	}
}

// Aggregate builds a table from a whole snapshot.
func Aggregate(s *model.Snapshot) *Table {
	t := New()
	for _, o := range s.Objects {
		t.Add(o)
	}
	return t
}

// Entry pairs a key with its cell for ordered iteration.
type Entry struct {
	Key  Key
	Cell Cell
}

// Sorted returns cells in a canonical (deterministic) order.
func (t *Table) Sorted() []Entry {
	out := make([]Entry, 0, len(t.Cells))
	for k, c := range t.Cells {
		out = append(out, Entry{Key: k, Cell: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key.Site != out[j].Key.Site {
			return out[i].Key.Site < out[j].Key.Site
		}
		return out[i].Key.Type < out[j].Key.Type
	})
	return out
}

// Total returns the total estimated bytes in the table.
func (t *Table) Total() int64 {
	var sum int64
	for _, c := range t.Cells {
		sum += c.Bytes
	}
	return sum
}
