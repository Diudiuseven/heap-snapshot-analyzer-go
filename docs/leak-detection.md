# Leak detection: thresholds, windows and FP/FN behavior

The detector consumes one ordered `Series` of points per allocation site
(bytes/count per snapshot) and returns an explainable `Verdict`.

## Window evaluation

A window is `WindowSize` consecutive snapshots. For each window:

- `risingPairs` counts strictly rising adjacent pairs;
- `risingRatio = risingPairs / (WindowSize-1)`;
- `growth = bytes(last) - bytes(first)`;
- `growthRatio = growth / bytes(first)` (defined relative to first);
- a **reclamation dip** is any adjacent pair that drops by at least
  `DipReclaimRatio` of the pair's higher value.

A window **passes** iff all hold:

```
risingRatio   >= MinRisingRatio
growth        >= MinGrowthBytes
growthRatio   >= MinGrowthRatio
no reclamation dip
```

Every failing condition is recorded as a human-readable reason in
`WindowEval.Reasons` (`leak.TestVerdictExplainability`).

## Series verdict

Sliding windows are formed across all points.

| Verdict | Condition |
|---------|-----------|
| `leak_suspect` | at least `MinConsecutiveWindows` **consecutive** passing windows |
| `growing` | at least one window passes, but the consecutive-run requirement is not met |
| `steady` | enough data, no window passes (each window carries its failure reasons) |
| `unknown` | fewer than `MinSnapshots` points, **or** any required point is tainted by missing-sampling data |

## False positives (avoiding noise alerts)

1. **Absolute floor** (`MinGrowthBytes`): micro-growth of long-lived large
   structures is ignored (`leak.TestSteadyMicroGrowthFiltered`).
2. **Relative floor** (`MinGrowthRatio`): proportional noise must clear the
   configured percentage.
3. **Rising-ratio floor**: occasional up-ticks in a flat series do not pass.
4. **Reclamation dip reset**: a series that grows and then substantially frees
   memory behaves like a cache/workload burst, not a leak; any window
   containing such a dip fails, which breaks the consecutive-window streak
   (`leak.TestDipResets`).

## False negatives (avoiding missed leaks / unjustified "clean")

- Insufficient evidence is **never** reported as "not leaking". A series too
  short to fill a window, or containing a snapshot whose site bytes depend on
  missing-sampling regions, is explicitly `unknown`
  (`leak.TestUnknownOnShortOrMissing`).
- A site that is clearly growing but has not yet sustained across enough
  windows is `growing`, not `steady`, so rising-but-unconfirmed trends remain
  visible.
- Verdicts retain every `WindowEval` (ratios, growth, dip flag, reasons), so
  operators can audit why a site was or was not promoted, and tune thresholds
  rather than trusting a black-box boolean.

## Time series construction

`store.Snapshot.LeakSeries(ids)` builds points in the supplied snapshot order:

- each point's bytes are the sum of the site's **estimated** shallow bytes
  across its cells;
- sites absent in an earlier snapshot get a missing/zero point rather than
  fabricating a value;
- snapshots carrying unattributable `gap` bytes taint the series so the
  detector can return `unknown` instead of a misleading clean verdict.
