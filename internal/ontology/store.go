// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ontology

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

// Prov says where a value came from and how it got here.
type Prov struct {
	Source     string    `json:"source"`
	SourceID   string    `json:"source_id,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
	IngestedAt time.Time `json:"ingested_at"`
	Transform  []string  `json:"transform,omitempty"`
}

// Value is one property value with its provenance.
type Value struct {
	V    any  `json:"v"`
	Prov Prov `json:"prov"`
}

// Change is one property taking a new value.
type Change struct {
	Object   string `json:"object"`
	Property string `json:"property"`
	Before   *Value `json:"before,omitempty"`
	After    Value  `json:"after"`
}

// maxHistory bounds the change log; the oldest entries are dropped first.
const maxHistory = 20000

// Alias is an identifier the object has in another system.
type Alias struct {
	System     string `json:"system"`
	ExternalID string `json:"external_id"`
}

// Object is a business object. Its ID is stable: type:namespace:key.
type Object struct {
	ID      string           `json:"id"`
	Type    string           `json:"type"`
	Tenant  string           `json:"tenant,omitempty"`
	Props   map[string]Value `json:"props"`
	Aliases []Alias          `json:"aliases,omitempty"`
}

// Link is a typed, directed edge between two objects.
type Link struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	From string `json:"from"`
	To   string `json:"to"`
	Prov Prov   `json:"prov"`
}

// MakeID builds a stable object ID.
func MakeID(typ, namespace, key string) string {
	return typ + ":" + namespace + ":" + key
}

// SplitID is the inverse of MakeID.
func SplitID(id string) (typ, namespace, key string, ok bool) {
	p := strings.SplitN(id, ":", 3)
	if len(p) != 3 || p[0] == "" || p[1] == "" || p[2] == "" {
		return "", "", "", false
	}
	return p[0], p[1], p[2], true
}

// LinkID is derived from the link's content so re-ingesting is idempotent.
func LinkID(typ, from, to string) string {
	h := sha256.Sum256([]byte(typ + "\x00" + from + "\x00" + to))
	return "link:" + hex.EncodeToString(h[:8])
}

type state struct {
	Version    int                  `json:"version"`
	Objects    map[string]Object    `json:"objects"`
	Links      map[string]Link      `json:"links"`
	Candidates map[string]Candidate `json:"candidates,omitempty"`
	// History records every property value change, oldest first.
	History []Change `json:"history,omitempty"`
	// Redirects maps the id of a merged-away object to the one it became.
	Redirects map[string]string `json:"redirects,omitempty"`
}

// Store holds objects and links. The zero value is not usable; call Open.
type Store struct {
	mu     sync.RWMutex
	path   string
	schema *Schema
	s      state
	// backend persists changes; nil keeps the store in memory.
	backend    Backend
	extHistory bool // the backend keeps the change log, not memory
	ix         index
	dirty      []op
	batch      int
	// Audit, when set, is told about every ingest and merge so the
	// decision audit chain covers ontology changes.
	Audit func(subject, by, note string)
}

// Open loads the store from path; an empty path keeps it in memory. A .db,
// .sqlite or .sqlite3 path selects the SQLite backend, any other the JSON file.
func Open(path string, schema *Schema) (*Store, error) {
	if path == "" {
		return OpenBackend(nil, schema)
	}
	be, err := NewBackend(path)
	if err != nil {
		return nil, err
	}
	return OpenBackend(be, schema)
}

// OpenBackend loads the store from a backend (nil for memory only).
func OpenBackend(be Backend, schema *Schema) (*Store, error) {
	if schema == nil {
		return nil, errors.New("ontology: nil schema")
	}
	st := &Store{schema: schema, backend: be, s: emptyState(), ix: newIndex()}
	if be == nil {
		return st, nil
	}
	loaded, err := be.Load()
	if err != nil {
		be.Close()
		return nil, err
	}
	if loaded.Objects != nil {
		st.s = loaded
	}
	if st.s.Objects == nil {
		st.s.Objects = map[string]Object{}
	}
	if st.s.Links == nil {
		st.s.Links = map[string]Link{}
	}
	if st.s.Candidates == nil {
		st.s.Candidates = map[string]Candidate{}
	}
	if st.s.Redirects == nil {
		st.s.Redirects = map[string]string{}
	}
	_, st.extHistory = be.(HistoryReader)
	st.reindex()
	return st, nil
}

func emptyState() state {
	return state{Version: 1, Objects: map[string]Object{}, Links: map[string]Link{}, Candidates: map[string]Candidate{}, Redirects: map[string]string{}}
}

func (s *Store) save() error { return s.flush() }

// Upsert adds or merges an object. Properties are replaced per name, so a
// later observation of one property keeps the others. It rejects unknown
// types, unknown properties and values of the wrong kind.
// validate checks an object against the schema without storing it.
func (s *Store) validate(o Object) error {
	typ, _, _, ok := SplitID(o.ID)
	if !ok || typ != o.Type {
		return fmt.Errorf("object id %q is not <type>:<namespace>:<key> for type %q", o.ID, o.Type)
	}
	ot, ok := s.schema.Object(o.Type)
	if !ok {
		return fmt.Errorf("unknown object type %q", o.Type)
	}
	decl := map[string]Property{}
	for _, p := range ot.Properties {
		decl[p.Name] = p
	}
	for name, v := range o.Props {
		p, ok := decl[name]
		if !ok {
			return fmt.Errorf("%s: unknown property %q", o.ID, name)
		}
		if !kindOK(p.Type, v.V) {
			return fmt.Errorf("%s: property %q wants %s, got %T", o.ID, name, p.Type, v.V)
		}
		if v.Prov.Source == "" {
			return fmt.Errorf("%s: property %q has no provenance source", o.ID, name)
		}
	}
	return nil
}

func (s *Store) Upsert(o Object) error {
	if err := s.validate(o); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, exists := s.s.Objects[o.ID]
	if !exists {
		cur = Object{ID: o.ID, Type: o.Type, Tenant: o.Tenant, Props: map[string]Value{}}
	}
	for _, k := range sortedKeys(o.Props) {
		v := o.Props[k]
		old, had := cur.Props[k]
		if !had || !reflect.DeepEqual(old.V, v.V) {
			c := Change{Object: o.ID, Property: k, After: v}
			if had {
				c.Before = &old
			}
			s.addChange(c)
		}
		cur.Props[k] = v
	}
	for _, a := range o.Aliases {
		if !hasAlias(cur.Aliases, a) {
			cur.Aliases = append(cur.Aliases, a)
		}
	}
	s.putObject(cur)
	return s.save()
}

// Missing lists required properties an object has no value for.
func (s *Store) Missing(id string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.s.Objects[id]
	if !ok {
		return nil
	}
	ot, _ := s.schema.Object(o.Type)
	var out []string
	for _, p := range ot.Properties {
		if _, has := o.Props[p.Name]; p.Required && !has {
			out = append(out, p.Name)
		}
	}
	return out
}

// AddLink records a link between two existing objects of the right types.
func (s *Store) AddLink(typ, from, to string, prov Prov) (Link, error) {
	lt, ok := s.schema.Link(typ)
	if !ok {
		return Link{}, fmt.Errorf("unknown link type %q", typ)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, fok := s.s.Objects[from]
	t, tok := s.s.Objects[to]
	if !fok || !tok {
		return Link{}, fmt.Errorf("link %s: both ends must exist (%s, %s)", typ, from, to)
	}
	if f.Type != lt.From || t.Type != lt.To {
		return Link{}, fmt.Errorf("link %s joins %s to %s, not %s to %s", typ, lt.From, lt.To, f.Type, t.Type)
	}
	l := Link{ID: LinkID(typ, from, to), Type: typ, From: from, To: to, Prov: prov}
	s.putLink(l)
	return l, s.save()
}

// Get returns one object.
func (s *Store) Get(id string) (Object, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.s.Objects[s.resolveLocked(id)]
	return o, ok
}

func (s *Store) resolveLocked(id string) string {
	for i := 0; i < 8; i++ {
		to, ok := s.s.Redirects[id]
		if !ok {
			return id
		}
		id = to
	}
	return id
}

// List returns objects of a type (all types when typ is empty), sorted by ID.
func (s *Store) List(typ string) []Object {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Object
	for _, o := range s.s.Objects {
		if typ == "" || o.Type == typ {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Links returns every link touching id, sorted by link ID.
func (s *Store) Links(id string) []Link {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.linksLocked(id)
}

func (s *Store) linksLocked(id string) []Link {
	ids := s.ix.adj[id]
	out := make([]Link, 0, len(ids))
	for lid := range ids {
		out = append(out, s.s.Links[lid])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Impact is an object that depends, directly or transitively, on another.
type Impact struct {
	Object Object `json:"object"`
	Depth  int    `json:"depth"`
	Via    string `json:"via"` // link type of the last hop
}

// Impact walks links backwards from id: an object X depends on Y when a link
// X -> Y exists. So the impact of a failing cluster is the services running
// on it, the customers consuming those services, and so on. maxDepth <= 0
// means unlimited. Cycles are safe. This is reachability, not causation.
func (s *Store) Impact(id string, maxDepth int) []Impact {
	return s.impact(id, maxDepth, func(o Object) (Object, bool) { return o, true })
}

// impact is Impact with a visibility filter: objects the caller may not see
// are neither returned nor traversed through.
func (s *Store) impact(id string, maxDepth int, vis func(Object) (Object, bool)) []Impact {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id = s.resolveLocked(id)
	root, ok := s.s.Objects[id]
	if !ok {
		return nil
	}
	if _, ok := vis(root); !ok {
		return nil
	}
	seen := map[string]bool{id: true}
	frontier := []string{id}
	var out []Impact
	for depth := 1; len(frontier) > 0 && (maxDepth <= 0 || depth <= maxDepth); depth++ {
		var next []string
		for _, cur := range frontier {
			for _, l := range s.linksLocked(cur) {
				if l.To != cur || seen[l.From] {
					continue
				}
				seen[l.From] = true
				o, ok := vis(s.s.Objects[l.From])
				if !ok {
					continue
				}
				next = append(next, l.From)
				out = append(out, Impact{Object: o, Depth: depth, Via: l.Type})
			}
		}
		frontier = next
	}
	return out
}

func hasAlias(as []Alias, a Alias) bool {
	for _, x := range as {
		if x == a {
			return true
		}
	}
	return false
}

func kindOK(typ string, v any) bool {
	switch typ {
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		switch v.(type) {
		case float64, float32, int, int64, int32:
			return true
		}
	case "bool":
		_, ok := v.(bool)
		return ok
	case "time":
		switch x := v.(type) {
		case time.Time:
			return true
		case string:
			_, err := time.Parse(time.RFC3339, x)
			return err == nil
		}
	}
	return false
}

func sortedKeys(m map[string]Value) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// History returns the changes to one object, oldest first.
func (s *Store) History(id string) []Change {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id = s.resolveLocked(id)
	if hr, ok := s.backend.(HistoryReader); ok && s.extHistory {
		out, err := hr.History(id)
		if err != nil {
			return nil
		}
		return out
	}
	var out []Change
	for _, c := range s.s.History {
		if c.Object == id {
			out = append(out, c)
		}
	}
	return out
}

// Digest fingerprints what an action would act on: each object's id, tenant
// and property values (not observation times). A proposal records it so the
// executor can tell the facts changed after approval.
func (s *Store) Digest(refs []ObjectRef) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h := sha256.New()
	sorted := append([]ObjectRef(nil), refs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Input < sorted[j].Input })
	for _, r := range sorted {
		o, ok := s.s.Objects[s.resolveLocked(r.ID)]
		fmt.Fprint(h, r.Input, "|", r.ID, "|", ok, "|", o.ID, "|", o.Tenant, "|")
		for _, k := range sortedKeys(o.Props) {
			fmt.Fprint(h, k, "=", o.Props[k].V, ";")
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
