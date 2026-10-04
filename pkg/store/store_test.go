package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// snapStream builds a JSONL record stream for one snapshot.
func snapStream(id string, ts int64, sizes ...int64) io.Reader {
	var b bytes.Buffer
	fmt.Fprintf(&b, `{"kind":"meta","id":%q,"timestamp":%d}`+"\n", id, ts)
	for i, sz := range sizes {
		fmt.Fprintf(&b, `{"kind":"object","numID":%d,"size":%d,"site":%d}`+"\n", i+1, sz, i+1)
	}
	return &b
}

func TestImportAndQuery(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Import(context.Background(), snapStream("a", 1, 100, 200))
	if err != nil {
		t.Fatal(err)
	}
	if id != "a" {
		t.Fatalf("id %q", id)
	}
	err = s.Query(func(v View) error {
		metas := v.Snapshots()
		if len(metas) != 1 || metas[0].ID != "a" {
			return fmt.Errorf("metas %+v", metas)
		}
		tbl, ok := v.Table("a")
		if !ok {
			return errors.New("table missing")
		}
		if tbl.Total() != 300 {
			return fmt.Errorf("total %d", tbl.Total())
		}
		snap, ok := v.Snapshot("a")
		if !ok || len(snap.Objects) != 2 {
			return errors.New("snapshot re-read failed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFailedImportLeavesNoTrace(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Stream that breaks in the middle.
	bad := io.MultiReader(
		strings.NewReader(`{"kind":"meta","id":"bad","timestamp":1}`+"\n"),
		strings.NewReader("this is not json\n"),
	)
	if _, err := s.Import(context.Background(), bad); err == nil {
		t.Fatal("expected error")
	}
	if err := s.Query(func(v View) error {
		if len(v.Snapshots()) != 0 {
			return errors.New("failed import visible")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Reopen: no staging dirs, no half-written snapshots.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Query(func(v View) error {
		if len(v.Snapshots()) != 0 {
			return errors.New("failed import survived reopen")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledImportLeavesNoTrace(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	_, err = s.Import(ctx, snapStream("x", 1, 1, 2, 3))
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if err := s.Query(func(v View) error {
		if len(v.Snapshots()) != 0 {
			return errors.New("cancelled import visible")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCrashRecoveryRemovesStaging(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(context.Background(), snapStream("ok", 1, 5)); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash: leave a staging dir and a half-written
	// snapshot dir behind.
	if err := os.MkdirAll(filepath.Join(dir, "tmp", "import-9"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "snapshots", "half"), 0o755); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Query(func(v View) error {
		m := v.Snapshots()
		if len(m) != 1 || m[0].ID != "ok" {
			return fmt.Errorf("got %+v", m)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tmp", "import-9")); !os.IsNotExist(err) {
		t.Fatal("staging dir not cleaned")
	}
	if _, err := os.Stat(filepath.Join(dir, "snapshots", "half")); !os.IsNotExist(err) {
		t.Fatal("half-written snapshot not cleaned")
	}
}

func TestConcurrentImportAndQuery(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var wg sync.WaitGroup
	// Concurrent queries must always observe a self-consistent state:
	// the number of visible snapshots is monotonic per-view and every
	// visible snapshot has a complete table.
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if err := s.Query(func(v View) error {
					for _, m := range v.Snapshots() {
						tbl, ok := v.Table(m.ID)
						if !ok {
							return fmt.Errorf("snapshot %q visible without table", m.ID)
						}
						if tbl.Total() <= 0 {
							return fmt.Errorf("snapshot %q has empty table", m.ID)
						}
					}
					return nil
				}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("s%d", i)
		if _, err := s.Import(ctx, snapStream(id, int64(i), 10, 20)); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	if err := s.Query(func(v View) error {
		if len(v.Snapshots()) != 20 {
			return fmt.Errorf("got %d snapshots", len(v.Snapshots()))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDuplicateIDRejected(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.Import(ctx, snapStream("dup", 1, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(ctx, snapStream("dup", 2, 2)); err == nil {
		t.Fatal("duplicate id accepted")
	}
}
