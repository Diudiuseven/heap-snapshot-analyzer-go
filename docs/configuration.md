# Configuration

## `store.Config`

| Field | Default | Meaning |
|-------|---------|---------|
| `MemoryBudgetBytes` | `0` (RAM-only) | Soft cap on live buffered bytes. `>0` enables the bounded/spill pipeline for every stage that can spill. |
| `SpillDir` | OS temp dir | Root directory for temporary spill files (chunked sort runs, paged arrays, edges). One store may create a fresh subdirectory per build. |
| `Workers` | `1` | Parsing shards. Results are identical for any value because shards only produce commutative exact partial sums. |

## `index.Options`

Same three fields as `store.Config`, scoped to one build. With
`MemoryBudgetBytes <= 0` the whole snapshot is materialized in RAM (fast path
for small inputs/tests). With a positive budget:

- per-object cell rows stream into a **bounded channel** and through an
  **external sort/merge** (sort runs are sealed to disk on overflow);
- object records and strong edges are appended to spill files;
- the final aggregates come from a k-way merge of exact-rational partial sums.

Peak *buffering* is bounded by the budget. Two structures scale with object
count by design: the final immutable `index.Cell` set (one entry per distinct
(site,type), normally tiny) and the analyzer's own dense-node bookkeeping used
for point queries; the heavy graph/aggregation state is paged or streamed.

## `retain.Options`

| Field | Meaning |
|-------|---------|
| `MemoryBudgetBytes` | Bounds the dominator pass. When set, the graph is built as a paged external CSR and the Lengauer-Tarjan working arrays are disk-backed (LRU pages); edge lists use external sort. |
| `SpillDir` | Root for graph spill files. |

## `diff.Options`

No thresholds: comparison is a deterministic streaming merge-join keyed by
object id. Memory use is independent of snapshot size.

## `leak.Options`

| Field | Default | Meaning |
|-------|---------|---------|
| `WindowSize` | `4` | Consecutive snapshots evaluated as one window. |
| `MinSnapshots` | `4` | Minimum points required for any verdict (`<` => `unknown`). |
| `MinRisingRatio` | `0.75` | Fraction of adjacent pairs in a window that must rise. |
| `MinGrowthBytes` | `1024` | Absolute minimum window growth. |
| `MinGrowthRatio` | `0.10` | Relative minimum growth vs window start. |
| `MinConsecutiveWindows` | `2` | Passing consecutive windows required for `leak_suspect`. |
| `DipReclaimRatio` | `0.50` | A single adjacent drop reclaiming this fraction of the higher value marks the window as a "reclamation dip" and fails it. |

Every field falls back to the documented default when its zero value is used.
