package index

import (
	"encoding/binary"
	"io"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/snapshot"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/spill"
)

// extLookup returns object info by producer ID via a sequential scan.
func (ix *Index) extLookup(id snapshot.ID) (ObjInfo, bool) {
	var found ObjInfo
	ok := false
	_ = ix.ext.scanObjects(func(o ObjInfo) error {
		if o.ID == id {
			found, ok = o, true
			return io.EOF
		}
		return nil
	})
	return found, ok
}

// extLookupDense returns object info by dense index.
func (ix *Index) extLookupDense(d int64) (ObjInfo, bool) {
	var found ObjInfo
	ok := false
	_ = ix.ext.scanObjects(func(o ObjInfo) error {
		if o.Dense == d {
			found, ok = o, true
			return io.EOF
		}
		return nil
	})
	return found, ok
}

// extObjIter wraps a spill iterator of object rows sorted by (site,type,id).
type extObjIter struct {
	it spill.Iter
}

func (it *extObjIter) Next() (ObjInfo, error) {
	kv, err := it.it.Next()
	if err != nil {
		return ObjInfo{}, err
	}
	return decodeObjVal(kv.Val)
}

func (it *extObjIter) Close() error { return it.it.Close() }

func (ix *Index) extIterObjectsByCell() (ObjIter, error) {
	st := ix.ext.store
	sortIn := make(chan spill.KV)
	resCh := make(chan sortResult, 1)
	go func() {
		it, err := spill.SortMerge(st, sortIn)
		resCh <- sortResult{it: it, err: err}
	}()
	go func() {
		defer close(sortIn)
		var seq int64
		_ = ix.ext.scanObjects(func(o ObjInfo) error {
			seq++
			sortIn <- spill.KV{Key: objSortKey(o), Val: encodeObjVal(o), Seq: seq}
			return nil
		})
	}()
	res := <-resCh
	if res.err != nil {
		return nil, res.err
	}
	return &extObjIter{it: res.it}, nil
}

type sortResult struct {
	it  spill.Iter
	err error
}

// IterObjectsDense streams objects in dense order (retain pipeline).
func (ix *Index) IterObjectsDense(fn func(ObjInfo) error) error {
	return ix.ext.scanObjects(fn)
}

// IterEdges streams strong edges as (fromDense, toProducerID).
func (ix *Index) IterEdges(fn func(fromDense int64, to snapshot.ID) error) error {
	return ix.ext.scanEdges(fn)
}

// objSortKey encodes (site, type, producer id) in lexically sortable form.
func objSortKey(o ObjInfo) []byte {
	buf := make([]byte, 0, 4+len(o.Site)+len(o.Type)+8)
	putStr := func(s string) {
		var lb [binary.MaxVarintLen64]byte
		n := binary.PutUvarint(lb[:], uint64(len(s)))
		buf = append(buf, lb[:n]...)
		buf = append(buf, s...)
	}
	putStr(o.Site)
	putStr(o.Type)
	var idb [8]byte
	binary.BigEndian.PutUint64(idb[:], uint64(o.ID))
	return append(buf, idb[:]...)
}

// encodeObjVal is a self-contained object record for sorted iteration.
func encodeObjVal(o ObjInfo) []byte {
	buf := make([]byte, 0, 64)
	putVar := func(v int64) {
		var b [binary.MaxVarintLen64]byte
		n := binary.PutVarint(b[:], v)
		buf = append(buf, b[:n]...)
	}
	putUvar := func(v uint64) {
		var b [binary.MaxVarintLen64]byte
		n := binary.PutUvarint(b[:], v)
		buf = append(buf, b[:n]...)
	}
	putStr := func(s string) {
		putUvar(uint64(len(s)))
		buf = append(buf, s...)
	}
	putVar(o.Dense)
	putUvar(uint64(o.ID))
	putStr(o.Site)
	putStr(o.Type)
	putVar(o.Size)
	r := o.Weight.Big()
	putVar(r.Num().Int64())
	putVar(r.Denom().Int64())
	flag := byte(0)
	if o.IsRoot {
		flag = 1
	}
	buf = append(buf, flag)
	putStr(o.RootKind)
	return buf
}

func decodeObjVal(b []byte) (ObjInfo, error) {
	r := &valReader{b: b}
	dense, err := r.varint()
	if err != nil {
		return ObjInfo{}, err
	}
	id, err := r.uvarint()
	if err != nil {
		return ObjInfo{}, err
	}
	site, err := r.str()
	if err != nil {
		return ObjInfo{}, err
	}
	typ, err := r.str()
	if err != nil {
		return ObjInfo{}, err
	}
	size, err := r.varint()
	if err != nil {
		return ObjInfo{}, err
	}
	num, err := r.varint()
	if err != nil {
		return ObjInfo{}, err
	}
	den, err := r.varint()
	if err != nil {
		return ObjInfo{}, err
	}
	flag, err := r.byte()
	if err != nil {
		return ObjInfo{}, err
	}
	rootKind, err := r.str()
	if err != nil {
		return ObjInfo{}, err
	}
	w := weightOne(num, den)
	return ObjInfo{ID: snapshot.ID(id), Dense: dense, Site: site, Type: typ,
		Size: size, Weight: w, IsRoot: flag == 1, RootKind: rootKind}, nil
}

// ExternalBuild reports whether this index was built in bounded mode.
func (ix *Index) ExternalBuild() bool { return ix.ext != nil }

type valReader struct {
	b   []byte
	pos int
}

func (r *valReader) varint() (int64, error) { v, n := binary.Varint(r.b[r.pos:]); return r.adv(v, n) }
func (r *valReader) uvarint() (uint64, error) {
	v, n := binary.Uvarint(r.b[r.pos:])
	return r.advU(v, n)
}
func (r *valReader) byte() (byte, error) {
	if r.pos >= len(r.b) {
		return 0, io.ErrUnexpectedEOF
	}
	v := r.b[r.pos]
	r.pos++
	return v, nil
}
func (r *valReader) str() (string, error) {
	l, err := r.uvarint()
	if err != nil {
		return "", err
	}
	end := r.pos + int(l)
	if end > len(r.b) {
		return "", io.ErrUnexpectedEOF
	}
	s := string(r.b[r.pos:end])
	r.pos = end
	return s, nil
}
func (r *valReader) adv(v int64, n int) (int64, error) {
	if n <= 0 {
		return 0, io.ErrUnexpectedEOF
	}
	r.pos += n
	return v, nil
}
func (r *valReader) advU(v uint64, n int) (uint64, error) {
	if n <= 0 {
		return 0, io.ErrUnexpectedEOF
	}
	r.pos += n
	return v, nil
}

// extIterObjectsByID externally sorts object rows by producer ID only.
func (ix *Index) extIterObjectsByID() (ObjIter, error) {
	st := ix.ext.store
	sortIn := make(chan spill.KV)
	resCh := make(chan sortResult, 1)
	go func() {
		it, err := spill.SortMerge(st, sortIn)
		resCh <- sortResult{it: it, err: err}
	}()
	go func() {
		defer close(sortIn)
		var seq int64
		_ = ix.ext.scanObjects(func(o ObjInfo) error {
			seq++
			var k [8]byte
			binary.BigEndian.PutUint64(k[:], uint64(o.ID))
			sortIn <- spill.KV{Key: k[:], Val: encodeObjVal(o), Seq: seq}
			return nil
		})
	}()
	res := <-resCh
	if res.err != nil {
		return nil, res.err
	}
	return &extObjIter{it: res.it}, nil
}
