package snapshot

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/weight"
)

// JSONLReader decodes newline-delimited JSON snapshot records from an input
// stream. Decoding is streaming: at most one line/record is held in memory,
// so a snapshot may be arbitrarily larger than available RAM.
//
// Line format (one JSON object per line; blank lines and lines beginning
// with '#' are ignored):
//
//	{"t":"meta","snapshot_id":...,"taken_at_ms":...,"format":...,"attrs":{...}}
//	{"t":"type","id":1,"name":"[]byte","symbol":true}
//	{"t":"site","key":"...","symbol":true,"stack":[{"func":...}]}
//	{"t":"obj","id":10,"type":1,"site":"...","size":24,"root":"stack",
//	 "sample":{"w":[3,1],"gap":false,"gap_bytes":0},
//	 "edges":[{"to":11,"kind":"field","field":"x","strong":true}],
//	 "weak":[{"to":12,"kind":"weak"}],
//	 "unres":[{"token":"0x...","offset":128,"original":"..."}]}
//	{"t":"gap","bytes":4096,"note":"profiler skipped region"}
//
// Sample weight "w" is an exact [numerator, denominator] pair (inverse
// inclusion probability); omitted means enumerated (weight 1). A bare number
// is also accepted (integers exactly; decimals via exact float conversion).
type JSONLReader struct {
	br   *bufio.Reader
	line int64
	meta bool
}

// NewJSONLReader wraps r.
func NewJSONLReader(r io.Reader) *JSONLReader {
	return &JSONLReader{br: bufio.NewReaderSize(r, 1<<20)}
}

// Line returns the 1-based line number of the last returned record.
func (r *JSONLReader) Line() int64 { return r.line }

type wireSite struct {
	T      string  `json:"t"`
	Key    string  `json:"key"`
	Symbol bool    `json:"symbol"`
	Stack  []Frame `json:"stack"`
}

type wireType struct {
	T      string `json:"t"`
	ID     uint64 `json:"id"`
	Name   string `json:"name"`
	Symbol bool   `json:"symbol"`
}

type wireMeta struct {
	T          string            `json:"t"`
	SnapshotID string            `json:"snapshot_id"`
	TakenAtMs  int64             `json:"taken_at_ms"`
	Format     string            `json:"format"`
	Attrs      map[string]string `json:"attrs"`
}

type wireEdge struct {
	To     ID       `json:"to"`
	Kind   EdgeKind `json:"kind"`
	Field  string   `json:"field"`
	Index  int      `json:"index"`
	Strong bool     `json:"strong"`
}

type wireSample struct {
	W        json.RawMessage `json:"w"`
	Gap      bool            `json:"gap"`
	GapBytes int64           `json:"gap_bytes"`
}

type wireObj struct {
	T      string      `json:"t"`
	ID     ID          `json:"id"`
	Type   uint64      `json:"type"`
	Site   string      `json:"site"`
	Size   int64       `json:"size"`
	Root   string      `json:"root"` // non-empty marks a GC root
	Edges  []wireEdge  `json:"edges"`
	Weak   []wireEdge  `json:"weak"`
	Unres  []RawRef    `json:"unres"`
	Sample *wireSample `json:"sample"`
}

type wireGap struct {
	T     string `json:"t"`
	Bytes int64  `json:"bytes"`
	Note  string `json:"note"`
}

type wireHead struct {
	T string `json:"t"`
}

