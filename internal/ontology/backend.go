// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ontology

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// opKind names one incremental change to persisted state.
type opKind int

const (
	opPutObject opKind = iota
	opDelObject
	opPutLink
	opDelLink
	opPutCand
	opPutRedirect
	opAddChange
)

type op struct {
	kind opKind
	id   string // object, link or candidate id; redirect source
	to   string // redirect target
	obj  Object
	link Link
	cand Candidate
	chg  Change
}

// Backend persists the store. Load returns everything at start-up; Apply
// receives the changes made since the last call, in order, so a backend can
// write only what changed.
type Backend interface {
	Load() (state, error)
	Apply(ops []op, snapshot func() state) error
	Close() error
}

// HistoryReader is implemented by backends that keep the full change log
// outside memory.
type HistoryReader interface {
	History(object string) ([]Change, error)
}

// jsonBackend keeps the whole state in one file, rewritten on every save. It
// is simple and fine up to a few thousand objects.
type jsonBackend struct{ path string }

func (j jsonBackend) Load() (state, error) {
	b, err := os.ReadFile(j.path)
	if errors.Is(err, os.ErrNotExist) {
		return state{}, nil
	}
	if err != nil {
		return state{}, err
	}
	var st state
	return st, json.Unmarshal(b, &st)
}

func (j jsonBackend) Apply(_ []op, snapshot func() state) error {
	b, err := json.MarshalIndent(snapshot(), "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(j.path), 0o750); err != nil {
		return err
	}
	tmp := j.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, j.path)
}

func (j jsonBackend) Close() error { return nil }

// NewBackend picks a backend from the file name: .db, .sqlite or .sqlite3
// select SQLite, anything else the JSON file.
func NewBackend(path string) (Backend, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".db", ".sqlite", ".sqlite3":
		return openSQLite(path)
	}
	return jsonBackend{path: path}, nil
}

// index holds the lookups that keep ingest and traversal from scanning every
// object. It is rebuilt from state on load and kept current by the mutation
// helpers below.
type index struct {
	adj   map[string]map[string]struct{} // object id -> ids of links touching it
	alias map[string]string              // tenant|system|external id -> object id
	match map[string]map[string]struct{} // type|tenant|normalised match value -> object ids
	// bound holds the ids of objects measured by at least one KPI (through
	// their type or their own "kpis" property): the only objects that can be
	// at risk, so the risk summary reads these instead of every object.
	bound map[string]struct{}
	// tenantTypes counts objects per tenant and type ("" is the tenant-less
	// space), so the types a tenant can possibly see are known without a scan.
	tenantTypes map[string]map[string]int
}

func newIndex() index {
	return index{adj: map[string]map[string]struct{}{}, alias: map[string]string{}, match: map[string]map[string]struct{}{}, bound: map[string]struct{}{}, tenantTypes: map[string]map[string]int{}}
}

func aliasKey(tenant string, a Alias) string {
	return tenant + "\x00" + a.System + "\x00" + a.ExternalID
}

func (s *Store) matchKey(o Object) (string, bool) {
	ot, ok := s.schema.Object(o.Type)
	if !ok || ot.Match == "" {
		return "", false
	}
	v, ok := o.Props[ot.Match]
	if !ok || normalise(v.V) == "" {
		return "", false
	}
	return o.Type + "\x00" + o.Tenant + "\x00" + normalise(v.V), true
}

func (s *Store) indexObject(o Object) {
	tt := s.ix.tenantTypes[o.Tenant]
	if tt == nil {
		tt = map[string]int{}
		s.ix.tenantTypes[o.Tenant] = tt
	}
	tt[o.Type]++
	if len(BoundKPIs(s.schema, o)) > 0 {
		s.ix.bound[o.ID] = struct{}{}
	}
	for _, a := range o.Aliases {
		s.ix.alias[aliasKey(o.Tenant, a)] = o.ID
	}
	if k, ok := s.matchKey(o); ok {
		set := s.ix.match[k]
		if set == nil {
			set = map[string]struct{}{}
			s.ix.match[k] = set
		}
		set[o.ID] = struct{}{}
	}
}

func (s *Store) unindexObject(o Object) {
	if tt := s.ix.tenantTypes[o.Tenant]; tt != nil {
		if tt[o.Type]--; tt[o.Type] <= 0 {
			delete(tt, o.Type)
		}
	}
	delete(s.ix.bound, o.ID)
	for _, a := range o.Aliases {
		if s.ix.alias[aliasKey(o.Tenant, a)] == o.ID {
			delete(s.ix.alias, aliasKey(o.Tenant, a))
		}
	}
	if k, ok := s.matchKey(o); ok {
		delete(s.ix.match[k], o.ID)
		if len(s.ix.match[k]) == 0 {
			delete(s.ix.match, k)
		}
	}
}

func (s *Store) reindex() {
	s.ix = newIndex()
	for _, o := range s.s.Objects {
		s.indexObject(o)
	}
	for _, l := range s.s.Links {
		s.linkAdj(l)
	}
}

