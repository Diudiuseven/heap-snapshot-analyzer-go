package index

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/snapshot"
	"github.com/Diudiuseven/heap-snapshot-analyzer-go/weight"
)

// On-disk object record (append-only, dense order):
//
//	dense uvarint | producerID uvarint | siteStr | typeStr | size varint |
//	weight num/den varint (den==1 => enumerated) | flags(1:root) | rootStr
//
// Strings are uvarint-length prefixed.
func (e *externalIndex) openWritable(name string) (*os.File, *bufio.Writer, error) {
	path := filepath.Join(e.store.Dir(), name)
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, err
	}
	switch name {
	case "objects.bin":
		e.objPath = path
	case "edges.bin":
		e.edgePath = path
	}
	return f, bufio.NewWriterSize(f, 1<<16), nil
}

func writeStr(w *bufio.Writer, s string) error {
	var lb [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lb[:], uint64(len(s)))
	if _, err := w.Write(lb[:n]); err != nil {
		return err
	}
	_, err := w.WriteString(s)
	return err
}

func writeVar(w *bufio.Writer, v int64) error {
	var b [binary.MaxVarintLen64]byte
	n := binary.PutVarint(b[:], v)
	_, err := w.Write(b[:n])
	return err
}

func (e *externalIndex) writeObject(dense int64, id snapshot.ID, site, typ string,
	size int64, wgt weight.Rat, root bool, rootKind string) error {
	_, bw, err := e.objWriter()
	if err != nil {
		return err
	}
	if err := writeVar(bw, dense); err != nil {
		return err
	}
	var b [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(b[:], uint64(id))
	if _, err := bw.Write(b[:n]); err != nil {
		return err
	}
	if err := writeStr(bw, site); err != nil {
		return err
	}
	if err := writeStr(bw, typ); err != nil {
		return err
	}
	if err := writeVar(bw, size); err != nil {
		return err
	}
	r := wgt.Big()
	if err := writeVar(bw, r.Num().Int64()); err != nil {
		return err
	}
	if err := writeVar(bw, r.Denom().Int64()); err != nil {
		return err
	}
	flag := byte(0)
	if root {
		flag = 1
	}
	if err := bw.WriteByte(flag); err != nil {
		return err
	}
	return writeStr(bw, rootKind)
}

func (e *externalIndex) writeEdge(fromDense int64, to snapshot.ID) error {
	_, bw, err := e.edgeWriter()
	if err != nil {
		return err
	}
	if err := writeVar(bw, fromDense); err != nil {
		return err
	}
	var b [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(b[:], uint64(to))
	_, err = bw.Write(b[:n])
	return err
}

// object/edge writers are opened lazily and flushed at close.
type bufFile struct {
	f  *os.File
	bw *bufio.Writer
}

func (e *externalIndex) objWriter() (*os.File, *bufio.Writer, error) {
	if e.objF == nil {
		f, bw, err := e.openWritable("objects.bin")
		if err != nil {
			return nil, nil, err
		}
		e.objF = &bufFile{f: f, bw: bw}
	}
	return e.objF.f, e.objF.bw, nil
}

func (e *externalIndex) edgeWriter() (*os.File, *bufio.Writer, error) {
	if e.edgeF == nil {
		f, bw, err := e.openWritable("edges.bin")
		if err != nil {
			return nil, nil, err
		}
		e.edgeF = &bufFile{f: f, bw: bw}
	}
	return e.edgeF.f, e.edgeF.bw, nil
}

// CloseWriters flushes append buffers exactly once. Safe for concurrent
// readers that each need the on-disk files finalized.
func (e *externalIndex) CloseWriters() error {
	e.closeOnce.Do(func() {
		e.closeErr = e.closeWritersLocked()
	})
	return e.closeErr
}

func (e *externalIndex) closeWritersLocked() error {
	for _, bf := range []*bufFile{e.objF, e.edgeF} {
		if bf != nil {
			if err := bf.bw.Flush(); err != nil {
				return err
			}
			if err := bf.f.Close(); err != nil {
				return err
			}
		}
	}
	e.objF, e.edgeF = nil, nil
	return nil
}

func readStr(br *bufio.Reader) (string, error) {
	l, err := binary.ReadUvarint(br)
	if err != nil {
		return "", err
	}
	buf := make([]byte, l)
	if _, err := io.ReadFull(br, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func readVar(br *bufio.Reader) (int64, error) { return binary.ReadVarint(br) }

// scanObjects opens the dense-order object file and calls fn per record.
func (e *externalIndex) scanObjects(fn func(ObjInfo) error) error {
	if err := e.CloseWriters(); err != nil {
		return err
	}
	f, err := os.Open(e.objPath)
	if err != nil {
		return err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 1<<16)
	for {
		dense, err := readVar(br)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		id, err := binary.ReadUvarint(br)
		if err != nil {
			return err
		}
		site, err := readStr(br)
		if err != nil {
			return err
		}
		typ, err := readStr(br)
		if err != nil {
			return err
		}
		size, err := readVar(br)
		if err != nil {
			return err
		}
		num, err := readVar(br)
		if err != nil {
			return err
		}
		den, err := readVar(br)
		if err != nil {
			return err
		}
		flag, err := br.ReadByte()
		if err != nil {
			return err
		}
		rootKind, err := readStr(br)
		if err != nil {
			return err
		}
		wgt := weight.One()
		if den != 1 || num != 1 {
			w, err := weight.New(num, den)
			if err != nil {
				return err
			}
			wgt = w
		}
		if err := fn(ObjInfo{
			ID: snapshot.ID(id), Dense: dense, Site: site, Type: typ,
			Size: size, Weight: wgt, IsRoot: flag == 1, RootKind: rootKind,
		}); err != nil {
			return err
		}
	}
}

// scanEdges calls fn for every strong edge (fromDense, toProducerID).
func (e *externalIndex) scanEdges(fn func(fromDense int64, to snapshot.ID) error) error {
	if e.edgePath == "" {
		return nil
	}
	f, err := os.Open(e.edgePath)
	if err != nil {
		return err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 1<<16)
	for {
		from, err := readVar(br)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		to, err := binary.ReadUvarint(br)
		if err != nil {
			return err
		}
		if err := fn(from, snapshot.ID(to)); err != nil {
			return err
		}
	}
}
