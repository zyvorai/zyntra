// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package api

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zyvorai/zyntra/internal/actions"
	"github.com/zyvorai/zyntra/internal/ai"
	"github.com/zyvorai/zyntra/internal/auth"
	"github.com/zyvorai/zyntra/internal/connector"
	"github.com/zyvorai/zyntra/internal/gaps"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/ontology"
	"github.com/zyvorai/zyntra/internal/scenario"
)

// OntologyOptions wires a pack's business-object layer into the server. All
// fields are nil for a pack without ontology.yaml and every ontology route
// then answers 404.
type OntologyOptions struct {
	Def       *ontology.Definition
	Store     *ontology.Store
	Dir       string // pack directory the mappings are relative to
	Access    *ontology.Access
	Actions   *actions.Registry
	Scenarios *scenario.Store
	Load      ontology.Loader
	// Scheduler runs the pack's file mappings and connectors on their
	// intervals; nil falls back to reading them on demand only.
	Scheduler *connector.Scheduler
	Connector connector.Options
}

func (s *Server) ontOn(w http.ResponseWriter) bool {
	if s.opt.Ontology.Store == nil {
		writeErr(w, http.StatusNotFound, "this pack has no ontology")
		return false
	}
	return true
}

func hasRole(id auth.Identity, role string) bool {
	for _, r := range id.Roles {
		if string(r) == role || r == auth.RoleAdmin {
			return true
		}
	}
	return false
}

func principal(r *http.Request) ontology.Principal {
	id := auth.FromContext(r.Context())
	p := ontology.Principal{Subject: id.Subject, Tenant: id.Tenant}
	for _, role := range id.Roles {
		p.Roles = append(p.Roles, string(role))
	}
	return p
}

func (s *Server) reader(r *http.Request) ontology.Reader {
	return s.opt.Ontology.Store.As(s.opt.Ontology.Access, principal(r))
}

// failing reports whether a KPI currently misses its target.
func failing(m *graph.Model) func(string) bool {
	return func(id string) bool {
		k, ok := m.KPI(id)
		return ok && gaps.Severity(*k, k.Value) > 0
	}
}

// objectContext is the assistant's permission-scoped view of the objects.
func (s *Server) objectContext(r *http.Request, m *graph.Model) *ai.ObjectContext {
	if s.opt.Ontology.Store == nil {
		return nil
	}
	return &ai.ObjectContext{Reader: s.reader(r), Schema: s.opt.Ontology.Store.Schema(), Failing: failing(m), Label: s.kpiLabeler(r)}
}

type linkView struct {
	Link  ontology.Link   `json:"link"`
	Other ontology.Object `json:"other"`
	Out   bool            `json:"out"`
}

func (s *Server) handleOntSchema(w http.ResponseWriter, r *http.Request) {
	if !s.ontOn(w) {
		return
	}
	// Connector commands, URLs and token variable names are operator
	// configuration, not something a reader needs; rollout sites can name
	// other customers' locations, so tenant-bound callers do not get them.
	d := *s.opt.Ontology.Def
	d.Connectors = nil
	if tenant := auth.FromContext(r.Context()).Tenant; tenant != "" {
		d.Rollout = nil
		// Object types name the provider KPIs that measure them; a tenant
		// keeps only its own tenant's.
		m, _ := s.snapshot()
		// Offer only the object types this account can actually see, so the
		// provider's type names are not listed to a tenant that has none.
		visible := map[string]bool{}
		for _, t := range s.reader(r).VisibleTypes() {
			visible[t] = true
		}
		var types []ontology.ObjectType
		for _, ot := range d.Objects {
			if !visible[ot.Name] {
				continue
			}
			var own []string
			for _, id := range ot.KPIs {
				if k, ok := m.KPI(id); ok && k.Tenant == tenant {
					own = append(own, id)
				}
			}
			ot.KPIs = own
			types = append(types, ot)
		}
		d.Objects = types
		var links []ontology.LinkType
		for _, l := range d.Links {
			if visible[l.From] && visible[l.To] {
				links = append(links, l)
			}
		}
		d.Links = links
		var views []ontology.ViewSpec
		for _, v := range d.Views {
			if visible[v.Type] {
				views = append(views, v)
			}
		}
		d.Views = views
		var acts []ontology.ActionType
		for _, a := range d.Actions {
			ok := true
			for _, in := range a.Inputs {
				ok = ok && visible[in.ObjectType]
			}
			if ok {
				acts = append(acts, a)
			}
		}
		d.Actions = acts
	}
	writeJSON(w, http.StatusOK, d)
}

// maxViewRows bounds one workflow view response.
const maxViewRows = 500

