# Heap Snapshot Analyzer (Go)

A fully in-process, reproducible toolkit for analyzing heap snapshots and
attributing memory growth / leaks to allocation sites. It ingests either a
local snapshot file or an in-process snapshot stream, runs the entire pipeline
locally (no network I/O), and produces deterministic results.

The pipeline is:

```
parse (JSONL stream)
  -> aggregate by (allocation site, type)      [index, unbiased estimates]
  -> retained size via dominator tree          [retain]
  -> multi-snapshot comparison                 [diff]
  -> time-series leak detection                [leak]
```

A `store` provides transactional, isolated imports and self-consistent
concurrent query views; a `spill` layer keeps peak memory bounded when a
single snapshot is larger than RAM.

## Quick start

```go
import (
    "os"
    "github.com/Diudiuseven/heap-snapshot-analyzer-go"
    "github.com/Diudiuseven/heap-snapshot-analyzer-go/snapshot"
    "github.com/Diudiuseven/heap-snapshot-analyzer-go/index"
    "github.com/Diudiuseven/heap-snapshot-analyzer-go/retain"
    "github.com/Diudiuseven/heap-snapshot-analyzer-go/leak"
    "github.com/Diudiuseven/heap-snapshot-analyzer-go/store"
)

a, _ := analyzer.New(store.Config{MemoryBudgetBytes: 256 << 20}) // 0 = RAM only
defer a.Close()

f, _ := os.Open("snap-001.jsonl")
ix, rep, err := a.AnalyzeOne(
    snapshot.NewJSONLReader(f),
    index.Options{MemoryBudgetBytes: 256 << 20},
    retain.Options{},
)
// rep.BySite[i].RetainedSize  -> bytes exclusively retained by that site
// ix.Cells()                  -> unbiased (site,type) estimates
```

Compare snapshots and detect leaks:

```go
report, _ := a.Compare(readerBefore, readerAfter, diff.Options{})
// report.Entries: added / removed / retained(+growth), attributed to a site
// report.Special: missing_sampling / unresolved / unattributed (never folded
//                  into a normal cell)

verdicts, _ := a.DetectLeaks([]string{"s1","s2","s3","s4"}, leak.DefaultOptions())
```

## Documentation

- [Data model & streaming format](docs/data-model.md)
- [Configuration](docs/configuration.md)
- [Consistency & determinism semantics](docs/consistency.md)
- [Retained size methodology](docs/retained-size.md)
- [Leak detection: thresholds & false positive/negative handling](docs/leak-detection.md)

## Requirements mapping

| # | Requirement | Where |
|---|-------------|-------|
| 1 | allocation/ref graph, (site,type) aggregation, unbiased sampling estimates, order/concurrency independent | `index`, `weight`, `snapshot` |
| 2 | multi-snapshot diff (new/reclaimed/sustained growth) attributed to sites; missing-sampling & unattributed isolated | `diff` |
| 3 | retained size, exclusive ownership, no double count on sharing, cycle-safe | `retain` |
| 4 | time-series leak verdicts, configurable thresholds/windows, documented FP/FN behavior | `leak`, `docs/leak-detection.md` |
| 5 | incremental imports + concurrent queries, self-consistent views, atomic/abortable writes | `store` |
| 6 | snapshots larger than RAM, bounded peak memory, external sort + paged arrays | `spill`, `index` (bounded), `retain` (external) |
| 7 | missing/multi-source symbols preserved distinctly, unresolvable refs traceable | `symbol`, `index.Unresolved`, dangling edges |
| 8 | automated tests + documentation | `*_test.go`, `docs/` |

## Running tests

```sh
go test ./...                  # unit + integration (fast parts)
go test -race ./store/         # concurrency/visibility
go test -count=1 ./...         # large bounded-memory test included (~20s)
go test -short ./...           # skip the 200k-object large test
```
