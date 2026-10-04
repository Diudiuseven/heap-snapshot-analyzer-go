package snapshot

import (
	"errors"
	"fmt"
	"io"
)

// Builder is a simple in-memory materialization of a stream, intended for
// tests and small snapshots. Large inputs are streamed straight into the
// index/spill pipelines instead.
type Builder struct {
	Meta  *Meta
	Types map[uint64]*TypeInfo
	Sites map[string]*AllocSite
	Objs  []*ObjectRecord
	Gaps  []GapRegion
}

// NewBuilder returns an empty builder.
func NewBuilder() *Builder {
	return &Builder{Types: map[uint64]*TypeInfo{}, Sites: map[string]*AllocSite{}}
}

// Consume drains rd into the builder.
func (b *Builder) Consume(rd Reader) error {
	for {
		rec, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch rec.Kind {
		case RecordMeta:
			if b.Meta != nil {
				return errors.New("snapshot: duplicate meta")
			}
			b.Meta = rec.Meta
		case RecordType:
			if _, exists := b.Types[rec.Type.ID]; exists {
				return fmt.Errorf("snapshot: duplicate type id %d", rec.Type.ID)
			}
			b.Types[rec.Type.ID] = rec.Type
		case RecordSite:
			key := rec.Site.Key
			if _, exists := b.Sites[key]; exists {
				return fmt.Errorf("snapshot: duplicate site key %q", key)
			}
			b.Sites[key] = rec.Site
		case RecordObject:
			b.Objs = append(b.Objs, rec.Object)
		case RecordGap:
			b.Gaps = append(b.Gaps, *rec.Gap)
		default:
			return fmt.Errorf("snapshot: unexpected record kind %d", rec.Kind)
		}
	}
	if b.Meta == nil {
		return errors.New("snapshot: missing meta record")
	}
	return nil
}

// Build returns the assembled snapshot.
func (b *Builder) Build() *Snapshot {
	return &Snapshot{
		Meta:  b.Meta,
		Types: b.Types,
		Sites: b.Sites,
		Objs:  append([]*ObjectRecord(nil), b.Objs...),
		Gaps:  append([]GapRegion(nil), b.Gaps...),
	}
}

// Snapshot is a fully materialized heap snapshot.
type Snapshot struct {
	Meta  *Meta
	Types map[uint64]*TypeInfo
	Sites map[string]*AllocSite
	Objs  []*ObjectRecord
	Gaps  []GapRegion
}