// maxPage bounds one page of objects; the default keeps the console snappy.
const (
	defaultPage = 200
	maxPage     = 1000
)

// handleOntObjects lists visible objects a page at a time. limit (default 200,
// at most 1000) and after (the "next" cursor of the previous page) page
// through them in id order; q and type filter. Because the cursor is an id,
// concurrent ingests never repeat or skip an object.
func (s *Server) handleOntObjects(w http.ResponseWriter, r *http.Request) {
	if !s.ontOn(w) {
		return
	}
	qv := r.URL.Query()
	limit := defaultPage
	if v := qv.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxPage {
			writeErr(w, http.StatusBadRequest, "limit must be 1-"+strconv.Itoa(maxPage))
			return
		}
		limit = n
	}
	q := strings.ToLower(qv.Get("q"))
	var match func(ontology.Object) bool
	if q != "" {
		match = func(o ontology.Object) bool {
			return strings.Contains(strings.ToLower(o.ID), q) || strings.Contains(strings.ToLower(propString(o, "name")), q)
		}
	}
	out, next := s.reader(r).Page(qv.Get("type"), qv.Get("after"), limit, match)
	if out == nil {
		out = []ontology.Object{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"objects": out, "next": next})
}

func propString(o ontology.Object, p string) string {
	if v, ok := o.Props[p]; ok {
		if s, ok := v.V.(string); ok {
			return s
		}
	}
	return ""
}

func (s *Server) handleOntObject(w http.ResponseWriter, r *http.Request) {
	if !s.ontOn(w) {
		return
	}
	rd := s.reader(r)
	o, ok := rd.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "no such object")
		return
	}
	var links []linkView
	for _, l := range rd.Links(o.ID) {
		other, out := l.From, false
		if l.From == o.ID {
			other, out = l.To, true
		}
		if x, ok := rd.Get(other); ok {
			links = append(links, linkView{l, x, out})
		}
	}
	m, _ := s.snapshot()
	var bad []string
	bound := ontology.BoundKPIs(s.opt.Ontology.Store.Schema(), o)
	for _, k := range bound {
		if failing(m)(k) {
			bad = append(bad, k)
		}
	}
	label := s.kpiLabeler(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"object": o, "links": links, "impact": rd.Impact(o.ID, 0),
		"bound_kpis": labelAll(label, bound), "failing_kpis": labelAll(label, bad),
	})
}

func (s *Server) handleOntImpact(w http.ResponseWriter, r *http.Request) {
	if !s.ontOn(w) {
		return
	}
	depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))
	im := s.reader(r).Impact(r.PathValue("id"), depth)
	if im == nil {
		im = []ontology.Impact{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"impact": im})
}

func (s *Server) handleOntRisk(w http.ResponseWriter, r *http.Request) {
	if !s.ontOn(w) {
		return
	}
	m, _ := s.snapshot()
	rd := s.reader(r)
	risks := ontology.AtRisk(rd, failing(m))
	exposed := ontology.Exposed(rd, risks)
	label := s.kpiLabeler(r)
	for i := range risks {
		risks[i].KPIs = labelAll(label, risks[i].KPIs)
	}
	for i := range exposed {
		exposed[i].KPIs = labelAll(label, exposed[i].KPIs)
	}
	writeJSON(w, http.StatusOK, map[string]any{"at_risk": nilSafe(risks), "exposed": nilSafe(exposed)})
}

func nilSafe[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

func (s *Server) handleOntCandidates(w http.ResponseWriter, r *http.Request) {
	if !s.ontOn(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": nilSafe(s.reader(r).Candidates())})
}

func (s *Server) decideCandidate(accept bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.ontOn(w) {
			return
		}
		c, err := s.reader(r).Decide(r.PathValue("id"), accept)
		if err != nil {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, c)
	}
}

// RefreshOntology re-reads the pack's mappings and connectors. since is the
// previous successful pull for incremental connectors.
func (s *Server) RefreshOntology(ctx context.Context, by string) ([]ontology.IngestReport, error) {
	o := s.opt.Ontology
	if o.Store == nil {
		return nil, nil
	}
	if o.Scheduler != nil {
		return o.Scheduler.RunAll(ctx)
	}
	now := time.Now().UTC()
	reps, err := o.Store.IngestMappings(o.Def, o.Dir, by, o.Load, now)
	if err != nil {
		return reps, err
	}
	for _, spec := range o.Def.Connectors {
		c, err := connector.FromSpec(spec, o.Def.Schema(), o.Connector)
		if err != nil {
			return reps, err
		}
		rep, err := connector.Run(ctx, o.Store, c, time.Time{}, by, now)
		reps = append(reps, rep)
		if err != nil {
			return reps, err
		}
	}
	return reps, nil
}

func (s *Server) handleOntRefresh(w http.ResponseWriter, r *http.Request) {
	if !s.ontOn(w) {
		return
	}
	reps, err := s.RefreshOntology(r.Context(), auth.FromContext(r.Context()).Subject)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "reports": reps})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reports": reps})
}

