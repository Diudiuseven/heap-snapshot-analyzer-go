// Package index builds queryable aggregated indexes from snapshot streams.
//
// Objects are aggregated by (allocation site, type). Sampled objects are
// inflated by exact-rational weights; aggregates are rounded once at output,
// so results are identical for any record order or worker count.
//
// When MemoryBudgetBytes > 0, object records and partial aggregates are
// spilled to disk and merged externally; peak buffering stays bounded by the
// budget regardless of input size. With MemoryBudgetBytes <= 0 everything is
// materialized in RAM (fast path for small inputs and tests).
package index

import (
	"errors"
	"io"
	"sort"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/snapshot"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/symbol"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/weight"
)

// Options controls index construction.
type Options struct {
	// MemoryBudgetBytes bounds buffered state. <=0 selects the RAM-only fast
	// path (suitable for small inputs/tests).
	MemoryBudgetBytes int64
	// SpillDir is the root for temporary spill files ("" = os.TempDir).
	SpillDir string
	// Workers is the number of parsing shards. Results are identical for any
	// value because shards only produce commutative exact partial sums.
	Workers int
}

// CellKey identifies an aggregation cell.
type CellKey struct {
	Site string
	Type string
}

// Cell is the aggregated estimate for one (site, type) pair.
type Cell struct {
	Key        CellKey
	Observed   int64  // number of observed objects
	CountEst   int64  // unbiased estimated object count (rounded)
	BytesEst   int64  // unbiased estimated shallow bytes (rounded)
	CountExact []byte // exact rational count (gob big.Rat), used for merges
	BytesExact []byte // exact rational bytes (gob big.Rat)
	Sampled    bool   // at least one contributing object was sampled
}

// ObjInfo is the indexed per-object record.
type ObjInfo struct {
	ID       snapshot.ID
	Dense    int64
	Site     string
	Type     string
	Size     int64
	Weight   weight.Rat
	IsRoot   bool
	RootKind string
}

// GapBucket carries unattributable sampling bytes; these are never folded
// into any cell.
type GapBucket struct {
	Bytes int64
	Note  string
}

// UnresolvedRef keeps a reference the producer could not resolve, traceable
// back to the originating object and the raw token.
type UnresolvedRef struct {
	From     snapshot.ID
	Token    string
	Offset   int64
	Original string
	Count    int64
}

// Stats reports build counters.
type Stats struct {
	Objects int64
	Edges   int64
	Spilled bool
	BytesIn int64
}

// cellAcc holds exact accumulators for one cell.
type cellAcc struct {
	key      CellKey
	observed int64
	sampled  bool
	count    *weight.Acc
	bytes    *weight.Acc
}

// Index is an immutable, queryable snapshot index.
type Index struct {
	meta      snapshot.Meta
	symbols   *symbol.Table
	cells     []Cell
	cellByKey map[CellKey]int
	objs      map[snapshot.ID]ObjInfo
	byDense   []ObjInfo
	// edges in dense coordinates; RAM mode only. Spill mode reads them from
	// the external store via the retain pipeline.
	outEdges   [][]int64
	gaps       []GapBucket
	unresolved []UnresolvedRef
	pending    []strongRef
	dangling   []danglingRef
	stats      Stats
	ext        *externalIndex // nil in RAM mode
}

// Build drains rd into an immutable Index.
func Build(rd snapshot.Reader, opts Options) (*Index, Stats, error) {
	if opts.MemoryBudgetBytes > 0 {
		return buildExternal(rd, opts)
	}
	return buildRAM(rd, opts)
}

