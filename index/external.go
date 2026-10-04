package index

import (
	"encoding/binary"
	"errors"
	"io"
	"sort"
	"sync"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/snapshot"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/spill"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/symbol"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/weight"
)

// externalIndex holds spill artifacts for a bounded-memory build.
type externalIndex struct {
	store     *spill.Store
	objPath   string // dense-order object records
	edgePath  string // strong edges: denseFrom,u64To pairs
	objF      *bufFile
	edgeF     *bufFile
	closeOnce sync.Once
	closeErr  error
}

// buildExternal streams a snapshot into spill files. Peak live memory is
// bounded by opts.MemoryBudgetBytes: overflow seals sorted chunks and the
// final cell aggregates are produced by a deterministic k-way merge of exact
// rational partial sums.
func buildExternal(rd snapshot.Reader, opts Options) (*Index, Stats, error) {
	budget := &spill.Budget{Bytes: opts.MemoryBudgetBytes}
	st, err := spill.NewStore(opts.SpillDir, budget)
	if err != nil {
		return nil, Stats{}, err
	}
	ext := &externalIndex{store: st}
	committed := false
	defer func() {
		if !committed {
			_ = ext.CloseWriters()
			_ = st.Cleanup()
		}
	}()

	ix := &Index{symbols: symbol.New(), cellByKey: map[CellKey]int{}, ext: ext}
	typeRef := map[uint64]string{}
	siteRef := map[string]string{}

	cellIn := make(chan spill.KV)
	sortDone := make(chan resultKV, 1)
	go func() {
		it, err := spill.SortMerge(st, cellIn)
		sortDone <- resultKV{it: it, err: err}
	}()

	var seq, nObjs, nEdges, bytesIn int64
	var metaSeen bool

	abort := func(e error) (*Index, Stats, error) {
		close(cellIn)
		<-sortDone // allow merger goroutine to wind down
		return nil, Stats{}, e
	}

	for {
		rec, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return abort(err)
		}
		switch rec.Kind {
		case snapshot.RecordMeta:
			if metaSeen {
				return abort(errors.New("index: duplicate meta"))
			}
			metaSeen = true
			ix.meta = *rec.Meta
		case snapshot.RecordType:
			can := ix.symbols.RegisterType(ix.meta.SnapshotID, *rec.Type)
			typeRef[rec.Type.ID] = can
		case snapshot.RecordSite:
			can := ix.symbols.RegisterSite(ix.meta.SnapshotID, *rec.Site)
			siteRef[rec.Site.Key] = can
		case snapshot.RecordGap:
			ix.gaps = append(ix.gaps, GapBucket{Bytes: rec.Gap.Bytes, Note: rec.Gap.Note})
		case snapshot.RecordObject:
			o := rec.Object
			dense := nObjs
			nObjs++
			bytesIn += o.Size
			siteCan := siteRef[o.SiteKey]
			if siteCan == "" {
				siteCan = ix.symbols.RegisterSite(ix.meta.SnapshotID, snapshot.AllocSite{
					Key: o.SiteKey, Symbol: false})
				siteRef[o.SiteKey] = siteCan
			}
			typeCan := typeRef[o.TypeID]
			if typeCan == "" {
				typeCan = ix.symbols.RegisterType(ix.meta.SnapshotID, snapshot.TypeInfo{
					ID: o.TypeID, Symbol: false})
				typeRef[o.TypeID] = typeCan
			}
			w := weight.One()
			sampled := false
			if o.Sample != nil && o.Sample.Weight != nil {
				if err := o.Sample.Weight.Validate(); err != nil {
					return abort(err)
				}
				w = *o.Sample.Weight
				sampled = !w.IsOne()
				if o.Sample.Gap && o.Sample.GapBytes != 0 {
					ix.gaps = append(ix.gaps, GapBucket{Bytes: o.Sample.GapBytes,
						Note: "object-declared sampling gap"})
				}
			}
			for _, u := range o.Unres {
				ix.addUnresolvedLocked(o.ID, u)
			}
			seq++
			cellIn <- spill.KV{Key: encodeCellKey(siteCan, typeCan),
				Val: encodeCellVal(w, o.Size, sampled), Seq: seq}
			if err := ext.writeObject(dense, o.ID, siteCan, typeCan, o.Size, w,
				o.IsRoot, o.RootKind); err != nil {
				return abort(err)
			}
			for _, e := range o.Strong {
				if !e.Strong {
					continue
				}
				nEdges++
				if err := ext.writeEdge(dense, e.To); err != nil {
					return abort(err)
				}
			}
		}
	}
	close(cellIn)
	if !metaSeen {
		res := <-sortDone
		if res.it != nil {
			res.it.Close()
		}
		return nil, Stats{}, errors.New("index: missing meta")
	}
	res := <-sortDone
	if res.err != nil {
		return nil, Stats{}, res.err
	}
	defer res.it.Close()

	type merged struct {
		k    CellKey
		cell Cell
	}
	var cells []merged
	var curKey []byte
	cntAcc := weight.NewAcc()
	byAcc := weight.NewAcc()
	var observed int64
	var sampled bool
	flush := func() error {
		if curKey == nil {
			return nil
		}
		k, err := decodeCellKey(curKey)
		if err != nil {
			return err
		}
		cr, br := cntAcc.Rat(), byAcc.Rat()
		ce := Cell{Key: k, Observed: observed, Sampled: sampled,
			CountEst: cntAcc.RoundInt(), BytesEst: byAcc.RoundInt()}
		if ce.CountExact, err = gobRat(cr); err != nil {
			return err
		}
		if ce.BytesExact, err = gobRat(br); err != nil {
			return err
		}
		cells = append(cells, merged{k: k, cell: ce})
		return nil
	}
	for {
		kv, err := res.it.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, Stats{}, err
		}
		if curKey == nil || string(kv.Key) != string(curKey) {
			if err := flush(); err != nil {
				return nil, Stats{}, err
			}
			curKey = append(curKey[:0], kv.Key...)
			cntAcc, byAcc = weight.NewAcc(), weight.NewAcc()
			observed, sampled = 0, false
		}
		w, size, s, err := decodeCellVal(kv.Val)
		if err != nil {
			return nil, Stats{}, err
		}
		cntAcc.AddWeighted(w, 1)
		byAcc.AddWeighted(w, size)
		observed++
		sampled = sampled || s
	}
	if err := flush(); err != nil {
		return nil, Stats{}, err
	}
	sort.Slice(cells, func(i, j int) bool {
		if cells[i].k.Site != cells[j].k.Site {
			return cells[i].k.Site < cells[j].k.Site
		}
		return cells[i].k.Type < cells[j].k.Type
	})
	ix.cells = make([]Cell, len(cells))
	for i, m := range cells {
		ix.cells[i] = m.cell
		ix.cellByKey[m.k] = i
	}
	ix.stats = Stats{Objects: nObjs, Edges: nEdges, BytesIn: bytesIn, Spilled: true}
	ix.sortGaps()
	if err := ext.CloseWriters(); err != nil {
		return nil, Stats{}, err
	}
	committed = true
	return ix, ix.stats, nil
}