func (s *Store) linkAdj(l Link) {
	for _, end := range []string{l.From, l.To} {
		set := s.ix.adj[end]
		if set == nil {
			set = map[string]struct{}{}
			s.ix.adj[end] = set
		}
		set[l.ID] = struct{}{}
	}
}

// Mutation helpers. The caller holds the write lock. Each updates memory and
// the indexes and records the change for the backend.

func (s *Store) putObject(o Object) {
	if old, ok := s.s.Objects[o.ID]; ok {
		s.unindexObject(old)
	} else {
		s.version++ // a new id: the sorted order changes
	}
	s.s.Objects[o.ID] = o
	s.indexObject(o)
	s.dirty = append(s.dirty, op{kind: opPutObject, id: o.ID, obj: o})
}

func (s *Store) delObject(id string) {
	if old, ok := s.s.Objects[id]; ok {
		s.unindexObject(old)
		s.version++
	}
	delete(s.s.Objects, id)
	s.dirty = append(s.dirty, op{kind: opDelObject, id: id})
}

func (s *Store) putLink(l Link) {
	s.s.Links[l.ID] = l
	s.linkAdj(l)
	s.dirty = append(s.dirty, op{kind: opPutLink, id: l.ID, link: l})
}

func (s *Store) delLink(id string) {
	if l, ok := s.s.Links[id]; ok {
		delete(s.ix.adj[l.From], id)
		delete(s.ix.adj[l.To], id)
	}
	delete(s.s.Links, id)
	s.dirty = append(s.dirty, op{kind: opDelLink, id: id})
}

func (s *Store) putCand(c Candidate) {
	s.s.Candidates[c.ID] = c
	s.dirty = append(s.dirty, op{kind: opPutCand, id: c.ID, cand: c})
}

func (s *Store) putRedirect(from, to string) {
	s.s.Redirects[from] = to
	s.dirty = append(s.dirty, op{kind: opPutRedirect, id: from, to: to})
}

func (s *Store) addChange(c Change) {
	if !s.extHistory {
		s.s.History = append(s.s.History, c)
		// Trim in chunks: let the log reach twice the cap, then cut it back
		// to the cap in one copy. Trimming on every change would copy the
		// whole log each time once it is full.
		if n := len(s.s.History); n >= 2*maxHistory {
			s.s.History = append([]Change(nil), s.s.History[n-maxHistory:]...)
		}
	}
	s.dirty = append(s.dirty, op{kind: opAddChange, chg: c})
}

// flush hands the pending changes to the backend. The caller holds the lock.
func (s *Store) flush() error {
	if s.batch > 0 {
		return nil // an ingest in progress flushes once at its end
	}
	ops := s.dirty
	s.dirty = nil
	if s.backend == nil || len(ops) == 0 {
		return nil
	}
	return s.backend.Apply(ops, func() state { return s.s })
}

// Close releases the backend.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend == nil {
		return nil
	}
	return s.backend.Close()
}

// Migrate copies a store from one backend path to another, for moving from
// the JSON file to SQLite (or back). The destination must not exist or must
// be empty. It returns the number of objects and links copied.
func Migrate(from, to string, schema *Schema) (objects, links int, err error) {
	if from == to {
		return 0, 0, errors.New("source and destination are the same file")
	}
	src, err := Open(from, schema)
	if err != nil {
		return 0, 0, err
	}
	defer src.Close()
	dst, err := Open(to, schema)
	if err != nil {
		return 0, 0, err
	}
	defer dst.Close()
	if len(dst.s.Objects) > 0 {
		return 0, 0, errors.New("destination already holds objects; refusing to merge into it")
	}
	src.mu.RLock()
	defer src.mu.RUnlock()
	dst.mu.Lock()
	defer dst.mu.Unlock()
	for _, id := range sortedIDs(src.s.Objects) {
		dst.putObject(src.s.Objects[id])
	}
	for _, id := range sortedIDs(src.s.Links) {
		dst.putLink(src.s.Links[id])
	}
	for _, id := range sortedIDs(src.s.Candidates) {
		dst.putCand(src.s.Candidates[id])
	}
	for from, to := range src.s.Redirects {
		dst.putRedirect(from, to)
	}
	hist := src.s.History
	if src.extHistory {
		hist = nil
		for _, id := range sortedIDs(src.s.Objects) {
			h, herr := src.backend.(HistoryReader).History(id)
			if herr != nil {
				return 0, 0, herr
			}
			hist = append(hist, h...)
		}
	}
	for _, c := range hist {
		dst.addChange(c)
	}
	if err := dst.flush(); err != nil {
		return 0, 0, err
	}
	return len(src.s.Objects), len(src.s.Links), nil
}

func sortedIDs[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// beginBatch defers backend writes until the matching endBatch, so a whole
// ingest is one write (one transaction in SQLite, one file rewrite in JSON).
func (s *Store) beginBatch() {
	s.mu.Lock()
	s.batch++
	s.mu.Unlock()
}

func (s *Store) endBatch() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batch--
	return s.flush()
}