func buildRAM(rd snapshot.Reader, opts Options) (*Index, Stats, error) {
	ix := &Index{
		symbols:   symbol.New(),
		cellByKey: map[CellKey]int{},
		objs:      map[snapshot.ID]ObjInfo{},
	}
	typeRef := map[uint64]string{}
	siteRef := map[string]string{}
	accs := map[CellKey]*cellAcc{}
	edges := [][]int64{}
	denseOf := map[snapshot.ID]int64{}

	var metaSeen bool
	for {
		rec, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, Stats{}, err
		}
		switch rec.Kind {
		case snapshot.RecordMeta:
			if metaSeen {
				return nil, Stats{}, errors.New("index: duplicate meta")
			}
			metaSeen = true
			ix.meta = *rec.Meta
		case snapshot.RecordType:
			src := ix.meta.SnapshotID
			can := ix.symbols.RegisterType(src, *rec.Type)
			typeRef[rec.Type.ID] = can
		case snapshot.RecordSite:
			src := ix.meta.SnapshotID
			can := ix.symbols.RegisterSite(src, *rec.Site)
			siteRef[rec.Site.Key] = can
		case snapshot.RecordGap:
			ix.gaps = append(ix.gaps, GapBucket{Bytes: rec.Gap.Bytes, Note: rec.Gap.Note})
		case snapshot.RecordObject:
			o := rec.Object
			if _, dup := denseOf[o.ID]; dup {
				return nil, Stats{}, errDupObject(o.ID)
			}
			siteCan, ok := siteRef[o.SiteKey]
			if !ok {
				// site declared inline / unknown: register as missing-ish
				siteCan = ix.symbols.RegisterSite(ix.meta.SnapshotID, snapshot.AllocSite{
					Key: o.SiteKey, Symbol: false,
				})
				siteRef[o.SiteKey] = siteCan
			}
			typeCan, ok := typeRef[o.TypeID]
			if !ok {
				typeCan = ix.symbols.RegisterType(ix.meta.SnapshotID, snapshot.TypeInfo{
					ID: o.TypeID, Symbol: false,
				})
				typeRef[o.TypeID] = typeCan
			}
			w := weight.One()
			sampled := false
			if o.Sample != nil {
				if o.Sample.Weight != nil {
					w = *o.Sample.Weight
					if err := w.Validate(); err != nil {
						return nil, Stats{}, err
					}
					sampled = !w.IsOne()
				}
				if o.Sample.Gap && o.Sample.GapBytes != 0 {
					ix.gaps = append(ix.gaps, GapBucket{
						Bytes: o.Sample.GapBytes,
						Note:  "object-declared sampling gap",
					})
				}
			}
			dense := int64(len(ix.byDense))
			info := ObjInfo{ID: o.ID, Dense: dense, Site: siteCan, Type: typeCan,
				Size: o.Size, Weight: w, IsRoot: o.IsRoot, RootKind: o.RootKind}
			ix.byDense = append(ix.byDense, info)
			ix.objs[o.ID] = info
			denseOf[o.ID] = dense
			edges = append(edges, make([]int64, 0, len(o.Strong)))
			ix.stats.Objects++
			ix.stats.BytesIn += o.Size

			key := CellKey{Site: siteCan, Type: typeCan}
			acc, ok := accs[key]
			if !ok {
				acc = &cellAcc{key: key, count: weight.NewAcc(), bytes: weight.NewAcc()}
				accs[key] = acc
			}
			acc.observed++
			acc.sampled = acc.sampled || sampled
			acc.count.AddWeighted(w, 1)
			acc.bytes.AddWeighted(w, o.Size)

			for _, u := range o.Unres {
				ix.addUnresolvedLocked(o.ID, u)
			}
			// edges resolve in a second step (targets may arrive later);
			// stash producer-target pairs on the object for resolveEdges.
			ix.queueStrongEdges(dense, o.Strong)
		}
	}
	if !metaSeen {
		return nil, Stats{}, errors.New("index: missing meta")
	}
	ix.outEdges = edges
	ix.resolveEdgesRAM(denseOf)
	if err := ix.finalizeCells(accs); err != nil {
		return nil, Stats{}, err
	}
	ix.sortGaps()
	return ix, ix.stats, nil
}

// strongRef is a pending edge in producer coordinates.
type strongRef struct {
	fromDense int64
	to        snapshot.ID
}

// queueStrongEdges stashes strong edges for RAM resolution. Using a package
// slice is avoided; see buildRAM-local pending list instead.
func (ix *Index) queueStrongEdges(dense int64, es []snapshot.Edge) {
	for _, e := range es {
		if !e.Strong {
			continue
		}
		ix.pending = append(ix.pending, strongRef{fromDense: dense, to: e.To})
		ix.stats.Edges++
	}
}

func (ix *Index) resolveEdgesRAM(denseOf map[snapshot.ID]int64) {
	for _, p := range ix.pending {
		to, ok := denseOf[p.to]
		if !ok {
			// Target never declared: record as a dangling (unresolvable) ref.
			ix.dangling = append(ix.dangling, danglingRef{From: p.fromDense, To: p.to})
			continue
		}
		ix.outEdges[p.fromDense] = append(ix.outEdges[p.fromDense], to)
	}
	for _, es := range ix.outEdges {
		sort.Slice(es, func(i, j int) bool { return es[i] < es[j] })
	}
	ix.pending = nil
}

type danglingRef struct {
	From int64
	To   snapshot.ID
}

func (ix *Index) addUnresolvedLocked(from snapshot.ID, u snapshot.RawRef) {
	for i := range ix.unresolved {
		e := &ix.unresolved[i]
		if e.From == from && e.Token == u.Token && e.Offset == u.Offset && e.Original == u.Original {
			e.Count++
			return
		}
	}
	ix.unresolved = append(ix.unresolved, UnresolvedRef{
		From: from, Token: u.Token, Offset: u.Offset, Original: u.Original, Count: 1,
	})
}

