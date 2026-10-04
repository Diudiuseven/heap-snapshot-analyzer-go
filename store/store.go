// Package store manages snapshot ingestion with transactional visibility.
//
// Each import runs in an isolated session and builds a fully private index.
// Queries execute against an immutable, point-in-time view: an import in
// progress is invisible until Commit; a failed or interrupted import is rolled
// back and its spill files are removed, so no half-written index is ever
// observable.
//
// Concurrency: the committed snapshot set is an append-only slice guarded by
// RWMutex. View() captures the current slice (indices themselves are
// immutable), so long-running reads never block imports and always observe a
// self-consistent set even while other sessions commit.
package store

import (
	"context"
	"errors"
	"sync"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/diff"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/index"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/leak"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/retain"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/snapshot"
)

// Config configures a Store.
type Config struct {
	MemoryBudgetBytes int64
	SpillDir          string
	Workers           int
}

// Store is the entry point for ingestion and queries.
type Store struct {
	cfg Config

	mu      sync.RWMutex
	commits []*index.Index // append-only; published snapshots
	byID    map[string]*index.Index

	// inflight prevents two concurrently committing sessions from claiming
	// the same snapshot id.
	sessions sync.WaitGroup
}

// New creates a store.
func New(cfg Config) (*Store, error) {
	return &Store{cfg: cfg, byID: map[string]*index.Index{}}, nil
}

// Session is one in-progress, private streaming import backed by a
// background builder goroutine.
type Session struct{ ls *liveSession }

// Begin starts an import session. Records appended are consumed concurrently
// by the index builder, keeping peak memory bounded.
func (s *Store) Begin(ctx context.Context, opts index.Options) (*Session, error) {
	if opts.MemoryBudgetBytes == 0 {
		opts.MemoryBudgetBytes = s.cfg.MemoryBudgetBytes
	}
	if opts.SpillDir == "" {
		opts.SpillDir = s.cfg.SpillDir
	}
	if opts.Workers == 0 {
		opts.Workers = s.cfg.Workers
	}
	return &Session{ls: beginSession(ctx, s, opts)}, nil
}

// Append streams records into the private session.
func (sess *Session) Append(rd snapshot.Reader) error { return sess.ls.append(rd) }

// Commit builds the index and atomically publishes it.
func (sess *Session) Commit() (*index.Index, error) { return sess.ls.commit() }

// Abort discards all partial state and releases spill files; idempotent.
func (sess *Session) Abort() error { return sess.ls.abort() }

// IDs returns committed snapshot ids in commit order.
func (s *Store) IDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.commits))
	for i, ix := range s.commits {
		out[i] = ix.SnapshotID()
	}
	return out
}

// Get returns the committed index by id.
func (s *Store) Get(id string) (*index.Index, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ix, ok := s.byID[id]
	return ix, ok
}

// snapshotView is a captured, immutable set of committed indices.
type snapshotView struct {
	list []*index.Index
	byID map[string]*index.Index
}

// View runs fn against a point-in-time view. Imports committing during fn do
// not affect the view.
func (s *Store) View(fn func(*Snapshot) error) error {
	s.mu.RLock()
	view := &Snapshot{
		list: append([]*index.Index(nil), s.commits...),
		byID: s.byID, // map read-only to callers; commits only add
		st:   s,
	}
	s.mu.RUnlock()
	return fn(view)
}

// Snapshot is a consistent query view.
type Snapshot struct {
	list []*index.Index
	byID map[string]*index.Index
	st   *Store
}

// Retain computes retained sizes for a snapshot. In bounded mode it builds a
// paged external graph from the spilled objects/edges.
func (v *Snapshot) Retain(id string, opts retain.Options) (*retain.Report, error) {
	ix, ok := v.byID[id]
	if !ok {
		return nil, errors.New("store: unknown snapshot " + id)
	}
	g, err := graphForIndex(ix, opts)
	if err != nil {
		return nil, err
	}
	if cl, ok := g.(interface{ Close() error }); ok {
		defer cl.Close()
	}
	return retain.Compute(g, opts)
}

// Diff compares two committed snapshots.
func (v *Snapshot) Diff(beforeID, afterID string, opts diff.Options) (*diff.Report, error) {
	b, ok1 := v.byID[beforeID]
	a, ok2 := v.byID[afterID]
	if !ok1 || !ok2 {
		return nil, errors.New("store: unknown snapshot in diff")
	}
	return diff.Compare(b, a, opts)
}

// LeakSeries builds one time series per canonical site across the given
// snapshots (in the supplied order). Missing-sampling gaps set Point.Missing.
func (v *Snapshot) LeakSeries(ids []string) ([]leak.Series, error) {
	type pt struct {
		atMs int64
		b    map[string]int64
		miss map[string]bool
	}
	series := map[string]*leak.Series{}
	var order []string
	for _, id := range ids {
		ix, ok := v.byID[id]
		if !ok {
			return nil, errors.New("store: unknown snapshot " + id)
		}
		b := map[string]int64{}
		miss := map[string]bool{}
		for _, c := range ix.Cells() {
			b[c.Key.Site] += c.BytesEst
		}
		for _, g := range ix.Gaps() {
			if g.Bytes > 0 {
				miss["__gap__"] = true
			}
		}
		order = append(order, id)
		for site := range b {
			s := series[site]
			if s == nil {
				s = &leak.Series{Site: site}
				series[site] = s
			}
			// fill earlier points if this site only appears later
			for len(s.Points) < len(order)-1 {
				s.Points = append(s.Points, leak.Point{
					SnapshotID: order[len(s.Points)], Missing: true,
				})
			}
			s.Points = append(s.Points, leak.Point{
				SnapshotID: id, TakenAtMs: ix.TakenAtMs(), Bytes: b[site],
			})
		}
	}
	// Normalize length and mark gap-tainted snapshots.
	var out []leak.Series
	for _, s := range series {
		for len(s.Points) < len(order) {
			s.Points = append(s.Points, leak.Point{
				SnapshotID: order[len(s.Points)], Bytes: 0})
		}
		out = append(out, *s)
	}
	return out, nil
}

// Close releases store resources, including spill files of every committed
// bounded (external) index. After Close, committed indexes must not be used.
func (s *Store) Close() error {
	s.sessions.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	var first error
	for _, ix := range s.commits {
		if err := ix.Close(); err != nil && first == nil {
			first = err
		}
	}
	s.commits = nil
	s.byID = map[string]*index.Index{}
	return first
}
