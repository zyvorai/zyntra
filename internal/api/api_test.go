// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zyvorai/zyntra/internal/adapters"
	"github.com/zyvorai/zyntra/internal/approvals"
	"github.com/zyvorai/zyntra/internal/auth"
	"github.com/zyvorai/zyntra/internal/executor"
	"github.com/zyvorai/zyntra/internal/graph"
)

const model = `
name: test
kpis:
  - {id: lat, value: 400, target: 300, direction: lower}
actions:
  - {id: cache, name: Cache, effects: [{kpi: lat, change: -0.3}]}
  - id: prio
    name: Priority
    risk: medium
    effects: [{kpi: lat, change: -0.3}]
    execute: {template: gravia.priority, params: {name: zyntra-test, value: "1000"}}
  - id: broken
    name: Broken
    effects: [{kpi: lat, change: -0.3}]
    execute: {template: gravia.priority, params: {name: Not_Valid, value: "1"}}
`

type runner struct {
	mu    sync.Mutex
	calls [][]string
}

func (r *runner) run(_ context.Context, args []string, _ string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, args)
	return "gryviapriority.gryvia.io/zyntra-test created (server dry run)", nil
}

type fixture struct {
	ts  *httptest.Server
	s   *Server
	run *runner
}

func setup(t *testing.T, mutate func(*Options)) *fixture {
	t.Helper()
	m, err := graph.Parse([]byte(model))
	if err != nil {
		t.Fatal(err)
	}
	store, _ := approvals.Open("")
	r := &runner{}
	o := Options{
		Model: m, Interval: 50 * time.Millisecond, Store: store,
		Auth:     auth.New("k3y", "ex3c"),
		Executor: &executor.Executor{Mode: executor.ModeDryRun, Run: r.run},
		Static:   fstest.MapFS{"index.html": {Data: []byte("<html>zyntra</html>")}, "assets/a.js": {Data: []byte("x")}},
		Version:  "test",
	}
	if mutate != nil {
		mutate(&o)
	}
	s := New(o)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Wait() })
	return &fixture{ts: ts, s: s, run: r}
}

