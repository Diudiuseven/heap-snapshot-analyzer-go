// Package snapshot defines the heap snapshot data model and the streaming
// record format consumed by every analyzer pipeline.
//
// The on-the-wire format is newline-delimited JSON (JSONL). A single logical
// snapshot is a sequence of JSON objects, one per line, each carrying a "t"
// discriminator. See docs/format.md for the normative specification.
package snapshot

import "github.com/Diudiuseven/heap-snapshot-analyzer-go/weight"

// ID is a snapshot-unique, 64-bit identifier of an object. IDs are taken
// verbatim from the producer; the analyzer never assumes them to be dense.
type ID uint64

// EdgeKind describes the referential semantics of an Edge.
type EdgeKind string

const (
	EdgeField EdgeKind = "field"
	EdgeArray EdgeKind = "array"
	EdgeWeak  EdgeKind = "weak" // weak reference: does not retain its target
	EdgeRoot  EdgeKind = "root" // synthetic edge emitted from a snapshot root
)

// Frame is one resolved stack frame of an allocation trace.
type Frame struct {
	Func string `json:"func,omitempty"`
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
}

// AllocSite identifies a program allocation point.
//
// Key is the producer-assigned canonical key (e.g. trace hash). When Key is
// empty the analyzer derives a canonical key from the stack frames. Symbolic
// information missing from the producer is retained as missing rather than
// silently merged with anything else.
type AllocSite struct {
	Key    string  `json:"key,omitempty"`
	Stack  []Frame `json:"stack,omitempty"`
	Symbol bool    `json:"symbol,omitempty"` // true when symbolic info is present
}

// TypeInfo describes an object type.
type TypeInfo struct {
	ID     uint64 `json:"id"`
	Name   string `json:"name"`
	Symbol bool   `json:"symbol,omitempty"`
}

// Edge is one reference of an object.
type Edge struct {
	To     ID       `json:"to"`
	Kind   EdgeKind `json:"kind"`
	Field  string   `json:"field,omitempty"`
	Index  int      `json:"index,omitempty"`
	Strong bool     `json:"strong"`
}

// Sample carries the sampling provenance of an observed object.
//
// A nil pointer means the object was enumerated (not sampled) and therefore
// has weight 1. For sampled objects, Weight is the inverse inclusion
// probability (>= 1). Gap/GapBytes describe a sampling interval in which the
// producer could not attribute bytes to any allocation site; such bytes are
// always reported separately and never folded into another entry.
type Sample struct {
	Weight *weight.Rat `json:"-"`
	Gap    bool        `json:"gap,omitempty"`

	GapBytes int64 `json:"gap_bytes,omitempty"`
}

// RawRef is a reference the producer could not resolve into an object ID.
// It is retained verbatim so results can be traced back to the raw snapshot.
type RawRef struct {
	Token    string `json:"token,omitempty"`
	Offset   int64  `json:"offset,omitempty"`
	Original string `json:"original,omitempty"`
}

// ObjectRecord is one observed heap object.
type ObjectRecord struct {
	ID       ID
	TypeID   uint64
	SiteKey  string
	Size     int64
	Strong   []Edge
	Weak     []Edge
	Unres    []RawRef
	Sample   *Sample
	IsRoot   bool
	RootKind string
}

// Meta carries snapshot-level metadata. It must be the first record.
type Meta struct {
	SnapshotID string            `json:"snapshot_id"`
	TakenAtMs  int64             `json:"taken_at_ms"`
	Format     string            `json:"format"`
	Attrs      map[string]string `json:"attrs,omitempty"`
}

// RecordKind discriminates records in the streaming format.
type RecordKind uint8

const (
	RecordUnknown RecordKind = iota
	RecordMeta
	RecordType
	RecordSite
	RecordObject
	RecordGap
	RecordEOF
)

// GapRegion describes a sampling interval whose bytes could not be attributed
// to an allocation site. It is carried as its own record rather than folded
// into an observed object.
type GapRegion struct {
	Bytes int64
	Note  string
}

// Record is one item of the streaming format. Exactly one payload field is
// non-nil; Kind reports which one.
type Record struct {
	Kind   RecordKind
	Meta   *Meta
	Type   *TypeInfo
	Site   *AllocSite
	Object *ObjectRecord
	Gap    *GapRegion
}

// Reader streams a snapshot as a sequence of records.
//
// Implementations must be safe for a single goroutine; callers that need
// parallel fan-out wrap a Reader themselves.
type Reader interface {
	Next() (*Record, error) // returns io.EOF when fully consumed
}
