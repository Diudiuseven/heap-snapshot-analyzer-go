// Package tests contains end-to-end scenarios across packages.
package tests

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"testing"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/aggregate"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/diff"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/leak"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/store"
)

// genStream produces a snapshot stream with n objects across the
// given number of sites, without materializing it in memory.
type genStream struct {
	n, i   int
	sites  int
	sizeFn func(i int) int64
	buf    []byte
}

func (g *genStream) Read(p []byte) (int, error) {
	total := 0
	for total < len(p) {
		if len(g.buf) == 0 {
			if g.i == 0 {
				g.buf = []byte(`{"kind":"meta","id":"big","timestamp":1}` + "\n")
			} else if g.i <= g.n {
				sz := g.sizeFn(g.i)
				site := g.i%g.sites + 1
				g.buf = []byte(fmt.Sprintf(`{"kind":"object","numID":%d,"size":%d,"site":%d,"scaleNum":2,"scaleDen":1}`+"\n", g.i, sz, site))
			} else {
				if total > 0 {
					return total, nil
				}
				return 0, io.EOF
			}
			g.i++
		}
		n := copy(p[total:], g.buf)
		g.buf = g.buf[n:]
		total += n
	}
	return total, nil
}

// A snapshot far larger than what we want to hold in memory must be
// importable and analyzable with bounded peak memory.
func TestHugeSnapshotStreamingImport(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	const n = 2_000_000
	const sites = 1000
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	g := &genStream{n: n, sites: sites, sizeFn: func(i int) int64 { return int64(i%97 + 1) }}
	if _, err := s.Import(context.Background(), g); err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	growth := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	// The raw stream is ~150MB; in-memory state must stay far below
	// the input size (bounded by the number of buckets, not objects).
	if limit := int64(200 << 20); growth > limit {
		t.Fatalf("heap grew by %d bytes (> %d) for a %d-object import", growth, limit, n)
	}

	// Verify aggregate correctness: each object is scaled x2.
	var want int64
	for i := 1; i <= n; i++ {
		want += int64(i%97+1) * 2
	}
	if err := s.Query(func(v store.View) error {
		tbl, ok := v.Table("big")
		if !ok {
			return fmt.Errorf("table missing")
		}
		if got := tbl.Total(); got != want {
			return fmt.Errorf("total %d, want %d", got, want)
		}
		if len(tbl.Cells) != sites {
			return fmt.Errorf("buckets %d, want %d", len(tbl.Cells), sites)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Full pipeline: import a time series of snapshots, diff consecutive
// ones, and run leak detection over the series.
func TestEndToEndLeakAttribution(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Site 1 grows steadily; site 2 oscillates; site 3 is flat.
	sizes := func(step int) (a, b, c int64) {
		a = int64(1000 + 500*step)
		b = 2000
		if step%2 == 1 {
			b = 4000
		}
		c = 500
		return
	}
	var ids []string
	for step := 0; step < 5; step++ {
		a, b, c := sizes(step)
		id := fmt.Sprintf("snap%d", step)
		stream := io.MultiReader(
			fmtReader(`{"kind":"meta","id":%q,"timestamp":%d}`, id, int64(step+1)),
			fmtReader(`{"kind":"object","numID":1,"size":%d,"site":1}`, a),
			fmtReader(`{"kind":"object","numID":2,"size":%d,"site":2}`, b),
			fmtReader(`{"kind":"object","numID":3,"size":%d,"site":3}`, c),
		)
		if _, err := s.Import(ctx, stream); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}

	series := map[aggregate.Key][]leak.Point{}
	var prev *aggregate.Table
	if err := s.Query(func(v store.View) error {
		for i, id := range ids {
			tbl, _ := v.Table(id)
			if prev != nil {
				rep := diff.CompareTables(prev, tbl)
				// Site 1 must be attributed as growing at every step.
				found := false
				for _, e := range rep.Entries {
					if e.Kind == diff.Growing && e.Key.Site == 1 {
						found = true
					}
				}
				if !found {
					return fmt.Errorf("step %d: growth at site 1 not attributed", i)
				}
			}
			prev = tbl
			for _, e := range tbl.Sorted() {
				series[e.Key] = append(series[e.Key], leak.Point{Timestamp: int64(i + 1), Bytes: e.Cell.Bytes})
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	findings := leak.Detect(series, leak.Config{
		Window:                  5,
		MinGrowthBytes:          100,
		MinGrowthRatio:          0.1,
		MinConsecutiveIncreases: 2,
	})
	if len(findings) != 1 || findings[0].Key.Site != 1 {
		t.Fatalf("findings %+v", findings)
	}
}

func fmtReader(format string, args ...any) io.Reader {
	return &byteReader{b: []byte(fmt.Sprintf(format+"\n", args...))}
}

type byteReader struct {
	b []byte
	i int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
