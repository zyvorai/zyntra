// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ontology

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RecordLink asks for a link from a record's object to another object.
type RecordLink struct {
	Type   string `json:"type"`
	ToType string `json:"to_type"`
	ToNS   string `json:"to_namespace"`
	ToKey  string `json:"to_key"`
}

// Record is one object observation from a connector.
type Record struct {
	Type       string         `json:"type"`
	Namespace  string         `json:"namespace"`
	Key        string         `json:"key"`
	Tenant     string         `json:"tenant,omitempty"`
	Props      map[string]any `json:"props"`
	Aliases    []Alias        `json:"aliases,omitempty"`
	Links      []RecordLink   `json:"links,omitempty"`
	ObservedAt time.Time      `json:"observed_at,omitempty"`
	SourceID   string         `json:"source_id,omitempty"`
	Transform  []string       `json:"transform,omitempty"`
}

// IngestReport says what a batch changed.
type IngestReport struct {
	Source     string   `json:"source"`
	Objects    int      `json:"objects"`
	Links      int      `json:"links"`
	Candidates int      `json:"candidates"`
	Skipped    []string `json:"skipped,omitempty"`
	Pruned     int      `json:"pruned,omitempty"`
	Hash       string   `json:"hash"`
	Changed    bool     `json:"changed"`
}

// Ingest writes a batch of records in two passes (objects, then links) so a
// link may point at an object later in the same batch. Records that fail
// validation are skipped and reported; one bad row never drops the batch.
func (s *Store) Ingest(source, by string, recs []Record, now time.Time) (IngestReport, error) {
	return s.ingest(source, by, recs, now, false)
}

// IngestSnapshot is Ingest for a source that lists everything it knows: after
// the batch, objects whose every fact came from this source and that the
// batch did not mention are removed, with their links. An empty batch never
// prunes, so a failed or empty listing cannot wipe the source's objects.
func (s *Store) IngestSnapshot(source, by string, recs []Record, now time.Time) (IngestReport, error) {
	return s.ingest(source, by, recs, now, true)
}

func (s *Store) ingest(source, by string, recs []Record, now time.Time, prune bool) (_ IngestReport, err error) {
	rep := IngestReport{Source: source}
	if err := s.checkLimit(recs); err != nil {
		return rep, err
	}
	seen := make(map[string]bool, len(recs))
	s.beginBatch()
	defer func() {
		if ferr := s.endBatch(); ferr != nil && err == nil {
			err = ferr
		}
	}()
	sorted := append([]Record(nil), recs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Type+sorted[i].Namespace+sorted[i].Key < sorted[j].Type+sorted[j].Namespace+sorted[j].Key
	})
	b, _ := json.Marshal(sorted)
	h := sha256.Sum256(b)
	rep.Hash = hex.EncodeToString(h[:])
	before := s.digest()

	type pending struct {
		id string
		r  Record
		pv Prov
	}
	var linkPass []pending
	links := map[string]bool{}
	for _, r := range recs {
		id := MakeID(r.Type, r.Namespace, r.Key)
		if r.Type == "" || r.Namespace == "" || r.Key == "" {
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("record %s/%s/%s: type, namespace and key are required", r.Type, r.Namespace, r.Key))
			continue
		}
		obs := r.ObservedAt
		if obs.IsZero() {
			obs = now
		}
		pv := Prov{Source: source, SourceID: r.SourceID, ObservedAt: obs, IngestedAt: now, Transform: r.Transform}
		props := map[string]Value{}
		for k, v := range r.Props {
			props[k] = Value{V: v, Prov: pv}
		}
		o := Object{ID: id, Type: r.Type, Tenant: r.Tenant, Props: props, Aliases: r.Aliases}
		// An alias seen before pins the record to the object that owns it.
		if owner, ok := s.byAlias(r.Aliases, r.Tenant); ok && owner != id {
			o.ID = owner
		}
		isNew := false
		if _, ok := s.Get(o.ID); !ok {
			isNew = true
		}
		if err := s.Upsert(o); err != nil {
			rep.Skipped = append(rep.Skipped, err.Error())
			continue
		}
		seen[o.ID] = true
		if isNew {
			rep.Candidates += s.propose(o.ID)
		}
		linkPass = append(linkPass, pending{o.ID, r, pv})
	}
	for _, p := range linkPass {
		for _, l := range p.r.Links {
			to := MakeID(l.ToType, l.ToNS, l.ToKey)
			if _, err := s.AddLink(l.Type, p.id, to, p.pv); err != nil {
				rep.Skipped = append(rep.Skipped, err.Error())
				continue
			}
			links[LinkID(l.Type, p.id, to)] = true
		}
	}
	// Distinct objects and links: a record seen twice (a customer on two rows)
	// is one object, and the report should say so.
	rep.Objects, rep.Links = len(seen), len(links)
	if prune && len(recs) > 0 && len(rep.Skipped) < len(recs) {
		rep.Pruned = s.pruneSource(source, seen)
	}
	rep.Changed = s.digest() != before
	if rep.Changed && s.Audit != nil {
		note := fmt.Sprintf("ingested %d objects, %d links (batch %s), %d skipped", rep.Objects, rep.Links, rep.Hash[:12], len(rep.Skipped))
		if rep.Pruned > 0 {
			note += fmt.Sprintf(", pruned %d that are no longer listed", rep.Pruned)
		}
		s.Audit("ontology:"+source, by, note)
	}
	return rep, nil
}

