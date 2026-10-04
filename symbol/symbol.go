// Package symbol merges allocation-site and type symbolic information coming
// from multiple snapshots and multiple symbol sources.
//
// Consistency rules:
//
//   - Sites with identical producer keys are identical sites. When no key is
//     present the analyzer derives one from the full stack-frame fingerprint.
//   - Missing symbols (Symbol==false, or no key and no frames) are NEVER
//     silently merged with a real site. A missing site with a producer key
//     keeps that stable identity (flagged Missing) so its time series stays
//     trackable; a missing site with no key at all cannot be proven equal to
//     anything, so each registration gets a distinct "missing:<n>" id and is
//     never merged with another one.
//   - When two sources disagree about a key (different frames / type name),
//     both variants are retained and a Conflict is recorded instead of one
//     silently overwriting the other.
package symbol

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Diudiuseven/heap-snapshot-analyzer-go/snapshot"
)

// Site is a canonical allocation site.
type Site struct {
	Canonical string
	Key       string
	Missing   bool
	Frames    []snapshot.Frame
	Conflicts []snapshot.AllocSite
	Sources   map[string]bool
}

// Type is a canonical type.
type Type struct {
	Canonical string
	Name      string
	Missing   bool
	Sources   map[string]bool
}

// Table is a mergeable symbol table. Use New to construct.
type Table struct {
	sites      map[string]*Site
	types      map[string]*Type
	missingSeq int
	typeSeq    int
}

// New returns an empty table.
func New() *Table {
	return &Table{sites: map[string]*Site{}, types: map[string]*Type{}}
}

func framesEqual(a, b []snapshot.Frame) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// RegisterSite interns site and returns its canonical id. source identifies
// the symbol source (snapshot id or source name).
func (t *Table) RegisterSite(source string, site snapshot.AllocSite) string {
	key := site.Key
	if key == "" {
		key = StableSiteKey(site)
	}
	missing := !site.Symbol && len(site.Stack) == 0

	if key == "" {
		// Unidentifiable site: do not merge, mint a distinct identity.
		t.missingSeq++
		id := fmt.Sprintf("missing:%d", t.missingSeq)
		t.sites[id] = &Site{
			Canonical: id, Key: "", Missing: true,
			Sources: map[string]bool{source: true},
		}
		return id
	}

	id := "s:" + key
	existing, ok := t.sites[id]
	if !ok {
		frames := append([]snapshot.Frame(nil), site.Stack...)
		t.sites[id] = &Site{
			Canonical: id, Key: key, Missing: missing, Frames: frames,
			Sources: map[string]bool{source: true},
		}
		return id
	}
	existing.Sources[source] = true
	if missing {
		// Do not erase previously known symbols from another source.
	} else {
		if existing.Missing {
			existing.Missing = false
			if len(existing.Frames) == 0 {
				existing.Frames = append([]snapshot.Frame(nil), site.Stack...)
			}
		} else if len(site.Stack) > 0 && !framesEqual(existing.Frames, site.Stack) {
			existing.Conflicts = append(existing.Conflicts, site)
		}
	}
	return id
}

// RegisterType interns a type by name. Empty names are kept as distinct
// missing types rather than merged.
func (t *Table) RegisterType(source string, ty snapshot.TypeInfo) string {
	if ty.Name == "" || !ty.Symbol {
		t.typeSeq++
		id := fmt.Sprintf("tmissing:%d", t.typeSeq)
		t.types[id] = &Type{Canonical: id, Missing: true,
			Sources: map[string]bool{source: true}}
		return id
	}
	id := "t:" + ty.Name
	existing, ok := t.types[id]
	if !ok {
		t.types[id] = &Type{Canonical: id, Name: ty.Name,
			Sources: map[string]bool{source: true}}
	} else {
		existing.Sources[source] = true
	}
	return id
}

// Site looks up a canonical site.
func (t *Table) Site(canonical string) (Site, bool) {
	s, ok := t.sites[canonical]
	if !ok {
		return Site{}, false
	}
	return *s, true
}

