// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package keep talks to Fabric Keep (the Zyvor agent runtime): it deploys the
// signed zyntra-executor agent, runs approved proposals through Keep's
// broker, and records Zyntra decisions in Keep's hash-chained audit.
package keep

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/zyvorai/zyntra/internal/approvals"
)

// AgentName is the Keep agent that executes Zyntra proposals.
const AgentName = "zyntra-executor"

// LoadSigner reads a Keep signer seed: 64 hex characters (a 32-byte Ed25519
// seed), as written by Fabric's deploy-keep.sh.
func LoadSigner(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s: want %d hex-encoded seed bytes", path, ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// PublicHex is the trusted-signer id Keep expects for priv.
func PublicHex(priv ed25519.PrivateKey) string {
	return hex.EncodeToString(priv.Public().(ed25519.PublicKey))
}

// Sign signs the exact bytes of a request body for X-Keep-Manifest-Signature.
func Sign(priv ed25519.PrivateKey, body []byte) string {
	return hex.EncodeToString(ed25519.Sign(priv, body))
}

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
	// Poll is how often approvals and sessions are polled.
	Poll time.Duration
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 20 * time.Second},
		Poll:    time.Second,
	}
}

// APIError is a non-2xx answer from Keep.
type APIError struct {
	Status int
	Msg    string
}

func (e *APIError) Error() string { return fmt.Sprintf("keep: HTTP %d: %s", e.Status, e.Msg) }

func (c *Client) do(ctx context.Context, method, path string, body []byte, hdr map[string]string, out any) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("keep: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(b))
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return &APIError{Status: resp.StatusCode, Msg: msg}
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, nil, out)
}

func (c *Client) postJSON(ctx context.Context, path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, path, b, nil, out)
}

type Agent struct {
	Name      string          `json:"name"`
	Version   string          `json:"version"`
	Digest    string          `json:"digest_sha256"`
	Manifest  json.RawMessage `json:"manifest"`
	CreatedAt time.Time       `json:"created_at"`
}

// Deploy uploads a signed agent bundle. The signature covers the exact
// request body bytes.
func (c *Client) Deploy(ctx context.Context, priv ed25519.PrivateKey, name string, bundle []byte, manifest json.RawMessage) (Agent, error) {
	body, err := json.Marshal(map[string]any{
		"name": name, "bundle_base64": base64.StdEncoding.EncodeToString(bundle), "manifest": manifest,
	})
	if err != nil {
		return Agent{}, err
	}
	var a Agent
	err = c.do(ctx, http.MethodPost, "/v1/agents", body, map[string]string{"X-Keep-Manifest-Signature": Sign(priv, body)}, &a)
	return a, err
}

func (c *Client) Agent(ctx context.Context, name string) (Agent, error) {
	var a Agent
	err := c.getJSON(ctx, "/v1/agents/"+url.PathEscape(name), &a)
	return a, err
}

type Session struct {
	ID     string `json:"id"`
	Agent  string `json:"agent"`
	Status string `json:"status"`
	Error  string `json:"error"`
}

func (s Session) Terminal() bool {
	switch s.Status {
	case "completed", "failed", "cancelled", "expired":
		return true
	}
	return false
}

func (c *Client) StartSession(ctx context.Context, agent string, input any, requestID string) (Session, error) {
	var s Session
	err := c.postJSON(ctx, "/v1/sessions", map[string]any{
		"agent": agent, "input": input, "request_id": requestID, "ttl_seconds": 600,
	}, &s)
	return s, err
}

func (c *Client) Session(ctx context.Context, id string) (Session, error) {
	var s Session
	err := c.getJSON(ctx, "/v1/sessions/"+url.PathEscape(id), &s)
	return s, err
}

type Approval struct {
	ID         string `json:"id"`
	SessionID  string `json:"session_id"`
	Kind       string `json:"kind"`
	Subject    string `json:"subject"`
	Prompt     string `json:"prompt"`
	Status     string `json:"status"`
	BrokerHeld bool   `json:"broker_held"`
}

func (c *Client) Approvals(ctx context.Context) ([]Approval, error) {
	var out struct {
		Items []Approval `json:"items"`
	}
	err := c.getJSON(ctx, "/v1/approvals", &out)
	return out.Items, err
}

func (c *Client) CreateApproval(ctx context.Context, session, kind, subject, prompt string, planned any) (Approval, error) {
	var a Approval
	err := c.postJSON(ctx, "/v1/approvals", map[string]any{
		"session_id": session, "kind": kind, "subject": subject, "prompt": prompt, "planned_action": planned,
	}, &a)
	return a, err
}

func (c *Client) Decide(ctx context.Context, id string, approve bool, comment string) error {
	d := "denied"
	if approve {
		d = "approved"
	}
	return c.postJSON(ctx, "/v1/approvals/"+url.PathEscape(id), map[string]any{"decision": d, "comment": comment}, nil)
}

// WaitRunning polls a session until the sandbox is running.
func (c *Client) WaitRunning(ctx context.Context, id string) (Session, error) {
	for {
		s, err := c.Session(ctx, id)
		if err != nil {
			return s, err
		}
		if s.Status == "running" {
			return s, nil
		}
		if s.Terminal() {
			return s, fmt.Errorf("keep session %s %s: %s", id, s.Status, s.Error)
		}
		select {
		case <-ctx.Done():
			return s, fmt.Errorf("keep session %s still %s: %w", id, s.Status, ctx.Err())
		case <-time.After(c.Poll):
		}
	}
}

// Bridge implements api.KeepBridge on top of Client.
type Bridge struct {
	C     *Client
	Agent string
	// Comment is attached to approvals Zyntra decides.
	Comment string
}