func (s *Server) handleOntActions(w http.ResponseWriter, _ *http.Request) {
	if !s.ontOn(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"actions": nilSafe(s.opt.Ontology.Actions.List())})
}

type viewRow struct {
	ID        string         `json:"id"`
	Cells     map[string]any `json:"cells"`
	Failing   []string       `json:"failing_kpis,omitempty"`
	ExposedBy []string       `json:"exposed_by,omitempty"`
}

func (s *Server) handleOntView(w http.ResponseWriter, r *http.Request) {
	if !s.ontOn(w) {
		return
	}
	var spec *ontology.ViewSpec
	for i := range s.opt.Ontology.Def.Views {
		if s.opt.Ontology.Def.Views[i].ID == r.PathValue("id") {
			spec = &s.opt.Ontology.Def.Views[i]
		}
	}
	if spec == nil {
		writeErr(w, http.StatusNotFound, "no such view")
		return
	}
	m, _ := s.snapshot()
	rd := s.reader(r)
	risks := ontology.AtRisk(rd, failing(m))
	byRisk := map[string]ontology.Risk{}
	for _, rk := range risks {
		byRisk[rk.ID] = rk
	}
	exposed := map[string]ontology.Exposure{}
	for _, e := range ontology.Exposed(rd, risks) {
		exposed[e.ID] = e
	}
	label := s.kpiLabeler(r)
	rows := []viewRow{}
	for _, o := range rd.List(spec.Type) {
		keep := true
		for k, want := range spec.Where {
			if v, ok := o.Props[k]; !ok || toStr(v.V) != want {
				keep = false
			}
		}
		if !keep {
			continue
		}
		row := viewRow{ID: o.ID, Cells: map[string]any{}}
		for _, c := range spec.Columns {
			if v, ok := o.Props[c]; ok {
				row.Cells[c] = v.V
			}
		}
		row.Failing = labelAll(label, byRisk[o.ID].KPIs)
		if spec.Exposed {
			for _, d := range exposed[o.ID].DependsOn {
				if x, ok := rd.Get(d); ok {
					row.ExposedBy = append(row.ExposedBy, propString(x, "name"))
				}
			}
			sort.Strings(row.ExposedBy)
		}
		rows = append(rows, row)
	}
	// Rows that need attention come first, then id order; the rest is capped
	// so a view over a huge type stays a reasonable response.
	flagged := func(r viewRow) bool { return len(r.Failing) > 0 || len(r.ExposedBy) > 0 }
	sort.SliceStable(rows, func(i, j int) bool { return flagged(rows[i]) && !flagged(rows[j]) })
	total := len(rows)
	if len(rows) > maxViewRows {
		rows = rows[:maxViewRows]
	}
	writeJSON(w, http.StatusOK, map[string]any{"view": spec, "rows": rows, "total": total, "truncated": total > len(rows)})
}

func toStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return strconv.FormatFloat(toFloat(v), 'f', -1, 64)
}

func toFloat(v any) float64 {
	f, _ := v.(float64)
	return f
}

// handleAIPropose drafts a typed proposal from text. It creates nothing.
func (s *Server) handleAIPropose(w http.ResponseWriter, r *http.Request) {
	if !s.ontOn(w) {
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &req) {
		return
	}
	m, _ := s.snapshot()
	id := auth.FromContext(r.Context())
	reg := s.opt.Ontology.Actions
	d := ai.Structure(req.Text, m, reg.List(), s.reader(r), func(a string, in map[string]string) []string {
		_, problems := reg.Validate(a, in, principal(r), func(role string) bool { return hasRole(id, role) })
		return problems
	})
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleScenarios(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"scenarios": nilSafe(s.opt.Ontology.Scenarios.List())})
}

func (s *Server) handleScenarioCreate(w http.ResponseWriter, r *http.Request) {
	var sc scenario.Scenario
	if !decode(w, r, &sc) {
		return
	}
	if strings.TrimSpace(sc.Name) == "" || len(sc.Actions) == 0 {
		writeErr(w, http.StatusBadRequest, "a scenario needs a name and at least one action")
		return
	}
	sc.ID, sc.Result = "", nil
	sc.CreatedBy, sc.CreatedAt = auth.FromContext(r.Context()).Subject, time.Now().UTC()
	out, err := s.runScenario(r, sc)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := s.opt.Ontology.Scenarios.Put(out)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, saved)
}

