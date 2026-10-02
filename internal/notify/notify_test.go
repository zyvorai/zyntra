// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/approvals"
)

func wait(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func TestAnnouncesPendingOnlyByDefaultAndLeaksNoDetail(t *testing.T) {
	var got atomic.Value
	var auth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m Message
		_ = json.NewDecoder(r.Body).Decode(&m)
		got.Store(m)
		auth.Store(r.Header.Get("Authorization"))
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n, err := New(ctx, Config{URL: srv.URL, Token: "s3cret", ConsoleURL: "https://zyntra.example/"})
	if err != nil {
		t.Fatal(err)
	}
	n.Event(approvals.Event{Proposal: "prop-1", Action: "scale-up", From: "", To: approvals.Approved, By: "bo"})
	n.Event(approvals.Event{Proposal: "", Action: "x", To: approvals.Pending})
	n.Event(approvals.Event{Proposal: "prop-2", Action: "scale-up", To: approvals.Pending, By: "ann", Note: "secret reason"})
	wait(t, func() bool { s, _, _ := n.Stats(); return s == 1 })
	m := got.Load().(Message)
	if m.Proposal != "prop-2" || m.To != "pending" || !strings.Contains(m.Text, "needs a decision") {
		t.Fatalf("message: %+v", m)
	}
	if m.Link != "https://zyntra.example/#/approvals" {
		t.Errorf("link: %q", m.Link)
	}
	if b, _ := json.Marshal(m); strings.Contains(string(b), "secret reason") {
		t.Error("a free-text note was sent")
	}
	if auth.Load() != "Bearer s3cret" {
		t.Errorf("auth header: %v", auth.Load())
	}
	if s, f, d := n.Stats(); s != 1 || f != 0 || d != 0 {
		t.Errorf("stats %d %d %d", s, f, d)
	}
}

func TestRetriesThenGivesUpAndNeverLeaksURL(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n, _ := New(ctx, Config{URL: srv.URL + "/hook?token=abc"})
	n.backoff = []time.Duration{time.Millisecond, time.Millisecond}
	n.Event(approvals.Event{Proposal: "p", Action: "a", To: approvals.Pending})
	wait(t, func() bool { s, _, _ := n.Stats(); return s == 1 })
	if calls.Load() != 3 {
		t.Errorf("attempts: %d", calls.Load())
	}

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	url := dead.URL + "/hook?token=abc"
	n2, _ := New(ctx, Config{URL: url})
	n2.backoff = []time.Duration{time.Millisecond, time.Millisecond}
	n2.Event(approvals.Event{Proposal: "p", Action: "a", To: approvals.Pending})
	wait(t, func() bool { _, f, _ := n2.Stats(); return f == 1 })
	dead.Close()
	if err := n2.post(ctx, []byte("{}")); err == nil || strings.Contains(err.Error(), "abc") {
		t.Errorf("error leaks the URL or is missing: %v", err)
	}
}

func TestDoesNotFollowRedirectsWithTheToken(t *testing.T) {
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n, _ := New(ctx, Config{URL: srv.URL, Token: "t"})
	n.backoff = nil
	n.Event(approvals.Event{Proposal: "p", Action: "a", To: approvals.Pending})
	wait(t, func() bool { _, f, _ := n.Stats(); return f == 1 })
	if leaked.Load() {
		t.Error("the redirect was followed")
	}
}

func TestFullQueueDropsInsteadOfBlocking(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n, _ := New(ctx, Config{URL: srv.URL})
	done := make(chan struct{})
	go func() {
		for i := 0; i < queueSize*2+10; i++ {
			n.Event(approvals.Event{Proposal: "p", Action: "a", To: approvals.Pending})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Event blocked on a slow webhook")
	}
	if _, _, d := n.Stats(); d == 0 {
		t.Error("nothing was dropped")
	}
}

func TestConfigValidationAndStatuses(t *testing.T) {
	ctx := context.Background()
	for _, u := range []string{"", "ftp://x/y", "not a url", "http://"} {
		if _, err := New(ctx, Config{URL: u}); err == nil {
			t.Errorf("accepted URL %q", u)
		}
	}
	if _, err := New(ctx, Config{URL: "https://h/x", ConsoleURL: "javascript:alert(1)"}); err == nil {
		t.Error("accepted a bad console URL")
	}
	if _, err := ParseStatuses("pending, bogus"); err == nil {
		t.Error("accepted an unknown status")
	}
	if s, err := ParseStatuses(" Pending,executed ,,failed"); err != nil || len(s) != 3 {
		t.Errorf("%v %v", s, err)
	}
}

func TestStoreHookFiresOnCreate(t *testing.T) {
	st, _ := approvals.Open("")
	var seen []approvals.Event
	st.OnEvent(func(e approvals.Event) { seen = append(seen, e) })
	if _, _, err := st.Create(approvals.Proposal{Action: "a"}, "ann"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].To != approvals.Pending || seen[0].Proposal == "" {
		t.Fatalf("events: %+v", seen)
	}
}
