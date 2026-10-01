// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package approvals tracks proposals through pending -> approved|rejected ->
// executed|failed, with an append-only audit trail persisted as JSON.
package approvals

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/zyvorai/zyntra/internal/executor"
)

type Status string

const (
	Pending  Status = "pending"
	Approved Status = "approved"
	Rejected Status = "rejected"
	Executed Status = "executed"
	Failed   Status = "failed"
)

var ErrNotFound = errors.New("proposal not found")

// KeepRef links a proposal to Fabric Keep's records.
type KeepRef struct {
	Mode       string `json:"mode"` // keep | mirror
	SessionID  string `json:"session_id,omitempty"`
	ApprovalID string `json:"approval_id,omitempty"`
	ReceiptID  string `json:"receipt_id,omitempty"`
	Error      string `json:"error,omitempty"`
}

type Prediction struct {
	SeverityBefore float64            `json:"severity_before"`
	SeverityAfter  float64            `json:"severity_after"`
	Closes         []string           `json:"closes,omitempty"`
	Opens          []string           `json:"opens,omitempty"`
	KPIs           map[string]float64 `json:"kpis"`
}

type Proposal struct {
	ID         string             `json:"id"`
	Action     string             `json:"action"`
	ActionName string             `json:"action_name"`
	Risk       string             `json:"risk,omitempty"`
	Adapter    string             `json:"adapter,omitempty"`
	Template   string             `json:"template,omitempty"`
	Render     string             `json:"render,omitempty"`
	RenderErr  string             `json:"render_error,omitempty"`
	Predicted  Prediction         `json:"predicted"`
	Baseline   map[string]float64 `json:"baseline"`
	Actual     map[string]float64 `json:"actual,omitempty"`
	Status     Status             `json:"status"`
	CreatedAt  time.Time          `json:"created_at"`
	CreatedBy  string             `json:"created_by"`
	DecidedAt  *time.Time         `json:"decided_at,omitempty"`
	DecidedBy  string             `json:"decided_by,omitempty"`
	Reason     string             `json:"reason,omitempty"`
	Execution  *executor.Result   `json:"execution,omitempty"`
	ExecutedAt *time.Time         `json:"executed_at,omitempty"`
	Keep       *KeepRef           `json:"keep,omitempty"`
}

type Event struct {
	At       time.Time `json:"at"`
	Proposal string    `json:"proposal"`
	Action   string    `json:"action"`
	From     Status    `json:"from,omitempty"`
	To       Status    `json:"to"`
	By       string    `json:"by"`
	Note     string    `json:"note,omitempty"`
}

type state struct {
	Proposals []*Proposal `json:"proposals"`
	Audit     []Event     `json:"audit"`
}

type Store struct {
	mu   sync.Mutex
	path string
	s    state
	now  func() time.Time
}

// Open loads path (empty path keeps everything in memory).
func Open(path string) (*Store, error) {
	st := &Store{path: path, now: time.Now}
	if path == "" {
		return st, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &st.s); err != nil {
		return nil, fmt.Errorf("approvals state %s: %w", path, err)
	}
	return st, nil
}

func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s.s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "prop-" + hex.EncodeToString(b)
}

func (s *Store) audit(p *Proposal, from, to Status, by, note string) {
	s.s.Audit = append(s.s.Audit, Event{At: s.now().UTC(), Proposal: p.ID, Action: p.Action, From: from, To: to, By: by, Note: note})
}

func clone(p *Proposal) Proposal {
	b, _ := json.Marshal(p)
	var c Proposal
	_ = json.Unmarshal(b, &c)
	return c
}

// Create adds a pending proposal. If one is already pending for the same
// action it is returned instead.
func (s *Store) Create(p Proposal, by string) (Proposal, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.s.Proposals {
		if x.Action == p.Action && x.Status == Pending {
			return clone(x), false, nil
		}
	}
	p.ID = newID()
	p.Status = Pending
	p.CreatedAt = s.now().UTC()
	p.CreatedBy = by
	np := p
	s.s.Proposals = append(s.s.Proposals, &np)
	s.audit(&np, "", Pending, by, "proposed")
	return clone(&np), true, s.save()
}

func (s *Store) find(id string) (*Proposal, error) {
	for _, p := range s.s.Proposals {
		if p.ID == id {
			return p, nil
		}
	}
	return nil, ErrNotFound
}

func (s *Store) Get(id string) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.find(id)
	if err != nil {
		return Proposal{}, err
	}
	return clone(p), nil
}

// List returns proposals newest first.
func (s *Store) List() []Proposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Proposal, 0, len(s.s.Proposals))
	for _, p := range s.s.Proposals {
		out = append(out, clone(p))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (s *Store) Audit() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.s.Audit...)
}

// Decide approves or rejects a pending proposal.
func (s *Store) Decide(id string, approve bool, by, reason string) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.find(id)
	if err != nil {
		return Proposal{}, err
	}
	if p.Status != Pending {
		return Proposal{}, fmt.Errorf("proposal %s is %s, not pending", id, p.Status)
	}
	to := Rejected
	if approve {
		to = Approved
	}
	now := s.now().UTC()
	p.Status, p.DecidedAt, p.DecidedBy, p.Reason = to, &now, by, reason
	s.audit(p, Pending, to, by, reason)
	return clone(p), s.save()
}

// Update applies f to a proposal under the lock (for Keep refs).
func (s *Store) Update(id string, f func(*Proposal)) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.find(id)
	if err != nil {
		return Proposal{}, err
	}
	f(p)
	return clone(p), s.save()
}

// Complete records the execution result of an approved proposal.
func (s *Store) Complete(id string, res executor.Result, by string) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.find(id)
	if err != nil {
		return Proposal{}, err
	}
	if p.Status != Approved {
		return Proposal{}, fmt.Errorf("proposal %s is %s, not approved", id, p.Status)
	}
	to := Executed
	note := string(res.Mode)
	if !res.OK {
		to = Failed
		note += ": " + res.Error
	}
	now := s.now().UTC()
	r := res
	p.Status, p.Execution, p.ExecutedAt = to, &r, &now
	s.audit(p, Approved, to, by, note)
	return clone(p), s.save()
}

// RecordActual stores post-execution KPI values for predicted-vs-actual.
func (s *Store) RecordActual(id string, actual map[string]float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.find(id)
	if err != nil {
		return err
	}
	p.Actual = actual
	return s.save()
}
