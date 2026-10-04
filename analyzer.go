// Package analyzer is the in-process facade for the heap snapshot analyzer.
//
// The whole pipeline runs locally: parse -> aggregate/index -> retained size
// -> snapshot diff -> leak verdict. It ingests either a local snapshot file
// or an in-process snapshot stream and performs no network I/O, so results
// are fully reproducible on the same input.
package analyzer

import (
	"context"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/diff"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/index"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/leak"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/retain"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/snapshot"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/store"
)

// Analyzer bundles a store with the analysis entry points.
type Analyzer struct{ st *store.Store }

// New constructs an Analyzer.
func New(cfg store.Config) (*Analyzer, error) {
	st, err := store.New(cfg)
	if err != nil {
		return nil, err
	}
	return &Analyzer{st: st}, nil
}

// ImportReader atomically imports one streaming snapshot and returns its
// committed index. A failed import is rolled back and never published.
func (a *Analyzer) ImportReader(rd snapshot.Reader, opts index.Options) (*index.Index, error) {
	sess, err := a.st.Begin(context.Background(), opts)
	if err != nil {
		return nil, err
	}
	if err := sess.Append(rd); err != nil {
		_ = sess.Abort()
		return nil, err
	}
	return sess.Commit()
}

// AnalyzeOne imports an input and returns its index plus retained-size report.
func (a *Analyzer) AnalyzeOne(rd snapshot.Reader, iopts index.Options, ropts retain.Options) (*index.Index, *retain.Report, error) {
	ix, err := a.ImportReader(rd, iopts)
	if err != nil {
		return nil, nil, err
	}
	var rep *retain.Report
	err = a.st.View(func(v *store.Snapshot) error {
		var e error
		rep, e = v.Retain(ix.SnapshotID(), ropts)
		return e
	})
	if err != nil {
		return ix, nil, err
	}
	return ix, rep, nil
}

// Compare imports two inputs and attributes their differences.
func (a *Analyzer) Compare(before, after snapshot.Reader, dopts diff.Options) (*diff.Report, error) {
	b, err := a.ImportReader(before, index.Options{})
	if err != nil {
		return nil, err
	}
	af, err := a.ImportReader(after, index.Options{})
	if err != nil {
		return nil, err
	}
	var rep *diff.Report
	err = a.st.View(func(v *store.Snapshot) error {
		var e error
		rep, e = v.Diff(b.SnapshotID(), af.SnapshotID(), dopts)
		return e
	})
	return rep, err
}

// DetectLeaks evaluates per-site time series across committed snapshots.
func (a *Analyzer) DetectLeaks(ids []string, opts leak.Options) ([]leak.Verdict, error) {
	var series []leak.Series
	err := a.st.View(func(v *store.Snapshot) error {
		var e error
		series, e = v.LeakSeries(ids)
		return e
	})
	if err != nil {
		return nil, err
	}
	suspects, growing, steady, unknown := leak.EvaluateAll(series, opts)
	out := make([]leak.Verdict, 0, len(suspects)+len(growing)+len(steady)+len(unknown))
	out = append(out, suspects...)
	out = append(out, growing...)
	out = append(out, steady...)
	out = append(out, unknown...)
	return out, nil
}

// Store exposes the underlying store for advanced use.
func (a *Analyzer) Store() *store.Store { return a.st }

// Close releases resources.
func (a *Analyzer) Close() error { return a.st.Close() }
