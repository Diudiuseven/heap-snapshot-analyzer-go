package snapshot

import (
	"bufio"
	"encoding/json"
	"io"
)

// JSONLWriter writes snapshot records in the JSONL format JSONLReader reads.
// It exists so an in-process producer can emit a snapshot stream without
// touching the filesystem (e.g. a runtime/metrics-driven collector).
type JSONLWriter struct {
	w *bufio.Writer
}

// NewJSONLWriter wraps w.
func NewJSONLWriter(w io.Writer) *JSONLWriter {
	return &JSONLWriter{w: bufio.NewWriterSize(w, 1<<20)}
}

func (j *JSONLWriter) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := j.w.Write(b); err != nil {
		return err
	}
	return j.w.WriteByte('\n')
}

// WriteMeta emits a meta record.
func (j *JSONLWriter) WriteMeta(m Meta) error {
	return j.write(struct {
		T string `json:"t"`
		Meta
	}{"meta", m})
}

// WriteType emits a type record.
func (j *JSONLWriter) WriteType(ty TypeInfo) error {
	return j.write(struct {
		T string `json:"t"`
		TypeInfo
	}{"type", ty})
}

// WriteSite emits a site record.
func (j *JSONLWriter) WriteSite(s AllocSite) error {
	return j.write(struct {
		T string `json:"t"`
		AllocSite
	}{"site", s})
}

// WriteGap emits an unattributable sampling region.
func (j *JSONLWriter) WriteGap(g GapRegion) error {
	return j.write(struct {
		T string `json:"t"`
		GapRegion
	}{"gap", g})
}

// WriteObject emits an object record including weights, edges and unresolved
// references.
func (j *JSONLWriter) WriteObject(o ObjectRecord) error {
	type wEdge struct {
		To     ID       `json:"to"`
		Kind   EdgeKind `json:"kind,omitempty"`
		Field  string   `json:"field,omitempty"`
		Index  int      `json:"index,omitempty"`
		Strong bool     `json:"strong,omitempty"`
	}
	wo := struct {
		T      string      `json:"t"`
		ID     ID          `json:"id"`
		Type   uint64      `json:"type"`
		Site   string      `json:"site,omitempty"`
		Size   int64       `json:"size"`
		Root   string      `json:"root,omitempty"`
		Sample interface{} `json:"sample,omitempty"`
		Edges  []wEdge     `json:"edges,omitempty"`
		Weak   []wEdge     `json:"weak,omitempty"`
		Unres  []RawRef    `json:"unres,omitempty"`
	}{T: "obj", ID: o.ID, Type: o.TypeID, Site: o.SiteKey, Size: o.Size,
		Root: o.RootKind, Unres: o.Unres}
	if o.Sample != nil {
		sw := struct {
			W        []int64 `json:"w,omitempty"`
			Gap      bool    `json:"gap,omitempty"`
			GapBytes int64   `json:"gap_bytes,omitempty"`
		}{Gap: o.Sample.Gap, GapBytes: o.Sample.GapBytes}
		if o.Sample.Weight != nil {
			r := o.Sample.Weight.Big()
			num := r.Num().Int64()
			den := r.Denom().Int64()
			sw.W = []int64{num, den}
		}
		wo.Sample = sw
	}
	for _, e := range o.Strong {
		wo.Edges = append(wo.Edges, wEdge{e.To, e.Kind, e.Field, e.Index, e.Strong})
	}
	for _, e := range o.Weak {
		wo.Weak = append(wo.Weak, wEdge{e.To, e.Kind, e.Field, e.Index, false})
	}
	return j.write(wo)
}

// Flush flushes buffered data.
func (j *JSONLWriter) Flush() error { return j.w.Flush() }
