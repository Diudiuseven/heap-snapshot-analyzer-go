package index

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/spill"
)

// ResolveEdgesBounded produces dense (from,to) edges with peak memory bounded
// by the index's memory budget, WITHOUT an in-memory producer->dense map.
//
// It externally sorts two streams by producer object id and merge-joins them:
//
//	objects : (producerID, denseID)        from the dense object file
//	edges   : (toProducerID, fromDense)    from the spilled edge file
//
// The join emits resolved edges as (fromDense,toDense). Edge targets without
// a matching object are skipped (they remain visible through Index.Unresolved
// / Dangling reporting in the snapshot pipeline).
//
// fn is called once per resolved strong edge.
func (ix *Index) ResolveEdgesBounded(fn func(fromDense, toDense int64) error) error {
	if ix.ext == nil {
		// RAM path: direct translation is already bounded by definition.
		edges, ok := ix.EdgesRAM()
		if !ok {
			return nil
		}
		for from, tos := range edges {
			for _, to := range tos {
				if err := fn(int64(from), to); err != nil {
					return err
				}
			}
		}
		return nil
	}
	st := ix.ext.store

	// Emit a combined, externally sorted stream of rows keyed by producerID.
	// Row layout key: [1][id 8]; value tag: obj => [0][dense 8], edge=>[1][from 8]
	in := make(chan spill.KV)
	sortRes := make(chan resultKV, 1)
	go func() {
		it, err := spill.SortMerge(st, in)
		sortRes <- resultKV{it: it, err: err}
	}()

	go func() {
		defer close(in)
		var seq int64
		// object rows
		_ = ix.ext.scanObjects(func(o ObjInfo) error {
			seq++
			k := make([]byte, 9)
			binary.BigEndian.PutUint64(k[:8], uint64(o.ID))
			k[8] = 0
			v := make([]byte, 9)
			v[0] = 0
			binary.BigEndian.PutUint64(v[1:], uint64(o.Dense))
			in <- spill.KV{Key: k, Val: v, Seq: seq}
			return nil
		})
		// edge rows
		path := filepath.Join(st.Dir(), "edges.bin")
		f, err := os.Open(path)
		if err != nil {
			return
		}
		defer f.Close()
		br := bufio.NewReaderSize(f, 1<<16)
		for {
			from, err := binary.ReadVarint(br)
			if err == io.EOF {
				break
			}
			if err != nil {
				return
			}
			to, err := binary.ReadUvarint(br)
			if err != nil {
				return
			}
			seq++
			k := make([]byte, 9)
			binary.BigEndian.PutUint64(k[:8], to)
			k[8] = 1 // edges sort after objects for the same producer id
			v := make([]byte, 9)
			v[0] = 1
			binary.BigEndian.PutUint64(v[1:], uint64(from))
			in <- spill.KV{Key: k, Val: v, Seq: seq}
		}
	}()

	res := <-sortRes
	if res.err != nil {
		return res.err
	}
	defer res.it.Close()

	// Group rows by producer id. For each group there is at most one object
	// row (tag 0, dense) followed by edge rows (tag 1, fromDense). Resolve
	// only when the group carries a dense id.
	peek, err := res.it.Next()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return err
	}
	for {
		curID := peek.Key[:8]
		var denseOf int64
		have := false
		var froms []int64
		for {
			if peek.Val[0] == 0 {
				denseOf = int64(binary.BigEndian.Uint64(peek.Val[1:]))
				have = true
			} else {
				froms = append(froms, int64(binary.BigEndian.Uint64(peek.Val[1:])))
			}
			nxt, err := res.it.Next()
			if err == io.EOF {
				peek = spill.KV{}
				if have {
					for _, fr := range froms {
						if err := fn(fr, denseOf); err != nil {
							return err
						}
					}
				}
				return nil
			}
			if err != nil {
				return err
			}
			if string(nxt.Key[:8]) != string(curID) {
				if have {
					for _, fr := range froms {
						if err := fn(fr, denseOf); err != nil {
							return err
						}
					}
				}
				peek = nxt
				break
			}
			peek = nxt
		}
	}
}