// digest fingerprints object and link content ignoring ingest timestamps, so
// re-reading an unchanged file is not an audited change.
func (s *Store) digest() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h := sha256.New()
	ids := make([]string, 0, len(s.s.Objects))
	for id := range s.s.Objects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		o := s.s.Objects[id]
		keys := make([]string, 0, len(o.Props))
		for k := range o.Props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprint(h, id, o.Tenant)
		for _, k := range keys {
			fmt.Fprint(h, k, "=", o.Props[k].V, ";")
		}
		fmt.Fprint(h, o.Aliases)
	}
	lids := make([]string, 0, len(s.s.Links))
	for id := range s.s.Links {
		lids = append(lids, id)
	}
	sort.Strings(lids)
	fmt.Fprint(h, lids)
	return hex.EncodeToString(h.Sum(nil))
}

// byAlias finds the object of this tenant that owns one of the aliases. An
// alias held by another tenant's object is invisible here.
func (s *Store) byAlias(as []Alias, tenant string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range as {
		if id, ok := s.ix.alias[aliasKey(tenant, a)]; ok {
			return id, true
		}
	}
	return "", false
}

// Loader reads a parsed document (a slice of row maps for CSV) from a path.
// The adapters file cache satisfies it.
type Loader func(path, format string) (any, error)

// Records turns a mapping's source rows into records. dir is the pack
// directory the mapping's source is relative to. A fact with no observed
// column is dated by the file's modification time, not by the time it was
// read: re-reading an old file must not make its facts look fresh.
func (m Mapping) Records(dir string, schema *Schema, load Loader) ([]Record, error) {
	path := filepath.Join(dir, m.Source)
	doc, err := load(path, m.Format)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", m.Source, err)
	}
	rows, ok := doc.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: expected a table of rows", m.Source)
	}
	var observed time.Time
	if fi, err := os.Stat(path); err == nil {
		observed = fi.ModTime().UTC()
	}
	return m.FromRows(rows, schema, m.Source, observed)
}

// FromRows maps already-read rows (a table from a file, a SQL result, a
// kubectl listing) to records. label names the source in errors and
// provenance; observed dates facts that have no observed column (zero means
// the ingest time).
func (m Mapping) FromRows(rows []any, schema *Schema, label string, observed time.Time) ([]Record, error) {
	ot, _ := schema.Object(m.Type)
	kinds := map[string]string{}
	for _, p := range ot.Properties {
		kinds[p.Name] = p.Type
	}
	var out []Record
	for i, el := range rows {
		row, ok := el.(map[string]any)
		if !ok {
			continue
		}
		cell := func(col string) string { return strings.TrimSpace(fmt.Sprint(row[col])) }
		if row[m.Key] == nil || cell(m.Key) == "" {
			return nil, fmt.Errorf("%s row %d: key column %q is empty", label, i+1, m.Key)
		}
		r := Record{Type: m.Type, Namespace: m.Namespace, Key: cell(m.Key), Tenant: m.Tenant,
			Props: map[string]any{}, SourceID: label + "#" + strconv.Itoa(i+1), ObservedAt: observed}
		for prop, col := range m.Props {
			if row[col] == nil || cell(col) == "" {
				continue
			}
			v, err := convert(kinds[prop], row[col])
			if err != nil {
				return nil, fmt.Errorf("%s row %d: %s: %w", label, i+1, prop, err)
			}
			r.Props[prop] = v
		}
		if m.Observed != "" && cell(m.Observed) != "" {
			if t, err := time.Parse(time.RFC3339, cell(m.Observed)); err == nil {
				r.ObservedAt = t
			}
		}
		for _, a := range m.Aliases {
			if v := cell(a.Column); v != "" && row[a.Column] != nil {
				r.Aliases = append(r.Aliases, Alias{System: a.System, ExternalID: v})
			}
		}
		for _, l := range m.Links {
			if v := cell(l.Column); v != "" && row[l.Column] != nil {
				ns := l.Namespace
				if ns == "" {
					ns = m.Namespace
				}
				r.Links = append(r.Links, RecordLink{Type: l.Type, ToType: l.To, ToNS: ns, ToKey: v})
			}
		}
		out = append(out, r)
	}
	return out, nil
}

