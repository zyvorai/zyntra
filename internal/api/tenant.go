// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/zyvorai/zyntra/internal/approvals"
	"github.com/zyvorai/zyntra/internal/auth"
	"github.com/zyvorai/zyntra/internal/ontology"
	"github.com/zyvorai/zyntra/internal/policy"
)

// A tenant-bound identity is a customer-facing account: it works inside its
// own workspace and nothing else. The gate is deny-by-default, so a route
// added later is closed to tenant accounts until it is listed here.
//
// What a tenant account can do:
//   - read and search its own tenant's objects, history and exposure
//   - ask about those objects and draft typed proposals on them
//   - create, list, approve and reject typed proposals of its own tenant
//   - read its own tenant's decisions and audit entries (trimmed)
//
// It cannot see the KPI model, gaps, plan, simulation, sources, inputs,
// policy, scenarios, other tenants' anything, the global audit chain, signed
// exports, Keep or the event stream.
func tenantAllowed(method, path string) bool {
	if !strings.HasPrefix(path, "/api/") {
		return true // the console shell itself
	}
	switch {
	case path == "/api/v1/whoami" || path == "/api/v1/meta" || path == "/api/v1/session":
		return true
	case strings.HasPrefix(path, "/api/v1/ontology/"):
		return !(method == http.MethodPost && path == "/api/v1/ontology/refresh") &&
			!strings.HasPrefix(path, "/api/v1/ontology/ingest/") &&
			!strings.HasPrefix(path, "/api/v1/ontology/connectors")
	case path == "/api/v1/proposals":
		return method == http.MethodGet || method == http.MethodPost
	case strings.HasPrefix(path, "/api/v1/proposals/"):
		rest := strings.TrimPrefix(path, "/api/v1/proposals/")
		switch {
		case !strings.Contains(rest, "/"):
			return method == http.MethodGet
		case strings.HasSuffix(rest, "/approve"), strings.HasSuffix(rest, "/reject"):
			return method == http.MethodPost
		}
		return false
	case path == "/api/v1/decisions":
		return method == http.MethodGet
	case strings.HasPrefix(path, "/api/v1/decisions/"):
		return method == http.MethodGet && !strings.HasSuffix(path, "/export")
	case path == "/api/v1/audit":
		return method == http.MethodGet
	case path == "/api/v1/ai/ask" || path == "/api/v1/ai/propose":
		return method == http.MethodPost
	}
	return false
}

func (s *Server) tenantGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := s.opt.Auth.Identify(r); id.Tenant != "" && !tenantAllowed(r.Method, r.URL.Path) {
			writeErr(w, http.StatusForbidden, "tenant accounts can only use their own workspace")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// proposalFor loads a proposal the caller may act on. Another tenant's
// proposal looks exactly like a missing one.
func (s *Server) proposalFor(r *http.Request, id string) (approvals.Proposal, error) {
	p, err := s.opt.Store.Get(id)
	if err != nil {
		return p, err
	}
	if t := auth.FromContext(r.Context()).Tenant; t != "" && p.Tenant != t {
		return approvals.Proposal{}, approvals.ErrNotFound
	}
	return p, nil
}

// ownProposals keeps the proposals the caller may see, trimmed for tenants.
func (s *Server) ownProposals(r *http.Request, in []approvals.Proposal) []approvals.Proposal {
	t := auth.FromContext(r.Context()).Tenant
	if t == "" {
		return in
	}
	out := []approvals.Proposal{}
	for _, p := range in {
		if p.Tenant == t {
			out = append(out, s.view(r, p))
		}
	}
	return out
}

// view is the proposal as the caller may see it. Deployment-wide callers get
// it whole. A tenant gets who, what, on which objects and where it stands:
// no KPI values, simulation, rendered payloads, execution output, policy
// internals or other proposals' alternatives.
func (s *Server) view(r *http.Request, p approvals.Proposal) approvals.Proposal {
	if auth.FromContext(r.Context()).Tenant == "" {
		return p
	}
	t := approvals.Proposal{
		ID: p.ID, Action: p.Action, Actions: p.Actions, ActionName: p.ActionName, Risk: p.Risk,
		Tenant: p.Tenant, Status: p.Status, Phase: p.Phase,
		CreatedAt: p.CreatedAt, CreatedBy: p.CreatedBy, ExpiresAt: p.ExpiresAt,
		Approvals: p.Approvals, RequiredApprovals: p.RequiredApprovals,
		DecidedAt: p.DecidedAt, DecidedBy: p.DecidedBy, Reason: p.Reason,
		Objects: p.Objects, ActionInputs: p.ActionInputs, ObjectOutcome: p.ObjectOutcome,
		Baseline: map[string]float64{}, Predicted: approvals.Prediction{KPIs: map[string]float64{}},
	}
	if p.Policy != nil {
		t.Policy = &policy.Effective{Approvals: p.Policy.Approvals, DistinctFromProposer: p.Policy.DistinctFromProposer}
	}
	for _, reason := range p.BlockedReasons {
		if strings.HasPrefix(reason, "business objects:") {
			t.BlockedReasons = append(t.BlockedReasons, reason)
		}
	}
	if p.Status == approvals.Blocked && len(t.BlockedReasons) == 0 {
		t.BlockedReasons = []string{"blocked: conditions changed since the proposal was made"}
	}
	return t
}

// auditView keeps the audit entries the caller may see. Tenants get entries
// of their own proposals only, without the automatic notes, which can carry
// KPI detail, and without the hash chain.
func (s *Server) auditView(r *http.Request, in []approvals.Event) []approvals.Event {
	t := auth.FromContext(r.Context()).Tenant
	if t == "" {
		if in == nil {
			return []approvals.Event{}
		}
		return in
	}
	own := map[string]bool{}
	for _, p := range s.opt.Store.List() {
		if p.Tenant == t {
			own[p.ID] = true
		}
	}
	out := []approvals.Event{}
	for _, e := range in {
		if !own[e.Proposal] {
			continue
		}
		v := approvals.Event{At: e.At, Proposal: e.Proposal, Action: e.Action, From: e.From, To: e.To, Phase: e.Phase, By: e.By}
		if e.By != "zyntra" || strings.HasPrefix(e.Note, "object outcome:") {
			v.Note = e.Note
		}
		out = append(out, v)
	}
	return out
}

// proposalTenant decides which tenant a typed proposal belongs to. A
// tenant-bound caller proposes inside its own tenant and must name at least
// one object it owns; anyone else gets the single tenant their objects share,
// and mixing tenants in one proposal is refused.
func (s *Server) proposalTenant(r *http.Request, refs []ontology.ObjectRef) (string, error) {
	id := auth.FromContext(r.Context())
	tenants := map[string]bool{}
	for _, ref := range refs {
		o, ok := s.opt.Ontology.Store.Get(ref.ID)
		if !ok {
			return "", errors.New("object " + ref.ID + " no longer exists")
		}
		if o.Tenant != "" {
			tenants[o.Tenant] = true
		}
	}
	if id.Tenant != "" {
		if len(tenants) != 1 || !tenants[id.Tenant] {
			return "", errors.New("a proposal must act on at least one object of your tenant, and only on yours or shared ones")
		}
		return id.Tenant, nil
	}
	if len(tenants) > 1 {
		return "", errors.New("one proposal cannot act on objects of different tenants")
	}
	for t := range tenants {
		return t, nil
	}
	return "", nil
}
