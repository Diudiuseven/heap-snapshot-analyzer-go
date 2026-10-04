# Data model & streaming format

All snapshots enter the analyzer as a **newline-delimited JSON (JSONL)**
stream: one JSON object per line, discriminated by `"t"`. Decoding is
streaming (one line at a time), so a producer can emit a snapshot far larger
than available RAM, directly from inside the process via `snapshot.JSONLWriter`.

## Record grammar

```jsonc
{"t":"meta","snapshot_id":"s-42","taken_at_ms":1727000000000,"format":"jsonl/v1","attrs":{}}
{"t":"type","id":1,"name":"[]byte","symbol":true}
{"t":"site","key":"trace-ab12","symbol":true,
  "stack":[{"func":"runtime.mallocgc","file":"malloc.go","line":1234}]}
{"t":"gap","bytes":4096,"note":"profiler could not attribute this interval"}
{"t":"obj","id":10,"type":1,"site":"trace-ab12","size":24,"root":"stack",
  "sample":{"w":[3,1],"gap":false,"gap_bytes":0},
  "edges":[{"to":11,"kind":"field","field":"p","strong":true}],
  "weak":[{"to":12,"kind":"weak"}],
  "unres":[{"token":"0x7f..","offset":128,"original":"<raw slot>"}]}
```

- `meta` **must** be the first record and appear exactly once.
- `type` registers a type. `symbol:false` (or missing `name`) means the type
  has no symbolic information; such types stay **distinct** (see symbols).
- `site` registers an allocation site. `key` is the producer's stable trace
  identity; omit it and the analyzer derives a key from the full stack
  fingerprint (`func\0file\0line` tuples). `symbol:false` / empty stack means
  symbols are missing.
- `gap` is a sampling interval whose bytes the producer could not attribute to
  any site. These bytes are carried as their own category
  (`missing_sampling`) and **never** folded into an allocation site.
- `obj` is one observed object.
  - `id` is the snapshot-unique, opaque 64-bit object identity (used to match
    objects across snapshots). The analyzer never assumes ids are dense; it
    assigns its own dense node index internally.
  - `type`/`site` are references to registered ids/keys. Objects referencing an
    unregistered site/type are still indexed; the site/type is registered as a
    missing symbol rather than dropped.
  - `root` non-empty marks a GC root; its value is the root kind (`stack`,
    `global`, ...).
  - `edges` are **strong** references; `weak` are weak references (recorded but
    excluded from reachability/retained-size, since a weak pointer does not
    retain).
  - `unres` records a reference slot the producer could not resolve to an
    object id. It is preserved with the originating object, raw token, stream
    offset and original text, so any result can be traced back to the raw
    snapshot.

### Sampling weight

`sample.w` is the exact **inverse inclusion probability** `w >= 1`:

- omitted  -> the object was enumerated, weight = 1;
- `[n,d]`  -> exact rational `n/d` (preferred);
- a bare number is accepted (integer exactly; decimal parsed exactly).

`gap:true` with `gap_bytes` attaches an unattributable byte region to the
object record (equivalent to a standalone `gap` record).

## In-memory/on-disk entities

| Entity | Fields of note |
|--------|----------------|
| `snapshot.ObjectRecord` | id, type, site, size, strong/weak edges, unresolved refs, sample |
| `index.Cell` | key(site,type), observed count, unbiased estimated count/bytes, exact rationals, sampled flag |
| `index.ObjInfo` | producer id, dense node index, canonical site/type, size, weight, root |
| `index.GapBucket` | unattributable sampling bytes (separate from every cell) |
| `index.UnresolvedRef` | aggregated unresolvable reference with provenance |
| `retain.SiteRetain` | site, exclusive retained bytes, dominated node count |
| `diff.Entry` | per-cell added/removed/retained(+net & growth), observed counts |
| `diff.Special` | `missing_sampling` / `unresolved` / `unattributed` |
| `leak.Verdict` | kind(unknown/steady/growing/leak_suspect), per-window evidence, reason |

## Canonical identities (cross-snapshot)

- Sites are canonicalized by producer `key` (or stack fingerprint when absent).
  The same key in later snapshots with newly available symbols **enriches** the
  existing identity rather than creating a second site.
- Types are canonicalized by name.
- Objects are matched across snapshots by producer `id`. Reused addresses are
  the producer's responsibility: the analyzer treats equal ids as the same
  object across two snapshots (added/removed/retained follow from id set
  differences).