// Type looks up a canonical type.
func (t *Table) Type(canonical string) (Type, bool) {
	ty, ok := t.types[canonical]
	if !ok {
		return Type{}, false
	}
	return *ty, true
}

// MissingSites returns canonical ids of unresolved sites in stable order.
func (t *Table) MissingSites() []string {
	var out []string
	for id, s := range t.sites {
		if s.Missing {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// Sites returns all sites ordered by canonical id.
func (t *Table) Sites() []Site {
	ids := make([]string, 0, len(t.sites))
	for id := range t.sites {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Site, 0, len(ids))
	for _, id := range ids {
		out = append(out, *t.sites[id])
	}
	return out
}

// Types returns all types ordered by canonical id.
func (t *Table) Types() []Type {
	ids := make([]string, 0, len(t.types))
	for id := range t.types {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Type, 0, len(ids))
	for _, id := range ids {
		out = append(out, *t.types[id])
	}
	return out
}

// Merge folds other into t and reports conflicts discovered during merge.
// It never mutates other.
func (t *Table) Merge(other *Table) []Conflict {
	var conflicts []Conflict
	for _, s := range other.Sites() {
		existing, ok := t.sites[s.Canonical]
		if !ok {
			cp := s
			cp.Sources = cloneSources(s.Sources)
			cp.Frames = append([]snapshot.Frame(nil), s.Frames...)
			cp.Conflicts = append([]snapshot.AllocSite(nil), s.Conflicts...)
			t.sites[s.Canonical] = &cp
			continue
		}
		for src := range s.Sources {
			existing.Sources[src] = true
		}
		if len(s.Conflicts) > 0 {
			existing.Conflicts = append(existing.Conflicts, s.Conflicts...)
			conflicts = append(conflicts, Conflict{
				Kind: "site", Key: s.Key,
				Sources: sourcesList(s.Sources),
			})
		}
		if !s.Missing && existing.Missing {
			existing.Missing = false
			existing.Frames = append([]snapshot.Frame(nil), s.Frames...)
		} else if !s.Missing && !existing.Missing &&
			len(s.Frames) > 0 && !framesEqual(existing.Frames, s.Frames) {
			existing.Conflicts = append(existing.Conflicts, snapshot.AllocSite{
				Key: s.Key, Stack: s.Frames,
			})
			conflicts = append(conflicts, Conflict{Kind: "site", Key: s.Key})
		}
	}
	for _, ty := range other.Types() {
		existing, ok := t.types[ty.Canonical]
		if !ok {
			cp := ty
			cp.Sources = cloneSources(ty.Sources)
			t.types[ty.Canonical] = &cp
			continue
		}
		for src := range ty.Sources {
			existing.Sources[src] = true
		}
		if !ty.Missing && existing.Name != ty.Name {
			conflicts = append(conflicts, Conflict{Kind: "type", Key: ty.Name})
		}
	}
	sort.Slice(conflicts, func(i, j int) bool {
		if conflicts[i].Kind != conflicts[j].Kind {
			return conflicts[i].Kind < conflicts[j].Kind
		}
		return conflicts[i].Key < conflicts[j].Key
	})
	return conflicts
}

func cloneSources(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func sourcesList(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Conflict describes symbol sources disagreeing about the same key.
type Conflict struct {
	Kind     string // "site" or "type"
	Key      string
	Sources  []string
	Variants []string
}

// StableSiteKey returns the deterministic fingerprint used when a producer
// supplies no site key. The empty fingerprint is "" so callers can detect
// unidentifiable sites.
func StableSiteKey(s snapshot.AllocSite) string {
	if len(s.Stack) == 0 {
		return ""
	}
	var b strings.Builder
	for i, f := range s.Stack {
		if i > 0 {
			b.WriteByte(0)
		}
		fmt.Fprintf(&b, "%s\x01%s\x01%d", f.Func, f.File, f.Line)
	}
	return b.String()
}