type resultKV struct {
	it  spill.Iter
	err error
}

// cell key: tag(1)+siteLen(2)+site+type
func encodeCellKey(site, typ string) []byte {
	buf := make([]byte, 0, 4+len(site)+len(typ))
	buf = append(buf, 1)
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(site)))
	buf = append(buf, l[:]...)
	buf = append(buf, site...)
	buf = append(buf, typ...)
	return buf
}

func decodeCellKey(b []byte) (CellKey, error) {
	if len(b) < 3 || b[0] != 1 {
		return CellKey{}, errors.New("index: bad cell key")
	}
	slen := int(binary.BigEndian.Uint16(b[1:3]))
	if 3+slen > len(b) {
		return CellKey{}, errors.New("index: truncated cell key")
	}
	return CellKey{Site: string(b[3 : 3+slen]), Type: string(b[3+slen:])}, nil
}

// cell val: num varint, den varint, size varint, flags(1)
func encodeCellVal(w weight.Rat, size int64, sampled bool) []byte {
	r := w.Big()
	buf := make([]byte, 0, binary.MaxVarintLen64*3+1)
	put := func(v int64) {
		var b [binary.MaxVarintLen64]byte
		n := binary.PutVarint(b[:], v)
		buf = append(buf, b[:n]...)
	}
	put(r.Num().Int64())
	put(r.Denom().Int64())
	put(size)
	f := byte(0)
	if sampled {
		f = 1
	}
	return append(buf, f)
}

func decodeCellVal(b []byte) (weight.Rat, int64, bool, error) {
	rd := newByteReader(b)
	num, err := rd.varint()
	if err != nil {
		return weight.One(), 0, false, err
	}
	den, err := rd.varint()
	if err != nil {
		return weight.One(), 0, false, err
	}
	size, err := rd.varint()
	if err != nil {
		return weight.One(), 0, false, err
	}
	flag := byte(0)
	if rd.left() > 0 {
		flag = b[rd.pos]
	}
	w, err := weight.New(num, den)
	if err != nil {
		return weight.One(), 0, false, err
	}
	return w, size, flag == 1, nil
}

type byteReader struct {
	b   []byte
	pos int
}

func newByteReader(b []byte) *byteReader { return &byteReader{b: b} }
func (r *byteReader) left() int          { return len(r.b) - r.pos }
func (r *byteReader) varint() (int64, error) {
	v, n := binary.Varint(r.b[r.pos:])
	if n <= 0 {
		return 0, io.ErrUnexpectedEOF
	}
	r.pos += n
	return v, nil
}

// Close releases spill artifacts of a bounded build. It is safe to call on
// RAM-mode indexes (no-op) and multiple times.
func (ix *Index) Close() error {
	if ix.ext == nil || ix.ext.store == nil {
		return nil
	}
	st := ix.ext.store
	ix.ext = nil
	return st.Cleanup()
}