func (ix *Index) finalizeCells(accs map[CellKey]*cellAcc) error {
	keys := make([]CellKey, 0, len(accs))
	for k := range accs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Site != keys[j].Site {
			return keys[i].Site < keys[j].Site
		}
		return keys[i].Type < keys[j].Type
	})
	ix.cells = make([]Cell, 0, len(keys))
	ix.cellByKey = make(map[CellKey]int, len(keys))
	for _, k := range keys {
		a := accs[k]
		cr := a.count.Rat()
		br := a.bytes.Rat()
		ce := Cell{
			Key: k, Observed: a.observed, Sampled: a.sampled,
			CountEst: a.count.RoundInt(), BytesEst: a.bytes.RoundInt(),
		}
		var err error
		if ce.CountExact, err = gobRat(cr); err != nil {
			return err
		}
		if ce.BytesExact, err = gobRat(br); err != nil {
			return err
		}
		ix.cellByKey[k] = len(ix.cells)
		ix.cells = append(ix.cells, ce)
	}
	return nil
}

func (ix *Index) sortGaps() {
	sort.Slice(ix.gaps, func(i, j int) bool {
		if ix.gaps[i].Note != ix.gaps[j].Note {
			return ix.gaps[i].Note < ix.gaps[j].Note
		}
		return ix.gaps[i].Bytes < ix.gaps[j].Bytes
	})
	sort.Slice(ix.unresolved, func(i, j int) bool {
		if ix.unresolved[i].From != ix.unresolved[j].From {
			return ix.unresolved[i].From < ix.unresolved[j].From
		}
		return ix.unresolved[i].Token < ix.unresolved[j].Token
	})
}

func errDupObject(id snapshot.ID) error {
	return errors.New("index: duplicate object id " + itoa(id))
}

func itoa(id snapshot.ID) string {
	const digits = "0123456789"
	n := uint64(id)
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = digits[n%10]
		n /= 10
	}
	return string(b[i:])
}

// SnapshotID returns the producer snapshot id.
func (ix *Index) SnapshotID() string { return ix.meta.SnapshotID }

// TakenAtMs returns the snapshot timestamp.
func (ix *Index) TakenAtMs() int64 { return ix.meta.TakenAtMs }

// Meta returns full metadata.
func (ix *Index) Meta() snapshot.Meta { return ix.meta }

// Cell returns one aggregated cell.
func (ix *Index) Cell(k CellKey) (Cell, bool) {
	i, ok := ix.cellByKey[k]
	if !ok {
		return Cell{}, false
	}
	return ix.cells[i], true
}

// Cells returns all cells in deterministic (site, type) order.
func (ix *Index) Cells() []Cell { return append([]Cell(nil), ix.cells...) }

// Object returns per-object info by producer ID.
func (ix *Index) Object(id snapshot.ID) (ObjInfo, bool) {
	if info, ok := ix.objs[id]; ok {
		return info, true
	}
	if ix.ext != nil {
		return ix.extLookup(id)
	}
	return ObjInfo{}, false
}

// ObjectByDense maps a dense node index back to object info.
func (ix *Index) ObjectByDense(d int64) (ObjInfo, bool) {
	if d < 0 || d >= int64(len(ix.byDense)) {
		if ix.ext != nil {
			return ix.extLookupDense(d)
		}
		return ObjInfo{}, false
	}
	return ix.byDense[d], true
}

// NObjects returns the observed object count.
func (ix *Index) NObjects() int64 { return ix.stats.Objects }

// Gaps returns unattributable sampling regions.
func (ix *Index) Gaps() []GapBucket { return append([]GapBucket(nil), ix.gaps...) }

// Unresolved returns aggregated unresolved references.
func (ix *Index) Unresolved() []UnresolvedRef {
	return append([]UnresolvedRef(nil), ix.unresolved...)
}

// EstimatedTotals returns rounded estimated count/bytes and observed bytes.
func (ix *Index) EstimatedTotals() (count, bytes, observedBytes int64) {
	for _, c := range ix.cells {
		count += c.CountEst
		bytes += c.BytesEst
	}
	for _, o := range ix.byDense {
		observedBytes += o.Size
	}
	return count, bytes, observedBytes
}

// Symbols exposes the canonical symbol table (used by reports/CLI).
func (ix *Index) Symbols() *symbol.Table { return ix.symbols }

// EdgesRAM returns strong outgoing edges in dense coordinates (RAM mode).
func (ix *Index) EdgesRAM() ([][]int64, bool) {
	if ix.ext != nil {
		return nil, false
	}
	return ix.outEdges, true
}

// DanglingRAM returns unresolved strong edges (RAM mode).
func (ix *Index) DanglingRAM() []DanglingEdge {
	out := make([]DanglingEdge, len(ix.dangling))
	for i, d := range ix.dangling {
		out[i] = DanglingEdge{From: d.From, To: d.To}
	}
	return out
}

// DanglingEdge is a strong edge whose target ID was never declared.
type DanglingEdge struct {
	From int64
	To   snapshot.ID
}
