// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package notify tells a person that a proposal needs them. It posts one small
// JSON message to a webhook the operator configures (Slack and Teams
// "incoming webhooks" accept the "text" field). It never blocks an approval:
// events are queued, sent in the background, retried a few times and dropped
// when the queue is full.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/zyvorai/zyntra/internal/approvals"
)

// Config says where to post and which transitions to announce.
type Config struct {
	URL   string
	Token string // sent as a bearer token to URL only
	// On lists the statuses to announce. Empty means pending only.
	On []approvals.Status
	// ConsoleURL, when set, turns each message into a link to the proposal.
	ConsoleURL string
}

// Message is the JSON body. It carries the transition and who made it, never
// the action's inputs, the rendered change or any free text.
type Message struct {
	Text     string    `json:"text"`
	Proposal string    `json:"proposal"`
	Action   string    `json:"action"`
	From     string    `json:"from,omitempty"`
	To       string    `json:"to"`
	By       string    `json:"by,omitempty"`
	At       time.Time `json:"at"`
	Link     string    `json:"link,omitempty"`
}

// Notifier queues and sends messages.
type Notifier struct {
	cfg     Config
	on      map[approvals.Status]bool
	queue   chan Message
	client  *http.Client
	backoff []time.Duration
	sent    atomic.Int64
	dropped atomic.Int64
	failed  atomic.Int64
}

const queueSize = 256

// New validates cfg and starts the sender; it stops when ctx ends.
func New(ctx context.Context, cfg Config) (*Notifier, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("notify: the URL must be http or https with a host")
	}
	if cfg.ConsoleURL != "" {
		if c, err := url.Parse(cfg.ConsoleURL); err != nil || (c.Scheme != "http" && c.Scheme != "https") || c.Host == "" {
			return nil, errors.New("notify: the console URL must be http or https with a host")
		}
	}
	n := &Notifier{
		cfg: cfg, on: map[approvals.Status]bool{}, queue: make(chan Message, queueSize),
		backoff: []time.Duration{time.Second, 4 * time.Second},
		client: &http.Client{
			Timeout: 5 * time.Second,
			// A redirect could carry the token to another host; treat it as a failure.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	on := cfg.On
	if len(on) == 0 {
		on = []approvals.Status{approvals.Pending}
	}
	for _, s := range on {
		n.on[s] = true
	}
	go n.run(ctx)
	return n, nil
}

// ParseStatuses reads a comma-separated list such as "pending,executed,failed".
func ParseStatuses(list string) ([]approvals.Status, error) {
	valid := map[string]approvals.Status{}
	for _, s := range []approvals.Status{approvals.Pending, approvals.Approved, approvals.Rejected, approvals.Expired, approvals.Blocked, approvals.Executed, approvals.Failed} {
		valid[string(s)] = s
	}
	var out []approvals.Status
	for _, f := range strings.Split(list, ",") {
		f = strings.ToLower(strings.TrimSpace(f))
		if f == "" {
			continue
		}
		s, ok := valid[f]
		if !ok {
			return nil, fmt.Errorf("notify: unknown status %q", f)
		}
		out = append(out, s)
	}
	return out, nil
}

// Event is the approvals.Store hook. It never blocks.
func (n *Notifier) Event(e approvals.Event) {
	if e.Proposal == "" || !n.on[e.To] {
		return
	}
	m := Message{Proposal: e.Proposal, Action: e.Action, From: string(e.From), To: string(e.To), By: e.By, At: e.At}
	if n.cfg.ConsoleURL != "" {
		m.Link = strings.TrimRight(n.cfg.ConsoleURL, "/") + "/#/approvals"
	}
	m.Text = text(m)
	select {
	case n.queue <- m:
	default:
		n.dropped.Add(1)
	}
}

func text(m Message) string {
	switch m.To {
	case string(approvals.Pending):
		return fmt.Sprintf("Zyntra: %s needs a decision (proposal %s)", m.Action, m.Proposal)
	default:
		return fmt.Sprintf("Zyntra: %s is now %s (proposal %s)", m.Action, m.To, m.Proposal)
	}
}

// Stats reports delivery counts.
func (n *Notifier) Stats() (sent, failed, dropped int64) {
	return n.sent.Load(), n.failed.Load(), n.dropped.Load()
}

func (n *Notifier) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-n.queue:
			if err := n.deliver(ctx, m); err != nil {
				n.failed.Add(1)
			} else {
				n.sent.Add(1)
			}
		}
	}
}

func (n *Notifier) deliver(ctx context.Context, m Message) error {
	body, _ := json.Marshal(m)
	var err error
	for attempt := 0; ; attempt++ {
		if err = n.post(ctx, body); err == nil {
			return nil
		}
		if attempt >= len(n.backoff) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(n.backoff[attempt]):
		}
	}
}

func (n *Notifier) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return errors.New("notify: bad request")
	}
	req.Header.Set("Content-Type", "application/json")
	if n.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+n.cfg.Token)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return errors.New("notify: request failed") // the URL may hold a secret
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("notify: webhook answered %d", resp.StatusCode)
	}
	return nil
}
