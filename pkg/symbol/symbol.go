// Package symbol handles symbol tables: merging tables from multiple
// sources and representing missing symbols in a distinguishable way.
//
// Missing symbols are never silently dropped: resolving an unknown PC
// yields a placeholder frame whose Func is "<missing:0x...>", so the
// resulting allocation site stays visible in reports and can be
// traced back to the raw snapshot record.
package symbol

import (
	"fmt"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/model"
)

// MissingPrefix marks synthesized frames for missing symbols.
const MissingPrefix = "<missing:"

// Table maps raw PC-like keys to frames.
type Table struct {
	Frames map[uint64]model.Frame
}

// New creates an empty table.
func New() *Table { return &Table{Frames: map[uint64]model.Frame{}} }

// IsMissing reports whether f is a synthesized missing-symbol frame.
func IsMissing(f model.Frame) bool {
	return len(f.Func) >= len(MissingPrefix) && f.Func[:len(MissingPrefix)] == MissingPrefix
}

// MissingFrame builds the placeholder frame for an unresolved PC.
func MissingFrame(pc uint64) model.Frame {
	return model.Frame{Func: fmt.Sprintf("%s0x%x>", MissingPrefix, pc)}
}

// Merge combines multiple tables. Later tables fill gaps only;
// existing entries are never overwritten, so merging is stable under
// reordering of equally-prioritized sources as long as each source
// is internally consistent. Sources should be passed in decreasing
// order of trust.
func Merge(tables ...*Table) *Table {
	out := New()
	for _, t := range tables {
		if t == nil {
			continue
		}
		for pc, f := range t.Frames {
			if _, ok := out.Frames[pc]; !ok {
				out.Frames[pc] = f
			}
		}
	}
	return out
}

// Resolve maps raw PCs to frames. Unresolvable PCs become
// distinguishable placeholder frames, never silently dropped.
func (t *Table) Resolve(pcs []uint64) []model.Frame {
	out := make([]model.Frame, len(pcs))
	for i, pc := range pcs {
		if f, ok := t.Frames[pc]; ok {
			out[i] = f
		} else {
			out[i] = MissingFrame(pc)
		}
	}
	return out
}
