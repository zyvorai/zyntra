// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package inputs keeps values pushed into Zyntra rather than pulled: the
// last document POSTed to each webhook-in channel and the values operators
// type for manual KPIs. Both survive restarts when a path is given.
package inputs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Doc is the last document received on a webhook-in channel.
type Doc struct {
	Body any       `json:"body"`
	At   time.Time `json:"at"`
	From string    `json:"from,omitempty"`
}

// Manual is an operator-entered KPI value.
type Manual struct {
	Value  float64   `json:"value"`
	At     time.Time `json:"at"`
	By     string    `json:"by"`
	Reason string    `json:"reason,omitempty"`
}

type state struct {
	Webhooks map[string]Doc    `json:"webhooks"`
	Manual   map[string]Manual `json:"manual"`
}

// Store holds pushed inputs. The zero value is not usable; call Open.
type Store struct {
	mu   sync.RWMutex
	path string
	s    state
}

// Open loads the store from path; an empty path keeps it in memory.
func Open(path string) (*Store, error) {
	st := &Store{path: path, s: state{Webhooks: map[string]Doc{}, Manual: map[string]Manual{}}}
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
		return nil, err
	}
	if st.s.Webhooks == nil {
		st.s.Webhooks = map[string]Doc{}
	}
	if st.s.Manual == nil {
		st.s.Manual = map[string]Manual{}
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

// Ingest stores body as the latest document on channel; the last value wins.
func (s *Store) Ingest(channel string, body any, from string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.s.Webhooks[channel] = Doc{Body: body, At: at.UTC(), From: from}
	return s.save()
}

// Webhook returns the latest document on channel.
func (s *Store) Webhook(channel string) (Doc, bool) {
	if s == nil {
		return Doc{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.s.Webhooks[channel]
	return d, ok
}

// SetManual records an operator-entered value for kpi.
func (s *Store) SetManual(kpi string, v Manual) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v.At = v.At.UTC()
	s.s.Manual[kpi] = v
	return s.save()
}

// Manual returns the operator-entered value for kpi.
func (s *Store) Manual(kpi string) (Manual, bool) {
	if s == nil {
		return Manual{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.s.Manual[kpi]
	return v, ok
}

// ManualValues returns every manual entry.
func (s *Store) ManualValues() map[string]Manual {
	out := map[string]Manual{}
	if s == nil {
		return out
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for k, v := range s.s.Manual {
		out[k] = v
	}
	return out
}