func NewBridge(c *Client) *Bridge {
	return &Bridge{C: c, Agent: AgentName, Comment: "approved in Zyntra"}
}

func (b *Bridge) Status(ctx context.Context) (map[string]any, error) {
	out := map[string]any{"url": b.C.BaseURL, "agent": b.Agent}
	var st map[string]any
	if err := b.C.getJSON(ctx, "/v1/keep/status", &st); err != nil {
		return out, err
	}
	out["keep"] = st
	a, err := b.C.Agent(ctx, b.Agent)
	switch {
	case err == nil:
		out["agent_version"] = a.Version
		out["agent_deployed"] = true
	case isStatus(err, http.StatusNotFound):
		out["agent_deployed"] = false
	default:
		return out, err
	}
	return out, nil
}

func isStatus(err error, code int) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == code
}

func (b *Bridge) Start(ctx context.Context, p approvals.Proposal, execURL string) (approvals.KeepRef, error) {
	s, err := b.C.StartSession(ctx, b.Agent, map[string]any{
		"mode": "execute", "proposal_id": p.ID, "action": p.Action, "url": execURL, "approved_by": p.DecidedBy,
	}, "zyntra-exec-"+p.ID)
	if err != nil {
		return approvals.KeepRef{}, err
	}
	return approvals.KeepRef{Mode: "keep", SessionID: s.ID}, nil
}

// AwaitApproval waits for the broker-held approval the executor's call raises.
func (b *Bridge) AwaitApproval(ctx context.Context, session string) (string, error) {
	for i := 0; ; i++ {
		list, err := b.C.Approvals(ctx)
		if err != nil {
			return "", err
		}
		for _, a := range list {
			if a.SessionID == session && a.Status == "pending" {
				return a.ID, nil
			}
		}
		if i%5 == 4 {
			if s, err := b.C.Session(ctx, session); err == nil && s.Terminal() {
				return "", fmt.Errorf("keep session %s ended %s before asking for approval: %s", session, s.Status, s.Error)
			}
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("no Keep approval for session %s: %w", session, ctx.Err())
		case <-time.After(b.C.Poll):
		}
	}
}

func (b *Bridge) Decide(ctx context.Context, approvalID string, approve bool) error {
	return b.C.Decide(ctx, approvalID, approve, b.Comment)
}

// Mirror records a Zyntra decision in Keep's audit chain: it starts a
// record-mode executor session, raises a custom approval describing the
// decision, and decides it the same way.
func (b *Bridge) Mirror(ctx context.Context, p approvals.Proposal) (approvals.KeepRef, error) {
	ref := approvals.KeepRef{Mode: "mirror"}
	s, err := b.C.StartSession(ctx, b.Agent, map[string]any{
		"mode": "record", "proposal_id": p.ID, "action": p.Action, "status": p.Status, "by": p.DecidedBy,
	}, "zyntra-rec-"+p.ID+"-"+string(p.Status))
	if err != nil {
		return ref, err
	}
	ref.SessionID = s.ID
	if _, err := b.C.WaitRunning(ctx, s.ID); err != nil {
		return ref, err
	}
	approve := p.Status != approvals.Rejected && p.Status != approvals.Failed
	planned := map[string]any{
		"proposal": p.ID, "action": p.Action, "status": p.Status, "risk": p.Risk, "template": p.Template,
		"severity_before": p.Predicted.SeverityBefore, "severity_after": p.Predicted.SeverityAfter,
	}
	if p.Execution != nil {
		planned["execution_mode"], planned["execution_ok"] = p.Execution.Mode, p.Execution.OK
	}
	prompt := fmt.Sprintf("Zyntra %s %q (%s) — decided by %s", p.Status, p.ActionName, p.ID, orDash(p.DecidedBy))
	a, err := b.C.CreateApproval(ctx, s.ID, "custom", "zyntra/"+p.Action, prompt, planned)
	if err != nil {
		return ref, err
	}
	ref.ApprovalID = a.ID
	comment := "mirrored from Zyntra"
	if p.Reason != "" {
		comment += ": " + p.Reason
	}
	if err := b.C.Decide(ctx, a.ID, approve, comment); err != nil && !isStatus(err, http.StatusConflict) {
		return ref, err
	}
	return ref, nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (b *Bridge) Approvals(ctx context.Context) (any, error) {
	list, err := b.C.Approvals(ctx)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []Approval{}
	}
	return map[string]any{"items": list}, nil
}

type receipt struct {
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	SessionID  string    `json:"session_id"`
	Agent      string    `json:"agent"`
	Credential string    `json:"credential"`
	Method     string    `json:"method"`
	URL        string    `json:"url"`
	ApprovalID string    `json:"approval_id"`
	Status     int       `json:"status"`
}

func (b *Bridge) Receipts(ctx context.Context, session string) (any, error) {
	var out struct {
		Items []receipt `json:"items"`
	}
	if err := b.C.getJSON(ctx, "/v1/receipts?limit=200", &out); err != nil {
		return nil, err
	}
	items := []receipt{}
	for _, r := range out.Items {
		if r.Agent == b.Agent && (session == "" || r.SessionID == session) {
			items = append(items, r)
		}
	}
	return map[string]any{"items": items}, nil
}

func (b *Bridge) Audit(ctx context.Context, session string) (any, error) {
	q := url.Values{"limit": {"200"}}
	if session != "" {
		q.Set("session_id", session)
	}
	var out map[string]any
	err := b.C.getJSON(ctx, "/v1/audit?"+q.Encode(), &out)
	return out, err
}
