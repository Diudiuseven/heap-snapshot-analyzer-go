// heap-analyzer is a local CLI for heap snapshot analysis and leak
// attribution. All processing happens in-process; results are
// reproducible from the same inputs.
//
// Usage:
//
//	heap-analyzer import -store DIR [-id ID] FILE...   import snapshots
//	heap-analyzer top   -store DIR -snap ID [-n N]     top buckets by bytes
//	heap-analyzer diff  -store DIR -old A -new B       attribute growth
//	heap-analyzer leaks -store DIR [-flags]            time-series leak scan
//	heap-analyzer retain -store DIR -snap ID [-n N]    retained size by site
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/aggregate"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/diff"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/leak"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/retain"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "import":
		err = cmdImport(os.Args[2:])
	case "top":
		err = cmdTop(os.Args[2:])
	case "diff":
		err = cmdDiff(os.Args[2:])
	case "leaks":
		err = cmdLeaks(os.Args[2:])
	case "retain":
		err = cmdRetain(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: heap-analyzer <command> [flags]
commands: import, top, diff, leaks, retain`)
}

func openStore(dir string) (*store.Store, error) { return store.Open(dir) }

func cmdImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	dir := fs.String("store", "heapstore", "store directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := openStore(*dir)
	if err != nil {
		return err
	}
	for _, path := range fs.Args() {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		id, err := s.Import(context.Background(), f)
		f.Close()
		if err != nil {
			return fmt.Errorf("import %s: %w", path, err)
		}
		fmt.Printf("imported %s as %s\n", path, id)
	}
	return nil
}

func cmdTop(args []string) error {
	fs := flag.NewFlagSet("top", flag.ContinueOnError)
	dir := fs.String("store", "heapstore", "store directory")
	snap := fs.String("snap", "", "snapshot id")
	n := fs.Int("n", 20, "rows to print")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := openStore(*dir)
	if err != nil {
		return err
	}
	return s.Query(func(v store.View) error {
		tbl, ok := v.Table(*snap)
		if !ok {
			return fmt.Errorf("snapshot %q not found", *snap)
		}
		entries := tbl.Sorted()
		sort.Slice(entries, func(i, j int) bool { return entries[i].Cell.Bytes > entries[j].Cell.Bytes })
		for i, e := range entries {
			if i >= *n {
				break
			}
			fmt.Printf("site=%d type=%d bytes=%d count=%d\n", e.Key.Site, e.Key.Type, e.Cell.Bytes, e.Cell.Count)
		}
		return nil
	})
}

func cmdDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	dir := fs.String("store", "heapstore", "store directory")
	oldID := fs.String("old", "", "old snapshot id")
	newID := fs.String("new", "", "new snapshot id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := openStore(*dir)
	if err != nil {
		return err
	}
	return s.Query(func(v store.View) error {
		oldT, ok1 := v.Table(*oldID)
		newT, ok2 := v.Table(*newID)
		if !ok1 || !ok2 {
			return fmt.Errorf("snapshot not found")
		}
		rep := diff.CompareTables(oldT, newT)
		for _, e := range rep.Entries {
			if e.Kind == diff.Stable {
				continue
			}
			fmt.Printf("%-9s site=%d type=%d old=%d new=%d delta=%+d\n",
				e.Kind, e.Key.Site, e.Key.Type, e.OldBytes, e.NewBytes, e.Delta)
		}
		fmt.Printf("unattributed old=%d new=%d\n", rep.UnattributedOld, rep.UnattributedNew)
		return nil
	})
}

func cmdLeaks(args []string) error {
	fs := flag.NewFlagSet("leaks", flag.ContinueOnError)
	dir := fs.String("store", "heapstore", "store directory")
	window := fs.Int("window", 0, "observation window (0 = all)")
	minBytes := fs.Int64("min-bytes", 1<<20, "min absolute growth")
	minRatio := fs.Float64("min-ratio", 0.2, "min relative growth")
	minConsec := fs.Int("min-consecutive", 2, "min consecutive increases")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := openStore(*dir)
	if err != nil {
		return err
	}
	return s.Query(func(v store.View) error {
		series := map[aggregate.Key][]leak.Point{}
		for _, m := range v.Snapshots() {
			tbl, _ := v.Table(m.ID)
			for _, e := range tbl.Sorted() {
				series[e.Key] = append(series[e.Key], leak.Point{Timestamp: m.Timestamp, Bytes: e.Cell.Bytes})
			}
		}
		findings := leak.Detect(series, leak.Config{
			Window: *window, MinGrowthBytes: *minBytes,
			MinGrowthRatio: *minRatio, MinConsecutiveIncreases: *minConsec,
		})
		for _, f := range findings {
			fmt.Printf("LEAK site=%d type=%d growth=%+d (%.1f%%) over %d observations\n",
				f.Key.Site, f.Key.Type, f.GrowthBytes, f.GrowthRatio*100, f.Observations)
		}
		return nil
	})
}

func cmdRetain(args []string) error {
	fs := flag.NewFlagSet("retain", flag.ContinueOnError)
	dir := fs.String("store", "heapstore", "store directory")
	snap := fs.String("snap", "", "snapshot id")
	n := fs.Int("n", 20, "rows to print")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := openStore(*dir)
	if err != nil {
		return err
	}
	return s.Query(func(v store.View) error {
		sn, ok := v.Snapshot(*snap)
		if !ok {
			return fmt.Errorf("snapshot %q not found", *snap)
		}
		bySite := retain.BySite(sn.Objects, sn.Refs)
		type row struct {
			site  uint32
			bytes int64
		}
		var rows []row
		for site, b := range bySite {
			rows = append(rows, row{uint32(site), b})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].bytes > rows[j].bytes })
		for i, r := range rows {
			if i >= *n {
				break
			}
			fmt.Printf("site=%d retained=%d\n", r.site, r.bytes)
		}
		return nil
	})
}
