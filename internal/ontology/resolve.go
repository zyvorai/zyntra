// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ontology

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Candidate is a proposed identity match awaiting a human decision.
type Candidate struct {
	ID        string `json:"id"`
	A         string `json:"a"` // object that stays if accepted
	B         string `json:"b"` // object merged into A
	Reason    string `json:"reason"`
	Status    string `json:"status"` // pending | accepted | rejected
	DecidedBy string `json:"decided_by,omitempty"`
}

func normalise(v any) string {
	var b strings.Builder
	for _, r := range strings.ToLower(fmt.Sprint(v)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func candID(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return LinkID("cand", a, b)[5:]
}

// propose queues a candidate for every other object of the same type whose
// match property normalises to the same non-empty string. Pairs already
// decided are not proposed again.
func (s *Store) propose(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.s.Objects[id]
	if !ok {
		return 0
	}
	ot, _ := s.schema.Object(o.Type)
	if ot.Match == "" {
		return 0
	}
	mine, ok := o.Props[ot.Match]
	if !ok || normalise(mine.V) == "" {
		return 0
	}
	n := 0
	for _, other := range s.s.Objects {
		// Objects of different tenants are never candidates for merging.
		if other.ID == id || other.Type != o.Type || other.Tenant != o.Tenant {
			continue
		}
		theirs, ok := other.Props[ot.Match]
		if !ok || normalise(theirs.V) != normalise(mine.V) {
			continue
		}
		cid := candID(id, other.ID)
		if _, seen := s.s.Candidates[cid]; seen {
			continue
		}
		a, b := other.ID, id // the older object is kept
		s.s.Candidates[cid] = Candidate{ID: cid, A: a, B: b, Status: "pending",
			Reason: fmt.Sprintf("%s %q and %q normalise to the same value", ot.Match, mine.V, theirs.V)}
		n++
	}
	if n > 0 {
		_ = s.save()
	}
	return n
}

// Candidates lists identity candidates, pending first.
func (s *Store) Candidates() []Candidate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Candidate, 0, len(s.s.Candidates))
	for _, c := range s.s.Candidates {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Status == "pending") != (out[j].Status == "pending") {
			return out[i].Status == "pending"
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Decide accepts (merges B into A) or rejects a candidate.
func (s *Store) Decide(id string, accept bool, by string) (Candidate, error) {
	s.mu.Lock()
	c, ok := s.s.Candidates[id]
	if !ok {
		s.mu.Unlock()
		return Candidate{}, errors.New("no such candidate")
	}
	if c.Status != "pending" {
		s.mu.Unlock()
		return c, fmt.Errorf("candidate already %s", c.Status)
	}
	c.DecidedBy = by
	if !accept {
		c.Status = "rejected"
		s.s.Candidates[id] = c
		err := s.save()
		s.mu.Unlock()
		s.note(c, by, "rejected")
		return c, err
	}
	a, aok := s.s.Objects[c.A]
	b, bok := s.s.Objects[c.B]
	if !aok || !bok {
		s.mu.Unlock()
		return c, errors.New("one of the objects no longer exists")
	}
	for k, v := range b.Props {
		if _, has := a.Props[k]; !has {
			a.Props[k] = v
		}
	}
	for _, al := range b.Aliases {
		if !hasAlias(a.Aliases, al) {
			a.Aliases = append(a.Aliases, al)
		}
	}
	// Merge keeps b's id as an alias so external systems still resolve.
	_, ns, key, _ := SplitID(b.ID)
	if al := (Alias{System: ns, ExternalID: key}); !hasAlias(a.Aliases, al) {
		a.Aliases = append(a.Aliases, al)
	}
	s.s.Objects[a.ID] = a
	delete(s.s.Objects, b.ID)
	s.s.Redirects[b.ID] = a.ID
	for lid, l := range s.s.Links {
		if l.From != b.ID && l.To != b.ID {
			continue
		}
		delete(s.s.Links, lid)
		if l.From == b.ID {
			l.From = a.ID
		}
		if l.To == b.ID {
			l.To = a.ID
		}
		if l.From == l.To {
			continue
		}
		l.ID = LinkID(l.Type, l.From, l.To)
		s.s.Links[l.ID] = l
	}
	c.Status = "accepted"
	s.s.Candidates[id] = c
	err := s.save()
	s.mu.Unlock()
	s.note(c, by, "accepted: merged "+c.B+" into "+c.A)
	return c, err
}

func (s *Store) note(c Candidate, by, what string) {
	if s.Audit != nil {
		s.Audit("ontology:resolve:"+c.ID, by, what)
	}
}