func (f *fixture) do(t *testing.T, method, path, token, body string, out any) int {
	t.Helper()
	req, _ := http.NewRequest(method, f.ts.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
	return resp.StatusCode
}

func TestAuthAndPublicRoutes(t *testing.T) {
	f := setup(t, nil)
	for _, p := range []string{"/healthz", "/api/v1/meta", "/", "/approvals", "/assets/a.js"} {
		if c := f.do(t, "GET", p, "", "", nil); c != 200 {
			t.Errorf("GET %s anonymous: %d", p, c)
		}
	}
	for _, p := range []string{"/api/v1/gaps", "/api/v1/plan", "/api/v1/graph", "/api/v1/proposals", "/api/v1/ai/digest"} {
		if c := f.do(t, "GET", p, "", "", nil); c != 401 {
			t.Errorf("GET %s anonymous: %d, want 401", p, c)
		}
		if c := f.do(t, "GET", p, "ex3c", "", nil); c != 403 {
			t.Errorf("GET %s with exec token: %d, want 403", p, c)
		}
	}
	if c := f.do(t, "GET", "/api/v1/nope", "k3y", "", nil); c != 404 {
		t.Errorf("unknown api route: %d", c)
	}
	var meta map[string]any
	f.do(t, "GET", "/api/v1/meta", "", "", &meta)
	if meta["auth_required"] != true || meta["execute_mode"] != "dry-run" {
		t.Fatalf("meta %+v", meta)
	}
}

func TestDecisionEndpoints(t *testing.T) {
	f := setup(t, nil)
	var g struct {
		Gaps []struct{ KPI string } `json:"gaps"`
	}
	f.do(t, "GET", "/api/v1/gaps", "k3y", "", &g)
	if len(g.Gaps) != 1 || g.Gaps[0].KPI != "lat" {
		t.Fatalf("gaps = %+v", g)
	}
	var p struct {
		Recommendations []struct{ Action, Status string } `json:"recommendations"`
	}
	f.do(t, "GET", "/api/v1/plan", "k3y", "", &p)
	if len(p.Recommendations) != 3 || p.Recommendations[0].Status != "pending-approval" {
		t.Fatalf("plan = %+v", p)
	}
	var r struct {
		GapsClosed []string `json:"gaps_closed"`
	}
	if c := f.do(t, "POST", "/api/v1/simulate", "k3y", `{"action":"cache"}`, &r); c != 200 || len(r.GapsClosed) != 1 {
		t.Fatalf("simulate %d %+v", c, r)
	}
	custom := `{"custom":{"id":"x","name":"x","effects":[{"kpi":"lat","change":-0.5}]}}`
	if c := f.do(t, "POST", "/api/v1/simulate", "k3y", custom, &r); c != 200 || len(r.GapsClosed) != 1 {
		t.Fatalf("custom simulate %d %+v", c, r)
	}
	for _, body := range []string{`{"action":"nope"}`, `{}`, `not json`} {
		if c := f.do(t, "POST", "/api/v1/simulate", "k3y", body, nil); c != 400 {
			t.Errorf("%s: status %d, want 400", body, c)
		}
	}
	var a struct{ Text, Intent, Mode string }
	if c := f.do(t, "POST", "/api/v1/ai/ask", "k3y", `{"question":"what should we do first?"}`, &a); c != 200 || a.Intent != "plan" || a.Mode != "heuristic" {
		t.Fatalf("ask %d %+v", c, a)
	}
	if c := f.do(t, "POST", "/api/v1/ai/ask", "k3y", `{"question":""}`, nil); c != 400 {
		t.Fatalf("empty question: %d", c)
	}
	if c := f.do(t, "POST", "/api/v1/ai/explain", "k3y", `{"action":"prio"}`, &a); c != 200 || a.Text == "" {
		t.Fatalf("explain %d %+v", c, a)
	}
	var h struct {
		Points []any `json:"points"`
	}
	if c := f.do(t, "GET", "/api/v1/kpis/lat/history", "k3y", "", &h); c != 200 || len(h.Points) == 0 {
		t.Fatalf("history %d %+v", c, h)
	}
}

func TestApprovalLifecycle(t *testing.T) {
	f := setup(t, nil)
	var p approvals.Proposal
	if c := f.do(t, "POST", "/api/v1/proposals", "k3y", `{"action":"prio"}`, &p); c != 201 {
		t.Fatalf("propose: %d", c)
	}
	if p.Status != approvals.Pending || !strings.Contains(p.Render, "kind: GryviaPriority") || p.Predicted.KPIs["lat"] != 280 {
		t.Fatalf("proposal %+v", p)
	}
	var dup approvals.Proposal
	if c := f.do(t, "POST", "/api/v1/proposals", "k3y", `{"action":"prio"}`, &dup); c != 200 || dup.ID != p.ID {
		t.Fatalf("duplicate proposal: %d %s", c, dup.ID)
	}
	if c := f.do(t, "POST", "/api/v1/exec/"+p.ID, "ex3c", "", nil); c != 409 {
		t.Fatalf("exec before approval: %d", c)
	}
	var done approvals.Proposal
	if c := f.do(t, "POST", "/api/v1/proposals/"+p.ID+"/approve", "k3y", `{"reason":"ship it"}`, &done); c != 200 {
		t.Fatalf("approve: %d", c)
	}
	if done.Status != approvals.Executed || done.Execution == nil || done.Execution.Mode != executor.ModeDryRun {
		t.Fatalf("after approve %+v", done)
	}
	if len(f.run.calls) != 1 || !strings.Contains(strings.Join(f.run.calls[0], " "), "--dry-run=server") {
		t.Fatalf("kubectl calls %v", f.run.calls)
	}
	if c := f.do(t, "POST", "/api/v1/proposals/"+p.ID+"/reject", "k3y", "", nil); c != 409 {
		t.Fatalf("reject after execute: %d", c)
	}

	var b approvals.Proposal
	f.do(t, "POST", "/api/v1/proposals", "k3y", `{"action":"broken"}`, &b)
	if b.RenderErr == "" {
		t.Fatalf("broken action should not render: %+v", b)
	}
	if c := f.do(t, "POST", "/api/v1/proposals/"+b.ID+"/approve", "k3y", "", nil); c != 422 {
		t.Fatalf("approve unrenderable: %d", c)
	}
	var rej approvals.Proposal
	if c := f.do(t, "POST", "/api/v1/proposals/"+b.ID+"/reject", "k3y", `{"reason":"bad params"}`, &rej); c != 200 || rej.Status != approvals.Rejected {
		t.Fatalf("reject: %d %+v", c, rej)
	}

	var adv approvals.Proposal
	f.do(t, "POST", "/api/v1/proposals", "k3y", `{"action":"cache"}`, &adv)
	if c := f.do(t, "POST", "/api/v1/proposals/"+adv.ID+"/approve", "k3y", "", &adv); c != 200 || adv.Status != approvals.Approved {
		t.Fatalf("advisory approve: %d %+v", c, adv)
	}

	var audit struct {
		Events []approvals.Event `json:"events"`
	}
	f.do(t, "GET", "/api/v1/audit", "k3y", "", &audit)
	if len(audit.Events) < 6 {
		t.Fatalf("audit events %d", len(audit.Events))
	}
	if c := f.do(t, "GET", "/api/v1/proposals/nope", "k3y", "", nil); c != 404 {
		t.Fatalf("missing proposal: %d", c)
	}
}

type fakeKeep struct {
	mu       sync.Mutex
	started  []string
	decided  []string
	mirrored []string
	execURL  string
	f        *fixture
}

func (k *fakeKeep) Status(context.Context) (map[string]any, error) {
	return map[string]any{"agent_deployed": true}, nil
}

func (k *fakeKeep) Start(_ context.Context, p approvals.Proposal, execURL string) (approvals.KeepRef, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.started = append(k.started, p.ID)
	k.execURL = execURL
	return approvals.KeepRef{Mode: "keep", SessionID: "sess-" + p.ID}, nil
}

func (k *fakeKeep) AwaitApproval(_ context.Context, session string) (string, error) {
	return "appr-" + strings.TrimPrefix(session, "sess-"), nil
}

// Decide plays Keep's broker: once approved, it calls the exec endpoint with
// the exec token.
func (k *fakeKeep) Decide(_ context.Context, id string, approve bool) error {
	k.mu.Lock()
	k.decided = append(k.decided, id)
	url := k.execURL
	k.mu.Unlock()
	if !approve {
		return nil
	}
	req, _ := http.NewRequest("POST", url, nil)
	req.Header.Set("Authorization", "Bearer ex3c")
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}
	return err
}

