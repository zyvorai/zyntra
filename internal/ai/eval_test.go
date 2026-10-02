// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ai_test

// The evaluation suite: golden cases that must hold before a new model,
// prompt or retrieval change ships. They run against the heuristic engine,
// which is the floor every deployment has; run with an LLM provider
// configured to check that rewriting adds no facts. `make eval` runs them.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/actions"
	"github.com/zyvorai/zyntra/internal/adapters"
	"github.com/zyvorai/zyntra/internal/ai"
	"github.com/zyvorai/zyntra/internal/gaps"
	"github.com/zyvorai/zyntra/internal/ontology"
	"github.com/zyvorai/zyntra/internal/pack"
)

type world struct {
	snap ai.Snapshot
	st   *ontology.Store
	def  *ontology.Definition
	ac   *ontology.Access
}

func newWorld(t *testing.T, rules []ontology.Rule) *world {
	t.Helper()
	const dir = "../../packs/gpu"
	m, err := pack.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	def, _, err := pack.LoadOntology(dir, m)
	if err != nil || def == nil {
		t.Fatalf("ontology: %v", err)
	}
	st, _ := ontology.Open("", def.Schema())
	if _, err := st.IngestMappings(def, dir, "eval", adapters.NewFileCache().Load, time.Now()); err != nil {
		t.Fatal(err)
	}
	g := gaps.Detect(m)
	return &world{
		snap: ai.Snapshot{Model: m, Gaps: g, Severity: gaps.Total(m, nil)},
		st:   st, def: def, ac: &ontology.Access{Schema: def.Schema(), Rules: rules},
	}
}

func (w *world) ask(role, q string) ai.Answer {
	failing := map[string]bool{}
	for _, g := range w.snap.Gaps {
		failing[g.KPI] = true
	}
	s := w.snap
	s.Objects = &ai.ObjectContext{
		Reader:  w.st.As(w.ac, ontology.Principal{Subject: role, Roles: []string{role}}),
		Schema:  w.def.Schema(),
		Failing: func(k string) bool { return failing[k] },
	}
	return (&ai.Engine{}).Ask(context.Background(), q, s)
}

func TestEvalGrounding(t *testing.T) {
	w := newWorld(t, nil)
	rd := w.st.As(w.ac, ontology.Principal{Roles: []string{"admin"}})
	for _, q := range []string{
		"Which customers are at risk and why?",
		"Which services are affected?",
		"Tell me about Acme Robotics",
		"What depends on GPU Cluster A?",
	} {
		a := w.ask("admin", q)
		if a.Mode != "heuristic" || a.Text == "" {
			t.Fatalf("%q: %+v", q, a)
		}
		if len(a.Citations) == 0 {
			t.Errorf("%q: answer cites nothing", q)
		}
		for _, c := range a.Citations {
			o, ok := rd.Get(c.Object)
			if !ok {
				t.Errorf("%q: citation to unknown object %s", q, c.Object)
				continue
			}
			v, ok := o.Props[c.Property]
			if !ok || v.V != c.Value || c.Source == "" || c.ObservedAt.IsZero() {
				t.Errorf("%q: citation %+v does not match the object's recorded fact", q, c)
			}
		}
	}
	a := w.ask("admin", "Which customers are at risk and why?")
	for _, want := range []string{"Acme Robotics", "Globex Vision", "gpu_queue_wait_min"} {
		if !strings.Contains(a.Text, want) {
			t.Errorf("risk answer is missing %q:\n%s", want, a.Text)
		}
	}
}

func TestEvalPermissionBoundaries(t *testing.T) {
	w := newWorld(t, []ontology.Rule{
		{Roles: []string{"viewer"}, Types: []string{"Service", "Cluster"}},
	})
	questions := []string{
		"Which customers are at risk and why?",
		"Tell me about Acme Robotics",
		"What is the contact for Acme Robotics?",
		"Which services are affected?",
	}
	for _, q := range questions {
		a := w.ask("viewer", q)
		blob := a.Text + " " + strings.Join(a.Grounding, " ")
		for _, forbidden := range []string{"Acme", "Globex", "Initech", "ops@acme", "Customer:"} {
			if strings.Contains(blob, forbidden) {
				t.Errorf("%q: viewer answer leaks %q:\n%s", q, forbidden, a.Text)
			}
		}
		for _, c := range a.Citations {
			if strings.HasPrefix(c.Object, "Customer:") {
				t.Errorf("%q: citation to a hidden type: %+v", q, c)
			}
		}
	}
	// Sensitive properties stay hidden from non-admins even for visible types.
	open := newWorld(t, nil)
	if a := open.ask("approver", "Tell me about Acme Robotics"); strings.Contains(a.Text, "ops@acme") {
		t.Errorf("sensitive contact reached an approver:\n%s", a.Text)
	}
	if a := open.ask("admin", "Tell me about Acme Robotics"); !strings.Contains(a.Text, "ops@acme") {
		t.Errorf("admin should see the contact:\n%s", a.Text)
	}
}

func TestEvalActionSelectionAndProposals(t *testing.T) {
	w := newWorld(t, nil)
	reg := actions.New(w.def, w.st, w.ac)
	p := ontology.Principal{Roles: []string{"approver"}}
	validate := func(id string, in map[string]string) []string {
		_, problems := reg.Validate(id, in, p, func(r string) bool { return r == "approver" })
		return problems
	}
	rd := w.st.As(w.ac, p)
	draft := func(text string) ai.Draft {
		return ai.Structure(text, w.snap.Model, reg.List(), rd, validate)
	}
	cases := []struct {
		name, text, action, input string
		valid                     bool
	}{
		{"names action and object", "raise-inference-priority for Inference API", "raise-inference-priority", "Service:erp:svc-infer", true},
		{"object precondition fails", "raise-inference-priority for Batch training", "raise-inference-priority", "Service:erp:svc-batch", false},
		{"missing required input", "enable-mig-sharing please", "enable-mig-sharing", "", false},
		{"no action named", "make everything faster", "", "", false},
		{"never invents an action", "delete all clusters", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := draft(c.text)
			if d.Action != c.action || d.Valid != c.valid {
				t.Fatalf("got %+v", d)
			}
			if c.input != "" {
				found := false
				for _, v := range d.Inputs {
					found = found || v == c.input
				}
				if !found {
					t.Errorf("inputs %v lack %s", d.Inputs, c.input)
				}
			}
			if !d.Valid && len(d.Problems) == 0 {
				t.Error("an invalid draft must say why")
			}
		})
	}
}

func TestEvalNeverChoosesAnAction(t *testing.T) {
	w := newWorld(t, nil)
	a := w.ask("approver", "Which customers are at risk and what should we do?")
	for _, act := range w.snap.Model.Actions {
		// Objects answers describe state; they must not rank or pick actions.
		if strings.Contains(a.Text, act.ID) {
			t.Errorf("object answer names action %s; choosing is the planner's job", act.ID)
		}
	}
}
