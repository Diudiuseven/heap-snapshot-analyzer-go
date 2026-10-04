// Package model defines the core data model for heap snapshots.
package model

// ObjectID uniquely identifies an object within a single snapshot.
type ObjectID uint64

// SiteID identifies an allocation site (a unique call stack).
// SiteID 0 is reserved for the "unknown/unattributed" site.
type SiteID uint32

// TypeID identifies a type within a snapshot's type table.
// TypeID 0 is reserved for the "unknown" type.
type TypeID uint32

// UnknownSite is the reserved site for objects whose allocation
// site is missing or cannot be resolved.
const UnknownSite SiteID = 0

// UnknownType is the reserved type for objects whose type is missing.
const UnknownType TypeID = 0

// Frame is a single stack frame of an allocation site.
type Frame struct {
	Func string
	File string
	Line int
}

// AllocSite describes one allocation site as a stack of frames.
type AllocSite struct {
	ID     SiteID
	Frames []Frame
}

// TypeInfo describes a type.
type TypeInfo struct {
	ID   TypeID
	Name string
}

// Object is a single heap object record.
//
// Sampling: when the snapshot is sampled, ScaleNum/ScaleDen carry the
// per-record sampling scale (est = value * ScaleNum / ScaleDen).
// Unsampled records use ScaleNum=1, ScaleDen=1.
type Object struct {
	ID       ObjectID
	Size     int64
	Type     TypeID
	Site     SiteID
	ScaleNum int64
	ScaleDen int64
}

// Reference is a directed edge From -> To.
type Reference struct {
	From ObjectID
	To   ObjectID
}

// Meta carries snapshot-level metadata.
type Meta struct {
	ID        string
	Timestamp int64 // unix nanos
	Source    string
}

// Snapshot is the in-memory representation of one heap snapshot.
type Snapshot struct {
	Meta    Meta
	Objects []Object
	Refs    []Reference
	Sites   []AllocSite
	Types   []TypeInfo
}

// SiteByID returns the site table indexed by SiteID (index 0 = unknown).
func (s *Snapshot) SiteByID() map[SiteID]AllocSite {
	m := make(map[SiteID]AllocSite, len(s.Sites))
	for _, st := range s.Sites {
		m[st.ID] = st
	}
	return m
}

// TypeByID returns the type table indexed by TypeID (index 0 = unknown).
func (s *Snapshot) TypeByID() map[TypeID]TypeInfo {
	m := make(map[TypeID]TypeInfo, len(s.Types))
	for _, t := range s.Types {
		m[t.ID] = t
	}
	return m
}
