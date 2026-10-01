// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package keep

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	keeppack "github.com/zyvorai/zyntra/keep"

	"github.com/zyvorai/zyntra/internal/approvals"
)

// Fixture shared with Fabric Keep (agent-runtime/src/policy.rs) so a Go
// signature is byte-identical to the Rust and Node signers.
func TestSignMatchesKeep(t *testing.T) {
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed")
	if err := os.WriteFile(seed, []byte(strings.Repeat("07", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	priv, err := LoadSigner(seed)
	if err != nil {
		t.Fatal(err)
	}
	if got := PublicHex(priv); got != "ea4a6c63e29c520abef5507b132ec5f9954776aebebe7b92421eea691446d22c" {
		t.Fatalf("pubkey %s", got)
	}
	want := "5665d748703e5d6db9c7a2b7f9bf1623c33dc4d77767527ea4a4b530a01e36429a30f5a0b6998e3ca286cab2f1103f2f4a5540add46c04fa3d82686748492508"
	if got := Sign(priv, []byte("version: 1\ndefault_egress: deny\n")); got != want {
		t.Fatalf("signature %s", got)
	}
	_ = os.WriteFile(seed, []byte("nothex"), 0o600)
	if _, err := LoadSigner(seed); err == nil {
		t.Fatal("bad seed accepted")
	}
}

type fakeKeep struct {
	mu        sync.Mutex
	pub       ed25519.PublicKey
	agent     map[string]any
	sessions  map[string]string
	approvals []Approval
	decisions map[string]string
}

func (k *fakeKeep) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" {
		http.Error(w, `{"error":"missing or invalid bearer token"}`, 401)
		return
	}
	js := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == "POST" && r.URL.Path == "/v1/agents":
		sig, _ := hex.DecodeString(r.Header.Get("X-Keep-Manifest-Signature"))
		if !ed25519.Verify(k.pub, body, sig) {
			js(403, map[string]string{"error": "manifest signature invalid"})
			return
		}
		_ = json.Unmarshal(body, &k.agent)
		js(201, map[string]any{"name": k.agent["name"], "version": "abc123def456"})
	case r.Method == "GET" && r.URL.Path == "/v1/agents/"+AgentName:
		if k.agent == nil {
			js(404, map[string]string{"error": "agent not found"})
			return
		}
		js(200, map[string]any{"name": AgentName, "version": "abc123def456"})
	case r.Method == "GET" && r.URL.Path == "/v1/keep/status":
		js(200, map[string]any{"keep_mode": true, "fluxvm": map[string]any{"ready": true}})
	case r.Method == "POST" && r.URL.Path == "/v1/sessions":
		var in struct {
			Agent     string         `json:"agent"`
			Input     map[string]any `json:"input"`
			RequestID string         `json:"request_id"`
		}
		_ = json.Unmarshal(body, &in)
		id := "s-" + in.RequestID
		k.sessions[id] = "running"
		if in.Input["mode"] == "execute" {
			k.approvals = append(k.approvals, Approval{ID: "a-" + id, SessionID: id, Kind: "send", Status: "pending", BrokerHeld: true})
		}
		js(201, map[string]any{"id": id, "agent": in.Agent, "status": "creating"})
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/sessions/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/sessions/")
		js(200, map[string]any{"id": id, "status": k.sessions[id]})
	case r.Method == "GET" && r.URL.Path == "/v1/approvals":
		js(200, map[string]any{"items": k.approvals})
	case r.Method == "POST" && r.URL.Path == "/v1/approvals":
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		a := Approval{ID: "c-" + in["session_id"].(string), SessionID: in["session_id"].(string), Kind: in["kind"].(string), Status: "pending", Prompt: in["prompt"].(string)}
		k.approvals = append(k.approvals, a)
		js(201, a)
	case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v1/approvals/"):
		var in map[string]string
		_ = json.Unmarshal(body, &in)
		k.decisions[strings.TrimPrefix(r.URL.Path, "/v1/approvals/")] = in["decision"] + "|" + in["comment"]
		js(200, map[string]any{"ok": true})
	case r.Method == "GET" && r.URL.Path == "/v1/receipts":
		js(200, map[string]any{"items": []map[string]any{{"id": "r1", "agent": AgentName, "session_id": "s1"}, {"id": "r2", "agent": "other"}}})
	default:
		js(404, map[string]string{"error": "no route"})
	}
}

func TestBridge(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	fk := &fakeKeep{pub: pub, sessions: map[string]string{}, decisions: map[string]string{}}
	ts := httptest.NewServer(fk)
	defer ts.Close()
	c := New(ts.URL, "tok")
	c.Poll = 5 * time.Millisecond
	b := NewBridge(c)
	ctx := context.Background()

	st, err := b.Status(ctx)
	if err != nil || st["agent_deployed"] != false {
		t.Fatalf("status before deploy %v %v", st, err)
	}
	pack, err := keeppack.Executor()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Deploy(ctx, priv, pack.Name, pack.Bundle, pack.Manifest); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	_, other, _ := ed25519.GenerateKey(nil)
	if _, err := c.Deploy(ctx, other, pack.Name, pack.Bundle, pack.Manifest); !isStatus(err, 403) {
		t.Fatalf("deploy with untrusted key: %v", err)
	}
	if st, _ := b.Status(ctx); st["agent_deployed"] != true {
		t.Fatalf("status after deploy %v", st)
	}

	p := approvals.Proposal{ID: "p1", Action: "prio", ActionName: "Priority", Status: approvals.Approved, DecidedBy: "ana"}
	ref, err := b.Start(ctx, p, "https://127.0.0.1:19621/api/v1/exec/p1")
	if err != nil || ref.SessionID != "s-zyntra-exec-p1" {
		t.Fatalf("start %+v %v", ref, err)
	}
	aid, err := b.AwaitApproval(ctx, ref.SessionID)
	if err != nil || aid != "a-s-zyntra-exec-p1" {
		t.Fatalf("await %q %v", aid, err)
	}
	if err := b.Decide(ctx, aid, true); err != nil || fk.decisions[aid] != "approved|approved in Zyntra" {
		t.Fatalf("decide %v %v", err, fk.decisions)
	}

	p.Status, p.Reason = approvals.Rejected, "too risky"
	m, err := b.Mirror(ctx, p)
	if err != nil || m.ApprovalID == "" {
		t.Fatalf("mirror %+v %v", m, err)
	}
	if got := fk.decisions[m.ApprovalID]; got != "denied|mirrored from Zyntra: too risky" {
		t.Fatalf("mirror decision %q", got)
	}

	r, _ := b.Receipts(ctx, "")
	if items := r.(map[string]any)["items"].([]receipt); len(items) != 1 || items[0].ID != "r1" {
		t.Fatalf("receipts should be filtered to the executor agent: %+v", items)
	}

	ctx2, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := b.AwaitApproval(ctx2, "no-such-session"); err == nil {
		t.Fatal("await on a session without approvals should time out")
	}
	if _, err := New(ts.URL, "wrong").Approvals(ctx); !isStatus(err, 401) {
		t.Fatalf("bad token: %v", err)
	}
}