func parseWeight(raw json.RawMessage) (*weight.Rat, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	if raw[0] == '[' {
		var nd [2]int64
		if err := json.Unmarshal(raw, &nd); err != nil {
			return nil, fmt.Errorf("weight pair: %w", err)
		}
		w, err := weight.New(nd[0], nd[1])
		if err != nil {
			return nil, err
		}
		return &w, nil
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		w, err := weight.New(n, 1)
		if err != nil {
			return nil, err
		}
		return &w, nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("weight: %w", err)
	}
	r := new(big.Rat).SetFloat64(f)
	if r == nil || r.Cmp(big.NewRat(1, 1)) < 0 {
		return nil, fmt.Errorf("invalid weight %v", f)
	}
	w, err := weight.FromRat(r)
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// Next returns the next record, or io.EOF when the stream is exhausted.
func (r *JSONLReader) Next() (*Record, error) {
	var trimmed []byte
	for {
		line, err := r.br.ReadBytes('\n')
		if len(line) == 0 {
			return nil, err // includes io.EOF
		}
		trimmed = bytes.TrimSpace(line)
		if len(trimmed) > 0 && trimmed[0] != '#' {
			break
		}
		if err == io.EOF {
			return nil, io.EOF
		}
	}
	r.line++

	var head wireHead
	if jerr := json.Unmarshal(trimmed, &head); jerr != nil {
		return nil, fmt.Errorf("line %d: invalid record: %w", r.line, jerr)
	}
	rec := &Record{}
	switch head.T {
	case "meta":
		var m wireMeta
		if err := json.Unmarshal(trimmed, &m); err != nil {
			return nil, fmt.Errorf("line %d: meta: %w", r.line, err)
		}
		if r.meta {
			return nil, fmt.Errorf("line %d: duplicate meta", r.line)
		}
		r.meta = true
		rec.Kind = RecordMeta
		rec.Meta = &Meta{SnapshotID: m.SnapshotID, TakenAtMs: m.TakenAtMs,
			Format: m.Format, Attrs: m.Attrs}
	case "type":
		var ty wireType
		if err := json.Unmarshal(trimmed, &ty); err != nil {
			return nil, fmt.Errorf("line %d: type: %w", r.line, err)
		}
		rec.Kind = RecordType
		rec.Type = &TypeInfo{ID: ty.ID, Name: ty.Name, Symbol: ty.Symbol}
	case "site":
		var s wireSite
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return nil, fmt.Errorf("line %d: site: %w", r.line, err)
		}
		rec.Kind = RecordSite
		rec.Site = &AllocSite{Key: s.Key, Stack: s.Stack, Symbol: s.Symbol}
	case "gap":
		var g wireGap
		if err := json.Unmarshal(trimmed, &g); err != nil {
			return nil, fmt.Errorf("line %d: gap: %w", r.line, err)
		}
		rec.Kind = RecordGap
		rec.Gap = &GapRegion{Bytes: g.Bytes, Note: g.Note}
	case "obj", "object":
		var o wireObj
		if err := json.Unmarshal(trimmed, &o); err != nil {
			return nil, fmt.Errorf("line %d: obj: %w", r.line, err)
		}
		obj := &ObjectRecord{
			ID: o.ID, TypeID: o.Type, SiteKey: o.Site, Size: o.Size,
			IsRoot: o.Root != "", RootKind: o.Root,
		}
		for _, e := range o.Edges {
			obj.Strong = append(obj.Strong, Edge{To: e.To, Kind: e.Kind,
				Field: e.Field, Index: e.Index, Strong: e.Strong})
		}
		for _, e := range o.Weak {
			ek := e.Kind
			if ek == "" {
				ek = EdgeWeak
			}
			obj.Weak = append(obj.Weak, Edge{To: e.To, Kind: ek,
				Field: e.Field, Index: e.Index, Strong: false})
		}
		obj.Unres = append(obj.Unres, o.Unres...)
		if o.Sample != nil {
			w, werr := parseWeight(o.Sample.W)
			if werr != nil {
				return nil, fmt.Errorf("line %d: object %d: %w", r.line, o.ID, werr)
			}
			obj.Sample = &Sample{Weight: w, Gap: o.Sample.Gap,
				GapBytes: o.Sample.GapBytes}
		}
		rec.Kind = RecordObject
		rec.Object = obj
	default:
		return nil, fmt.Errorf("line %d: unknown record type %q", r.line, head.T)
	}
	return rec, nil
}