func (k *fakeKeep) Mirror(_ context.Context, p approvals.Proposal) (approvals.KeepRef, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.mirrored = append(k.mirrored, p.ID+":"+string(p.Status))
	return approvals.KeepRef{Mode: "mirror", SessionID: "rec"}, nil
}

func (k *fakeKeep) Approvals(context.Context) (any, error) {
	return map[string]any{"items": []any{}}, nil
}
func (k *fakeKeep) Receipts(context.Context, string) (any, error) {
	return map[string]any{"items": []any{}}, nil
}
func (k *fakeKeep) Audit(context.Context, string) (any, error) {
	return map[string]any{"items": []any{}}, nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestKeepApproval(t *testing.T) {
	k := &fakeKeep{}
	var f *fixture
	f = setup(t, func(o *Options) {
		o.Keep = k
		o.ApprovalMode = ModeKeep
	})
	f.s.opt.ExecURL = f.ts.URL
	var p approvals.Proposal
	f.do(t, "POST", "/api/v1/proposals", "k3y", `{"action":"prio"}`, &p)
	var got approvals.Proposal
	if c := f.do(t, "POST", "/api/v1/proposals/"+p.ID+"/approve", "k3y", "", &got); c != 202 {
		t.Fatalf("keep approve: %d", c)
	}
	if got.Status != approvals.Approved || got.Keep == nil || got.Keep.SessionID != "sess-"+p.ID {
		t.Fatalf("after keep approve %+v", got)
	}
	waitFor(t, "keep execution", func() bool {
		x, _ := f.s.opt.Store.Get(p.ID)
		return x.Status == approvals.Executed
	})
	x, _ := f.s.opt.Store.Get(p.ID)
	if x.Keep.ApprovalID != "appr-"+p.ID || x.Keep.Error != "" {
		t.Fatalf("keep ref %+v", x.Keep)
	}
	ev := f.s.opt.Store.Audit()
	if last := ev[len(ev)-1]; last.By != "keep-broker" || last.To != approvals.Executed {
		t.Fatalf("execution should be attributed to the Keep broker: %+v", last)
	}

	var r approvals.Proposal
	f.do(t, "POST", "/api/v1/proposals", "k3y", `{"action":"cache"}`, &r)
	f.do(t, "POST", "/api/v1/proposals/"+r.ID+"/reject", "k3y", "", nil)
	f.s.Wait()
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.mirrored) != 1 || k.mirrored[0] != r.ID+":rejected" {
		t.Fatalf("mirrored %v (keep-executed proposals must not be mirrored)", k.mirrored)
	}
	var st map[string]any
	if c := f.do(t, "GET", "/api/v1/keep/status", "k3y", "", &st); c != 200 || st["configured"] != true {
		t.Fatalf("keep status %d %+v", c, st)
	}
}

func TestExecHandlerOnlyServesExec(t *testing.T) {
	f := setup(t, nil)
	ts := httptest.NewServer(f.s.ExecHandler())
	defer ts.Close()
	for path, want := range map[string]int{"/api/v1/gaps": 404, "/api/v1/meta": 404, "/": 404, "/healthz": 200} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("exec listener GET %s: %d want %d", path, resp.StatusCode, want)
		}
	}
	resp, _ := http.Post(ts.URL+"/api/v1/exec/x", "application/json", nil)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("anonymous exec: %d", resp.StatusCode)
	}
}

func TestEventsStream(t *testing.T) {
	f := setup(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.ts.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer k3y")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	pulses := 0
	for sc.Scan() && pulses < 2 {
		if strings.HasPrefix(sc.Text(), "event: pulse") {
			pulses++
		}
	}
	if pulses < 2 {
		t.Fatalf("got %d pulses", pulses)
	}
}

func TestRefreshUpdatesModelAndSources(t *testing.T) {
	m, _ := graph.Parse([]byte(`kpis: [{id: a, value: 1}]`))
	s := New(Options{Model: m, Interval: time.Hour, Refresh: func(_ context.Context, m *graph.Model) ([]adapters.Status, error) {
		m.KPIs[0].Value = 42
		return []adapters.Status{{Name: "netra", Kind: "metrics", OK: true, KPIs: []string{"a"}}}, nil
	}})
	s.RefreshOnce(context.Background())
	got, _ := s.snapshot()
	if got.KPIs[0].Value != 42 {
		t.Fatalf("value = %v", got.KPIs[0].Value)
	}
	if src := s.sourceStatus(); len(src) != 1 || src[0].Name != "netra" {
		t.Fatalf("sources %+v", src)
	}
}
