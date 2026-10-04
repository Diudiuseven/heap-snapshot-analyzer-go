// Package store provides incremental snapshot import with concurrent
// queries.
//
// Consistency model:
//   - An import stages all data under tmp/<id>/ and commits with a
//     single atomic rename into snapshots/<id>/, then swaps the
//     in-memory index under a write lock. A query therefore sees
//     either the state before the import or after it — never a
//     half-written index.
//   - If an import fails or is cancelled, the staging directory is
//     removed and the store is left byte-identical to its prior
//     state.
//   - On Open, leftover staging directories and incomplete snapshot
//     directories (crash between rename and index write) are cleaned
//     up, so recovery is deterministic.
//   - In-memory state per snapshot is bounded by the number of
//     aggregation buckets (not objects); the raw record stream is
//     kept on disk and re-read on demand, so peak memory does not
//     grow with snapshot size.
package store

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/aggregate"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/model"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/parse"
)

// View is a consistent, read-only view of the store at one version.
type View interface {
	// Snapshots lists imported snapshot metadata in import order.
	Snapshots() []model.Meta
	// Table returns the aggregate table of one snapshot.
	Table(id string) (*aggregate.Table, bool)
	// Snapshot re-reads the full snapshot from disk, if present.
	Snapshot(id string) (*model.Snapshot, bool)
}

type entry struct {
	meta  model.Meta
	table *aggregate.Table
	file  string // committed record file
}

// view is an immutable snapshot of the index.
type view struct {
	store   *Store
	order   []string
	entries map[string]*entry
}

func (v *view) Snapshots() []model.Meta {
	out := make([]model.Meta, 0, len(v.order))
	for _, id := range v.order {
		out = append(out, v.entries[id].meta)
	}
	return out
}

func (v *view) Table(id string) (*aggregate.Table, bool) {
	e, ok := v.entries[id]
	if !ok {
		return nil, false
	}
	return e.table, true
}

func (v *view) Snapshot(id string) (*model.Snapshot, bool) {
	e, ok := v.entries[id]
	if !ok {
		return nil, false
	}
	f, err := os.Open(e.file)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	s, err := parse.ReadAll(f)
	if err != nil {
		return nil, false
	}
	return s, true
}

// Store manages imported snapshots.
type Store struct {
	dir      string
	importMu sync.Mutex // serializes imports
	mu       sync.RWMutex
	order    []string
	entries  map[string]*entry
	seq      int
}

// Open opens (and recovers) a store rooted at dir.
func Open(dir string) (*Store, error) {
	s := &Store{dir: dir, entries: map[string]*entry{}}
	for _, sub := range []string{"tmp", "snapshots"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	// Recovery: drop any staging directories from interrupted imports.
	tmp := filepath.Join(dir, "tmp")
	tentries, err := os.ReadDir(tmp)
	if err != nil {
		return nil, err
	}
	for _, te := range tentries {
		if err := os.RemoveAll(filepath.Join(tmp, te.Name())); err != nil {
			return nil, fmt.Errorf("store: recover tmp: %w", err)
		}
	}
	// Load committed snapshots; incomplete ones are removed.
	sdir := filepath.Join(dir, "snapshots")
	sentries, err := os.ReadDir(sdir)
	if err != nil {
		return nil, err
	}
	for _, se := range sentries {
		if !se.IsDir() {
			continue
		}
		dirPath := filepath.Join(sdir, se.Name())
		recFile := filepath.Join(dirPath, "records.jsonl")
		if _, err := os.Stat(recFile); err != nil {
			// Half-written commit: remove it.
			if err := os.RemoveAll(dirPath); err != nil {
				return nil, fmt.Errorf("store: recover snapshot: %w", err)
			}
			continue
		}
		e, err := loadEntry(recFile)
		if err != nil {
			if err := os.RemoveAll(dirPath); err != nil {
				return nil, fmt.Errorf("store: recover snapshot: %w", err)
			}
			continue
		}
		id := e.meta.ID
		if id == "" {
			id = se.Name()
			e.meta.ID = id
		}
		if _, dup := s.entries[id]; dup {
			continue
		}
		s.entries[id] = e
		s.order = append(s.order, id)
	}
	sort.Strings(s.order)
	return s, nil
}

// loadEntry rebuilds the in-memory index for a committed record file.
func loadEntry(recFile string) (*entry, error) {
	f, err := os.Open(recFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	e := &entry{table: aggregate.New(), file: recFile}
	if err := streamAggregate(f, &e.meta, e.table); err != nil {
		return nil, err
	}
	return e, nil
}

// Close releases resources.
func (s *Store) Close() error { return nil }

// Import atomically imports one snapshot read from r.
// On failure or cancellation the store is left unchanged.
func (s *Store) Import(ctx context.Context, r io.Reader) (string, error) {
	s.importMu.Lock()
	defer s.importMu.Unlock()

	s.mu.RLock()
	seq := s.seq
	s.mu.RUnlock()
	tmpDir := filepath.Join(s.dir, "tmp", fmt.Sprintf("import-%d", seq))
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return "", err
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(tmpDir)
		}
	}()

	recFile := filepath.Join(tmpDir, "records.jsonl")
	out, err := os.Create(recFile)
	if err != nil {
		return "", err
	}
	bw := bufio.NewWriter(out)
	e := &entry{table: aggregate.New(), file: recFile}
	// Tee the input: persist raw records while aggregating.
	tee := io.TeeReader(r, bw)
	if err := streamAggregateContext(ctx, tee, &e.meta, e.table); err != nil {
		out.Close()
		return "", err
	}
	if err := bw.Flush(); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}

	id := e.meta.ID
	if id == "" {
		id = fmt.Sprintf("snap-%d-%d", e.meta.Timestamp, seq)
		e.meta.ID = id
	}

	s.mu.Lock()
	if _, dup := s.entries[id]; dup {
		s.mu.Unlock()
		return "", fmt.Errorf("store: duplicate snapshot id %q", id)
	}
	// Atomic commit: rename the staged directory into place.
	finalDir := filepath.Join(s.dir, "snapshots", id)
	if err := os.Rename(tmpDir, finalDir); err != nil {
		s.mu.Unlock()
		return "", err
	}
	e.file = filepath.Join(finalDir, "records.jsonl")
	s.entries[id] = e
	s.order = append(s.order, id)
	s.seq++
	s.mu.Unlock()
	committed = true
	return id, nil
}

// Query runs fn against a consistent view of the current state.
func (s *Store) Query(fn func(View) error) error {
	s.mu.RLock()
	// Shallow-copy the index: entries are immutable once committed,
	// so the view stays consistent after the lock is released.
	entries := make(map[string]*entry, len(s.entries))
	for k, v := range s.entries {
		entries[k] = v
	}
	v := &view{store: s, order: append([]string(nil), s.order...), entries: entries}
	s.mu.RUnlock()
	return fn(v)
}

// streamAggregate consumes records, updating meta and table.
func streamAggregate(r io.Reader, meta *model.Meta, t *aggregate.Table) error {
	return streamAggregateContext(context.Background(), r, meta, t)
}

func streamAggregateContext(ctx context.Context, r io.Reader, meta *model.Meta, t *aggregate.Table) error {
	rd := parse.NewReader(r)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rec, err := rd.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch v := rec.(type) {
		case parse.MetaRecord:
			*meta = v.Meta
		case parse.ObjectRecord:
			t.Add(v.Object)
		}
	}
}
