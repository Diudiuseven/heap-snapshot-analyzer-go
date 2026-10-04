package index

import (
	"io"
	"sort"
)

// ObjIter iterates object info.
type ObjIter interface {
	Next() (ObjInfo, error)
	Close() error
}

// ramObjIter walks a materialized slice in (site, type, id) order.
type ramObjIter struct {
	objs []ObjInfo
	pos  int
}

// IterObjectsByCell returns a sorted object iterator used by the diff pass.
// In spill mode it returns an external iterator (see external.go).
func (ix *Index) IterObjectsByCell() (ObjIter, error) {
	if ix.ext != nil {
		return ix.extIterObjectsByCell()
	}
	objs := append([]ObjInfo(nil), ix.byDense...)
	sort.Slice(objs, func(i, j int) bool {
		if objs[i].Site != objs[j].Site {
			return objs[i].Site < objs[j].Site
		}
		if objs[i].Type != objs[j].Type {
			return objs[i].Type < objs[j].Type
		}
		return objs[i].ID < objs[j].ID
	})
	return &ramObjIter{objs: objs}, nil
}

func (it *ramObjIter) Next() (ObjInfo, error) {
	if it.pos >= len(it.objs) {
		return ObjInfo{}, io.EOF
	}
	o := it.objs[it.pos]
	it.pos++
	return o, nil
}

func (it *ramObjIter) Close() error { return nil }

// IterObjectsByID returns objects ordered by producer ID. Diff uses it to
// merge-join two snapshots without materializing either side.
func (ix *Index) IterObjectsByID() (ObjIter, error) {
	if ix.ext != nil {
		return ix.extIterObjectsByID()
	}
	objs := append([]ObjInfo(nil), ix.byDense...)
	sort.Slice(objs, func(i, j int) bool { return objs[i].ID < objs[j].ID })
	return &ramObjIter{objs: objs}, nil
}