func convert(kind string, v any) (any, error) {
	if kind == "" || kind == "string" {
		return strings.TrimSpace(fmt.Sprint(v)), nil
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	switch kind {
	case "number":
		if f, ok := v.(float64); ok {
			return f, nil
		}
		return strconv.ParseFloat(s, 64)
	case "bool":
		return strconv.ParseBool(s)
	case "time":
		if _, err := time.Parse(time.RFC3339, s); err != nil {
			return nil, err
		}
		return s, nil
	}
	return nil, fmt.Errorf("unknown kind %q", kind)
}

// IngestMappings reads every mapping of a definition and ingests the lot as
// one batch per source.
func (s *Store) IngestMappings(d *Definition, dir, by string, load Loader, now time.Time) ([]IngestReport, error) {
	var reps []IngestReport
	var all []Record
	for _, m := range d.Mappings {
		recs, err := m.Records(dir, d.Schema(), load)
		if err != nil {
			return reps, err
		}
		all = append(all, recs...)
	}
	rep, err := s.Ingest("pack:"+filepath.Base(dir), by, all, now)
	return append(reps, rep), err
}

// Fingerprint identifies the current object and link content, ignoring
// timestamps. Scenarios record it so a result names the data it ran on.
func (s *Store) Fingerprint() string { return s.digest() }

// IngestScoped is Ingest for a caller confined to one tenant. It is
// all-or-nothing: if any record is invalid, or would touch an object that
// belongs to another tenant (directly or through an alias), nothing is
// written. Records without a tenant are placed in the caller's tenant.
func (s *Store) IngestScoped(tenant, source, by string, recs []Record, now time.Time) (IngestReport, error) {
	scoped := make([]Record, len(recs))
	for i, r := range recs {
		if r.Tenant != "" && r.Tenant != tenant {
			return IngestReport{}, fmt.Errorf("record %d: tenant %q is not %q", i+1, r.Tenant, tenant)
		}
		r.Tenant = tenant
		scoped[i] = r
		if r.Type == "" || r.Namespace == "" || r.Key == "" {
			return IngestReport{}, fmt.Errorf("record %d: type, namespace and key are required", i+1)
		}
		id := MakeID(r.Type, r.Namespace, r.Key)
		if owner, ok := s.byAlias(r.Aliases, tenant); ok {
			id = owner
		}
		// Deliberately vague: the message must not confirm that another
		// tenant has an object with this id.
		if cur, ok := s.Get(id); ok && cur.Tenant != tenant {
			return IngestReport{}, fmt.Errorf("record %d: object id %s is not available to this tenant", i+1, id)
		}
		props := map[string]Value{}
		for k, v := range r.Props {
			props[k] = Value{V: v, Prov: Prov{Source: source}}
		}
		if err := s.validate(Object{ID: id, Type: r.Type, Tenant: tenant, Props: props}); err != nil {
			return IngestReport{}, fmt.Errorf("record %d: %w", i+1, err)
		}
	}
	return s.Ingest(source, by, scoped, now)
}

// PruneUnlisted removes the objects that source alone vouches for and that
// recs, a complete listing of what still exists, does not name. Nothing is
// written except the removals. An empty listing prunes nothing, so a failed or
// empty query cannot wipe the source's objects.
func (s *Store) PruneUnlisted(source, by string, recs []Record, now time.Time) (int, error) {
	if len(recs) == 0 {
		return 0, nil
	}
	seen := make(map[string]bool, len(recs))
	for _, r := range recs {
		if r.Type == "" || r.Namespace == "" || r.Key == "" {
			return 0, fmt.Errorf("record %s/%s/%s: type, namespace and key are required", r.Type, r.Namespace, r.Key)
		}
		id := MakeID(r.Type, r.Namespace, r.Key)
		if owner, ok := s.byAlias(r.Aliases, r.Tenant); ok {
			id = owner
		}
		seen[id] = true
	}
	n := s.pruneSource(source, seen)
	if n > 0 && s.Audit != nil {
		s.Audit("ontology:"+source, by, fmt.Sprintf("reconciled against %d listed rows: pruned %d that no longer exist", len(recs), n))
	}
	return n, nil
}

// pruneSource removes objects that this source alone vouches for and that it
// no longer lists. An object with any fact from another source is kept (the
// other source still knows it), as is one with no facts at all.
func (s *Store) pruneSource(source string, keep map[string]bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var gone []string
	for id, o := range s.s.Objects {
		if keep[id] || len(o.Props) == 0 {
			continue
		}
		only := true
		for _, v := range o.Props {
			if v.Prov.Source != source {
				only = false
				break
			}
		}
		if only {
			gone = append(gone, id)
		}
	}
	sort.Strings(gone)
	for _, id := range gone {
		for lid := range s.ix.adj[id] {
			s.delLink(lid)
		}
		s.delObject(id)
	}
	if len(gone) > 0 {
		_ = s.save()
	}
	return len(gone)
}
