package store

import (
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/index"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/retain"
)

// buildExternalGraph constructs a paged retain.Graph from a bounded index
// using fully streaming, bounded-memory primitives:
//
//   - node metadata comes from the dense-order object file (one object at a
//     time);
//   - edges are translated producer->dense via an external sort/merge
//     (index.ResolveEdgesBounded), so there is no O(n) id map in RAM.
func buildExternalGraph(ix *index.Index, opts retain.Options) (retain.Graph, error) {
	n := ix.NObjects()

	nodes := make(chan retain.NodeMeta, 256)
	edges := make(chan retain.Edge, 256)
	errCh := make(chan error, 2)

	go func() {
		defer close(nodes)
		errCh <- ix.IterObjectsDense(func(o index.ObjInfo) error {
			nodes <- retain.NodeMeta{
				Dense: o.Dense, Size: weightedSize(o), Site: o.Site, Root: o.IsRoot,
			}
			return nil
		})
	}()
	go func() {
		defer close(edges)
		errCh <- ix.ResolveEdgesBounded(func(fromDense, toDense int64) error {
			edges <- retain.Edge{From: fromDense, To: toDense}
			return nil
		})
	}()

	g, err := retain.BuildStreaming(n, opts, nodes, edges)
	// drain possible errors; BuildStreaming returns the decisive error first.
	<-errCh
	<-errCh
	return g, err
}
