// Package parse reads heap snapshots from files or in-process streams.
//
// Wire format: newline-delimited JSON records, one per line:
//
//	{"kind":"meta",   "id":"...", "timestamp":N, "source":"..."}
//	{"kind":"site",   "id":N, "frames":[{"func":"...","file":"...","line":N}]}
//	{"kind":"type",   "id":N, "name":"..."}
//	{"kind":"object", "id":N, "size":N, "type":N, "site":N, "scaleNum":N, "scaleDen":N}
//	{"kind":"ref",    "from":N, "to":N}
//
// Records may appear in any order. The reader is streaming: it holds
// only one record in memory, so arbitrarily large snapshots can be
// consumed without loading them whole.
package parse

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/pkg/model"
)

// Record is any parsed record.
type Record interface{ recordKind() string }

// MetaRecord carries snapshot metadata.
type MetaRecord struct{ Meta model.Meta }

// SiteRecord carries one allocation site.
type SiteRecord struct{ Site model.AllocSite }

// TypeRecord carries one type.
type TypeRecord struct{ Type model.TypeInfo }

// ObjectRecord carries one object.
type ObjectRecord struct{ Object model.Object }

// RefRecord carries one reference edge.
type RefRecord struct{ Ref model.Reference }

func (MetaRecord) recordKind() string   { return "meta" }
func (SiteRecord) recordKind() string   { return "site" }
func (TypeRecord) recordKind() string   { return "type" }
func (ObjectRecord) recordKind() string { return "object" }
func (RefRecord) recordKind() string    { return "ref" }

// wireRecord is the on-the-wire envelope.
type wireRecord struct {
	Kind      string       `json:"kind"`
	ID        string       `json:"id,omitempty"`
	Timestamp int64        `json:"timestamp,omitempty"`
	Source    string       `json:"source,omitempty"`
	Frames    []wireFrame  `json:"frames,omitempty"`
	Name      string       `json:"name,omitempty"`
	Size      int64        `json:"size,omitempty"`
	Type      model.TypeID `json:"type,omitempty"`
	Site      model.SiteID `json:"site,omitempty"`
	ScaleNum  int64        `json:"scaleNum,omitempty"`
	ScaleDen  int64        `json:"scaleDen,omitempty"`
	From      uint64       `json:"from,omitempty"`
	To        uint64       `json:"to,omitempty"`
	NumID     uint64       `json:"numID,omitempty"` // numeric id for site/type/object
}

type wireFrame struct {
	Func string `json:"func"`
	File string `json:"file"`
	Line int    `json:"line"`
}

// Reader streams records from an io.Reader.
type Reader struct {
	sc  *bufio.Scanner
	err error
}

// NewReader creates a streaming record reader.
func NewReader(r io.Reader) *Reader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	return &Reader{sc: sc}
}

// Next returns the next record, or io.EOF when exhausted.
func (r *Reader) Next() (Record, error) {
	if r.err != nil {
		return nil, r.err
	}
	for r.sc.Scan() {
		line := r.sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var w wireRecord
		if err := json.Unmarshal(line, &w); err != nil {
			r.err = fmt.Errorf("parse: bad record: %w", err)
			return nil, r.err
		}
		rec, err := convert(w)
		if err != nil {
			r.err = err
			return nil, err
		}
		return rec, nil
	}
	if err := r.sc.Err(); err != nil {
		r.err = err
		return nil, err
	}
	r.err = io.EOF
	return nil, io.EOF
}

func convert(w wireRecord) (Record, error) {
	switch w.Kind {
	case "meta":
		return MetaRecord{Meta: model.Meta{ID: w.ID, Timestamp: w.Timestamp, Source: w.Source}}, nil
	case "site":
		frames := make([]model.Frame, len(w.Frames))
		for i, f := range w.Frames {
			frames[i] = model.Frame{Func: f.Func, File: f.File, Line: f.Line}
		}
		return SiteRecord{Site: model.AllocSite{ID: model.SiteID(w.NumID), Frames: frames}}, nil
	case "type":
		return TypeRecord{Type: model.TypeInfo{ID: model.TypeID(w.NumID), Name: w.Name}}, nil
	case "object":
		return ObjectRecord{Object: model.Object{
			ID:       model.ObjectID(w.NumID),
			Size:     w.Size,
			Type:     w.Type,
			Site:     w.Site,
			ScaleNum: w.ScaleNum,
			ScaleDen: w.ScaleDen,
		}}, nil
	case "ref":
		return RefRecord{Ref: model.Reference{From: model.ObjectID(w.From), To: model.ObjectID(w.To)}}, nil
	default:
		return nil, fmt.Errorf("parse: unknown record kind %q", w.Kind)
	}
}

// ReadAll reads a complete snapshot into memory.
func ReadAll(r io.Reader) (*model.Snapshot, error) {
	s := &model.Snapshot{}
	rd := NewReader(r)
	for {
		rec, err := rd.Next()
		if err == io.EOF {
			return s, nil
		}
		if err != nil {
			return nil, err
		}
		switch v := rec.(type) {
		case MetaRecord:
			s.Meta = v.Meta
		case SiteRecord:
			s.Sites = append(s.Sites, v.Site)
		case TypeRecord:
			s.Types = append(s.Types, v.Type)
		case ObjectRecord:
			s.Objects = append(s.Objects, v.Object)
		case RefRecord:
			s.Refs = append(s.Refs, v.Ref)
		}
	}
}
