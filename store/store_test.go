package store_test

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/index"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/retain"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/snapshot"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/store"
)

func goodReader(id string, n int) *snapshot.JSONLReader {
	var b bytes.Buffer
	w := snapshot.NewJSONLWriter(&b)
	_ = w.WriteMeta(snapshot.Meta{SnapshotID: id, TakenAtMs: int64(n)})
	_ = w.WriteType(snapshot.TypeInfo{ID: 1, Name: "A", Symbol: true})
	_ = w.WriteSite(snapshot.AllocSite{Key: "k", Symbol: true})
	for i := 0; i < n; i++ {
		root := i == 0
		o := snapshot.ObjectRecord{ID: snapshot.ID(i + 1), TypeID: 1,
			SiteKey: "k", Size: 8, IsRoot: root}
		if root {
			o.RootKind = "stack"
			o.Strong = []snapshot.Edge{{To: snapshot.ID(2), Strong: true}}
		}
		_ = w.WriteObject(o)
	}
	_ = w.Flush()
	return snapshot.NewJSONLReader(bytes.NewReader(b.Bytes()))
}

func TestCommitAndQuery(t *testing.T) {
	st, err := store.New(store.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := st.Begin(context.Background(), index.Options{})
	if err := sess.Append(goodReader("s1", 3)); err != nil {
		t.Fatal(err)
	}
	ix, err := sess.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if ix.NObjects() != 3 {
		t.Fatalf("objects %d", ix.NObjects())
	}
	if got := st.IDs(); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("ids %v", got)
	}
	_ = st.View(func(v *store.Snapshot) error {
		r, err := v.Retain("s1", retain.Options{})
		if err != nil || r.TotalShallow != 24 {
			t.Fatalf("retain total=%d err=%v", r.TotalShallow, err)
		}
		return nil
	})
}

func TestAbortLeavesNothing(t *testing.T) {
	st, _ := store.New(store.Config{})
	sess, _ := st.Begin(context.Background(), index.Options{})
	if err := sess.Append(goodReader("partial", 5)); err != nil {
		t.Fatal(err)
	}
	if err := sess.Abort(); err != nil {
		t.Fatal(err)
	}
	if len(st.IDs()) != 0 {
		t.Fatalf("aborted session must not publish, got %v", st.IDs())
	}
	if _, ok := st.Get("partial"); ok {
		t.Fatal("aborted snapshot must not be retrievable")
	}
	if err := sess.Abort(); err != nil {
		t.Fatal(err)
	}
}

func TestBuildFailureNotPublished(t *testing.T) {
	st, _ := store.New(store.Config{})
	sess, _ := st.Begin(context.Background(), index.Options{})
	bad := strings.NewReader("{\"t\":\"meta\",\"snapshot_id\":\"x\"}\n{bad\n")
	if err := sess.Append(snapshot.NewJSONLReader(bad)); err != nil {
		_ = sess.Abort()
	} else if _, err := sess.Commit(); err == nil {
		t.Fatal("commit must fail on malformed input")
	}
	if len(st.IDs()) != 0 {
		t.Fatalf("failed commit must not publish, got %v", st.IDs())
	}
}

func TestConcurrentQueriesAndImports(t *testing.T) {
	st, _ := store.New(store.Config{})
	sess, _ := st.Begin(context.Background(), index.Options{})
	_ = sess.Append(goodReader("s0", 2))
	if _, err := sess.Commit(); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var last int
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = st.View(func(v *store.Snapshot) error {
					ids := st.IDs()
					cur := len(ids)
					if cur < last {
						t.Errorf("view shrank: %d -> %d", last, cur)
					}
					for _, id := range ids {
						if _, ok := st.Get(id); !ok {
							t.Errorf("id %s listed but not found", id)
						}
					}
					last = cur
					return nil
				})
			}
		}()
	}
	for i := 1; i <= 20; i++ {
		s, _ := st.Begin(context.Background(), index.Options{})
		id := "s" + string(rune('a'+i%26)) + itoaX(i)
		_ = s.Append(goodReader(id, 2))
		if _, err := s.Commit(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(5 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func itoaX(i int) string {
	if i == 0 {
		return "0"
	}
	var b [8]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

// TestAbortThenRecover verifies an interrupted import leaves the store able
// to accept a later, successful import with no half-written state.
func TestAbortThenRecover(t *testing.T) {
	st, _ := store.New(store.Config{})
	bad, _ := st.Begin(context.Background(), index.Options{})
	_ = bad.Append(goodReader("ghost", 4))
	if err := bad.Abort(); err != nil {
		t.Fatal(err)
	}
	good, _ := st.Begin(context.Background(), index.Options{})
	if err := good.Append(goodReader("real", 2)); err != nil {
		t.Fatal(err)
	}
	if _, err := good.Commit(); err != nil {
		t.Fatal(err)
	}
	if ids := st.IDs(); len(ids) != 1 || ids[0] != "real" {
		t.Fatalf("store did not recover cleanly: %v", ids)
	}
}

func TestDuplicateIDRejected(t *testing.T) {
	st, _ := store.New(store.Config{})
	s1, _ := st.Begin(context.Background(), index.Options{})
	_ = s1.Append(goodReader("dup", 2))
	if _, err := s1.Commit(); err != nil {
		t.Fatal(err)
	}
	s2, _ := st.Begin(context.Background(), index.Options{})
	_ = s2.Append(goodReader("dup", 3))
	if _, err := s2.Commit(); err == nil {
		t.Fatal("duplicate snapshot id must be rejected")
	}
	if ids := st.IDs(); len(ids) != 1 {
		t.Fatalf("rejected commit must not alter store, got %v", ids)
	}
}
