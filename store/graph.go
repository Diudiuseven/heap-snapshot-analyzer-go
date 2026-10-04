package store

import (
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/index"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/retain"
)

// graphForIndex adapts a built index to the retain.Graph interface.
//
// RAM indexes use the slice-backed CSR. Bounded indexes build a paged
// external graph from spilled object/edge files; producer edge targets are
// translated to dense ids via an external sort, and unknown targets are
// reported as dangling instead of aborting.
func graphForIndex(ix *index.Index, opts retain.Options) (retain.Graph, error) {
	if edges, ok := ix.EdgesRAM(); ok {
		g := retain.NewRAMCSR(ix.NObjects())
		for dense := int64(0); dense < ix.NObjects(); dense++ {
			info, ok := ix.ObjectByDense(dense)
			if !ok {
				continue
			}
			g.SetSize(dense, weightedSize(info))
			g.SetSite(dense, info.Site)
			g.SetRoot(dense, info.IsRoot)
		}
		for from, tos := range edges {
			for _, to := range tos {
				g.AddEdge(int64(from), to)
			}
		}
		return g.Build()
	}
	return buildExternalGraph(ix, opts)
}

// weightedSize applies the exact sampling weight to shallow size and rounds
// once (the dominator pass works with integer estimated bytes).
func weightedSize(info index.ObjInfo) int64 {
	if info.Weight.IsOne() {
		return info.Size
	}
	return info.Weight.MulInt(info.Size).RoundInt()
}