func (s *Server) runScenario(r *http.Request, sc scenario.Scenario) (scenario.Scenario, error) {
	m, _ := s.snapshot()
	var rd *ontology.Reader
	data := ""
	if s.opt.Ontology.Store != nil {
		v := s.reader(r)
		rd, data = &v, s.opt.Ontology.Store.Fingerprint()
	}
	return scenario.Run(m, rd, data, sc, time.Now().UTC())
}

func (s *Server) handleScenario(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.opt.Ontology.Scenarios.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "no such scenario")
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) handleScenarioRun(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.opt.Ontology.Scenarios.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "no such scenario")
		return
	}
	out, err := s.runScenario(r, sc)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, _ := s.opt.Ontology.Scenarios.Put(out)
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleScenarioDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.opt.Ontology.Scenarios.Delete(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleScenarioCompare(w http.ResponseWriter, r *http.Request) {
	var scs []scenario.Scenario
	for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
		sc, ok := s.opt.Ontology.Scenarios.Get(strings.TrimSpace(id))
		if !ok {
			writeErr(w, http.StatusNotFound, "no such scenario: "+id)
			return
		}
		scs = append(scs, sc)
	}
	c, err := scenario.Compare(scs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleOntHistory(w http.ResponseWriter, r *http.Request) {
	if !s.ontOn(w) {
		return
	}
	h := s.reader(r).History(r.PathValue("id"))
	if h == nil {
		h = []ontology.Change{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": h})
}

// maxIngestRecords bounds one push; larger loads are split by the connector.
const maxIngestRecords = 5000

// handleOntIngest takes a normalized batch from a connector. The caller needs
// the ingest or admin role and an explicit tenant grant in the access rules;
// the batch is all-or-nothing and cannot touch another tenant's objects.
func (s *Server) handleOntIngest(w http.ResponseWriter, r *http.Request) {
	if !s.ontOn(w) {
		return
	}
	tenant := r.PathValue("tenant")
	if tenant == "default" {
		tenant = ""
	}
	p := principal(r)
	id := auth.FromContext(r.Context())
	// A connector credential carries its own grant: the tenants and object
	// types it was issued for. The shared legacy token and admins go through
	// the access rules instead.
	if id.Scope != nil {
		if !id.Scope.Allows(tenant) {
			writeErr(w, http.StatusForbidden, "this connector credential is not issued for that tenant")
			return
		}
	} else if !s.opt.Ontology.Access.CanIngest(p, tenant) {
		writeErr(w, http.StatusForbidden, "no ingest grant for this tenant")
		return
	}
	var req struct {
		Source  string            `json:"source"`
		Records []ontology.Record `json:"records"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Source == "" || len(req.Source) > 100 || len(req.Records) == 0 || len(req.Records) > maxIngestRecords {
		writeErr(w, http.StatusBadRequest, "want a source name and 1-5000 records")
		return
	}
	if id.Scope != nil {
		for _, rec := range req.Records {
			if !id.Scope.AllowsType(rec.Type) {
				writeErr(w, http.StatusForbidden, "this connector credential may not write objects of type "+rec.Type)
				return
			}
		}
	}
	rep, err := s.opt.Ontology.Store.IngestScoped(tenant, "push:"+req.Source, p.Subject, req.Records, time.Now().UTC())
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, rep)
}

// handleConnectors reports each connector's health. Errors can quote what a
// source returned, so this is for approvers and admins, not tenants.
func (s *Server) handleConnectors(w http.ResponseWriter, _ *http.Request) {
	if !s.ontOn(w) {
		return
	}
	st := []connector.Status{}
	if sc := s.opt.Ontology.Scheduler; sc != nil {
		st = sc.Statuses()
	}
	writeJSON(w, http.StatusOK, map[string]any{"connectors": st})
}

func (s *Server) handleConnectorRun(w http.ResponseWriter, r *http.Request) {
	if !s.ontOn(w) {
		return
	}
	sc := s.opt.Ontology.Scheduler
	if sc == nil {
		writeErr(w, http.StatusNotFound, "no scheduler")
		return
	}
	rep, err := sc.RunNow(r.Context(), r.PathValue("name"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, rep)
	case err.Error() == "already running":
		writeErr(w, http.StatusConflict, err.Error())
	case strings.HasPrefix(err.Error(), "no connector"):
		writeErr(w, http.StatusNotFound, err.Error())
	default:
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "report": rep})
	}
}

// handleOntStats says how much the ontology holds against its cap.
func (s *Server) handleOntStats(w http.ResponseWriter, _ *http.Request) {
	if !s.ontOn(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.opt.Ontology.Store.Stats())
}
