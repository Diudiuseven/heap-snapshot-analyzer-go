# Consistency & determinism semantics

## 1. Unbiased, order-independent sampling estimates

Each observed object is inflated by its inverse inclusion probability
`w = 1/p >= 1`. Aggregates are kept as **exact rationals** (`math/big.Rat`)
and rounded **once**, at the output boundary, using deterministic
*round-half-away-from-zero*.

Consequences (all are tested):

- **Record order independence:** rational addition is commutative and
  associative, so feeding objects in any permutation gives identical exact
  sums and identical rounded cells (`weight.TestOrderIndependence`,
  `index.TestSamplingEstimateAndOrder`).
- **Concurrency/worker independence:** shards produce independent partial
  rational sums which are merged exactly; merging is associative, so any
  shard count yields the same result. There is no float rounding until the
  final per-cell integer is produced.
- **Unbiased totals:** estimated count = Σ wᵢ and estimated shallow bytes =
  Σ wᵢ·sizeᵢ are the Horvitz-Thompson estimators for a Bernoulli/Poisson
  inclusion process with per-object inclusion probability pᵢ.
- **Estimator vs truth:** unbiasedness concerns expectation over repeated
  sampling; a single sampled snapshot still produces an estimate, not an exact
  census. We never silently substitute an exact value for an estimate.

Explicit gaps (`gap` records, `sample.gap_bytes`) are **not** estimates of any
site. They are summed and reported as `missing_sampling` so "unattributable"
can never be mistaken for a real allocation site.

## 2. Diff consistency

Objects are matched by producer id using an ordered **merge join** over
id-sorted streams, so:

- added/removed/retained categories are exact set differences (no hashing to
  approximate membership);
- the same two indexes always produce the same report, regardless of build
  order;
- estimated bytes use each side's own exact weights;
- growth is always attached to a concrete `(site,type)` cell; the three
  non-attributable categories are emitted only under `Report.Special`.

An object whose site/type changed between snapshots is counted as removed
from the old cell and added to the new cell.

## 3. Retained size consistency

Retained attribution is derived from a dominator tree, which is a property of
the graph, not of traversal order. Successors are scanned in ascending dense
id to make vertex numbering deterministic. Sharing and cycles are handled by
the dominator definition itself (see [retained-size.md](retained-size.md)).

## 4. Import/query consistency (MVCC-like)

- The committed snapshot set is an **append-only** slice guarded by an
  `RWMutex`. `Index` objects are immutable after publication.
- `Store.View` captures the current committed set for the duration of the
  callback; an import committing during the query is invisible to it, so every
  query observes one self-consistent set. Reads never block writes.
- Each import runs in an isolated session with a private index and private
  spill directory:
  - `Commit` is the only point at which the immutable index becomes visible;
  - duplicate snapshot ids are rejected (the new index is not published);
  - any parse/build error or `Abort` discards the partial result and removes
    its spill files — no half-written index is ever observable
    (`store.TestAbortLeavesNothing`, `store.TestBuildFailureNotPublished`).

## 5. Bounded-memory guarantees

When a positive `MemoryBudgetBytes` is configured:

- snapshot records flow through a bounded channel into a background builder
  (the producer blocks when the consumer is behind);
- external sort seals in-memory runs to disk at the budget boundary and
  k-way merges them;
- dominator working arrays and node metadata use LRU-paged disk arrays;
- edge translation (producer id -> dense) is an external sort/merge rather
  than an O(n) in-memory map.

The budget is a **soft** cap: a single oversized record may exceed it, but
aggregate buffering does not grow without bound (`index.TestExternalMatchesRAM`,
the package-level `TestLargeSnapshotBoundedMemory`).

## 6. Missing symbols & unresolved references

- Missing symbols are kept as first-class, distinguishable identities and are
  never silently merged:
  - a site with a producer key but no symbols keeps that stable key (flagged
    `Missing`) so its time series stays trackable; resolution later enriches
    the same identity;
  - a site with **neither key nor frames** cannot be proven equal to anything,
    so each occurrence gets a distinct `missing:<n>` id and is reported as
    `unattributed`;
  - two sources that disagree about a key record a `symbol.Conflict` while
    retaining both variants.
- Unresolvable reference slots are preserved (`UnresolvedRef`: originating
  object, token, offset, original text); strong edges whose target id is never
  declared become dangling edges reported by the retain/external builders
  rather than crashing the analysis.

## 7. Reproducibility

Given identical input bytes and options, every stage produces byte-identical
categorical results and identical integer estimates. Nothing depends on
wall-clock time, goroutine scheduling, map iteration order (all output slices
are explicitly sorted) or network state.
