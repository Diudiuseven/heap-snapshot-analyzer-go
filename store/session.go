package store

import (
	"context"
	"sync"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/index"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/snapshot"
)

// liveSession is a streaming import: a background goroutine builds the index
// concurrently while Append feeds records through a bounded channel, so the
// producer never runs ahead of the consumer and memory stays bounded.
type liveSession struct {
	st   *Store
	opts index.Options

	ch     chan pipeItem
	done   chan buildOutcome
	mu     sync.Mutex
	closed bool
}

type buildOutcome struct {
	ix  *index.Index
	err error
}

func beginSession(_ context.Context, st *Store, opts index.Options) *liveSession {
	s := &liveSession{
		st:   st,
		opts: opts,
		ch:   make(chan pipeItem, 128),
		done: make(chan buildOutcome, 1),
	}
	go s.run()
	return s
}

func (s *liveSession) run() {
	ix, _, err := index.Build(&chanReader{ch: s.ch}, s.opts)
	s.done <- buildOutcome{ix: ix, err: err}
}

func (s *liveSession) append(rd snapshot.Reader) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errSessionDone
	}
	for {
		rec, err := rd.Next()
		if err != nil {
			if isEOF(err) {
				return nil
			}
			return err
		}
		select {
		case s.ch <- pipeItem{rec: rec}:
		case o := <-s.done:
			// builder already terminated (e.g. fatal parse/abort)
			s.closed = true
			if o.err != nil {
				return o.err
			}
			return errSessionDone
		}
	}
}

func (s *liveSession) commit() (*index.Index, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errSessionDone
	}
	s.closed = true
	s.mu.Unlock()
	s.ch <- pipeItem{err: eofErr}
	o := <-s.done
	if o.err != nil {
		return nil, o.err
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	if _, exists := s.st.byID[o.ix.SnapshotID()]; exists {
		return nil, errDupSnapshot(o.ix.SnapshotID())
	}
	s.st.commits = append(s.st.commits, o.ix)
	s.st.byID[o.ix.SnapshotID()] = o.ix
	return o.ix, nil
}

func (s *liveSession) abort() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.ch <- pipeItem{err: errAbortSentinel}
	o := <-s.done
	_ = o // rolled back; never published
	return nil
}
