package store

import (
	"errors"
	"io"
)

var (
	eofErr           = io.EOF
	errAbortSentinel = errors.New("store: import aborted")
	errSessionDone   = errors.New("store: session already finished")
)

func errDupSnapshot(id string) error {
	return errors.New("store: duplicate snapshot id " + id)
}
