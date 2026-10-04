package store

import (
	"errors"
	"io"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/snapshot"
)

type pipeItem struct {
	rec *snapshot.Record
	err error
}

// chanReader adapts the session channel to snapshot.Reader.
type chanReader struct{ ch <-chan pipeItem }

func (c *chanReader) Next() (*snapshot.Record, error) {
	item := <-c.ch
	if item.err != nil {
		if errors.Is(item.err, errAbortSentinel) {
			return nil, io.EOF // builder unwinds; session discards the result
		}
		return nil, item.err
	}
	return item.rec, nil
}

func isEOF(err error) bool { return errors.Is(err, io.EOF) }
