// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package pack

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/adapters"
	"github.com/zyvorai/zyntra/internal/executor"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/ontology"
	"github.com/zyvorai/zyntra/internal/planner"
	"github.com/zyvorai/zyntra/internal/sim"
)

const root = "../../packs"

func TestEveryPackValidates(t *testing.T) {
	list, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 {
		t.Fatal("no packs found")
	}
	for _, p := range list {
		r := Validate(context.Background(), p.Dir)
		if !r.Valid() {
			t.Errorf("pack %s: %v", p.ID, r.Errors)
		}
	}
}

func shop(t *testing.T) *graph.Model {
	t.Helper()
	m, err := Load(filepath.Join(root, "shop"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapters.Refresh(context.Background(), m, adapters.Config{Files: adapters.NewFileCache()}); err != nil &&
		!strings.Contains(err.Error(), "cashiers_open") {
		t.Fatal(err)
	}
	return m
}

func TestShopFixturePlan(t *testing.T) {
	m := shop(t)
	if m.Pack == nil || m.Pack.ID != "shop" || m.Timezone != "Asia/Kolkata" || len(m.Calendars) != 3 {
		t.Fatalf("pack not applied: %+v tz=%s calendars=%d", m.Pack, m.Timezone, len(m.Calendars))
	}
	if k, _ := m.KPI("stockout_rate"); k.Value != 0.03 {
		t.Fatalf("stockout_rate from fixture = %v, want 0.03", k.Value)
	}
	res, err := planner.PlanWith(m, planner.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var reorder *planner.Recommendation
	for i, r := range res.Recommendations {
		if r.Action == "reorder_fast_movers" {
			reorder = &res.Recommendations[i]
		}
		if strings.Contains(r.Action, "markdown_dead_stock") {
			t.Errorf("markdown_dead_stock breaks the margin invariant but was ranked: %s", r.Action)
		}
	}
	if reorder == nil || reorder.Status != planner.StatusPendingApproval {
		t.Fatalf("reorder_fast_movers not approvable in the plan: %+v", reorder)
	}
	closes := strings.Join(reorder.Result.GapsClosed, ",")
	if !strings.Contains(closes, "stockout_rate") {
		t.Errorf("reorder should close stockout_rate, closes %q", closes)
	}
	blocked := false
	for _, r := range res.Blocked {
		if r.Action == "markdown_dead_stock" && strings.Contains(strings.Join(r.BlockedReasons, ";"), "invariant") {
			blocked = true
		}
	}
	if !blocked {
		t.Errorf("markdown_dead_stock should be blocked by the gross_margin invariant: %+v", res.Blocked)
	}

	a, _ := m.Action("reorder_fast_movers")
	rd, err := executor.RenderIn(m, *a)
	if err != nil || rd.Kind != graph.KindFile || !strings.Contains(rd.Content, "Stockout rate: 3.0%") {
		t.Fatalf("dry-run PO: %+v %v", rd, err)
	}
	if out := (&executor.Executor{}).Execute(context.Background(), rd); !out.OK || out.Written != "" {
		t.Fatalf("dry-run must not write: %+v", out)
	}
}

func TestShopPreconditionFailsClosed(t *testing.T) {
	m := shop(t)
	k, _ := m.KPI("stockout_rate")
	k.Value = 0.01
	a, _ := m.Action("reorder_fast_movers")
	r, err := sim.ApplyPlan(m, []graph.Action{*a}, sim.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.PreconditionFailures) == 0 {
		t.Fatal("reorder with shelves full should fail its precondition")
	}
	k.Value = 0.03
	r, _ = sim.ApplyPlan(m, []graph.Action{*a}, sim.Options{Unusable: map[string]bool{"stockout_rate": true}})
	if len(r.PreconditionFailures) == 0 || !strings.Contains(r.PreconditionFailures[0], "stale") {
		t.Fatalf("a stale precondition input must fail closed: %v", r.PreconditionFailures)
	}
}

func TestManifestErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(ModelFile, "name: x\nkpis:\n  - {id: a, value: 1, owner: cook, calendar: lunch}\nedges: []\nactions: []\n")
	write(Manifest, "id: x\ntitle: X\nowners: [owner]\ncalendars:\n  lunch: {start: \"12:00\", end: \"15:00\"}\n")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "not one of the pack owners") {
		t.Fatalf("unknown owner should fail, got %v", err)
	}
	write(Manifest, "id: x\ntitle: X\nowners: [cook]\ncalendars:\n  lunch: {start: \"12:00\", end: \"15:00\"}\n")
	if _, err := Load(dir); err != nil {
		t.Fatalf("calendar from pack.yaml should resolve: %v", err)
	}
	write(Manifest, "id: X Bad\ntitle: X\n")
	if _, err := Load(dir); err == nil {
		t.Fatal("bad id should fail")
	}
	write(Manifest, "id: x\ntitle: X\nowners: [cook]\nsurprise: 1\n")
	if _, err := Load(dir); err == nil {
		t.Fatal("unknown manifest field should fail")
	}
}

func TestManufacturingOntologyLinksOrdersToCompute(t *testing.T) {
	m, err := Load(root + "/manufacturing")
	if err != nil {
		t.Fatal(err)
	}
	def, dir, err := LoadOntology(root+"/manufacturing", m)
	if err != nil || def == nil {
		t.Fatalf("ontology: %v", err)
	}
	st, _ := ontology.Open("", def.Schema())
	if _, err := st.IngestMappings(def, dir, "test", adapters.NewFileCache().Load, time.Now()); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, i := range st.Impact("Cluster:infra:gpu-a", 0) {
		got[i.Object.ID] = true
	}
	for _, want := range []string{"InspectionService:infra:vision-qa", "Line:erp:L1", "Order:erp:O-1001"} {
		if !got[want] {
			t.Errorf("a cluster failure should reach %s", want)
		}
	}
	if got["Order:erp:O-1007"] {
		t.Error("line 3 inspects on the overflow cluster; its orders do not depend on cluster A")
	}
	if len(st.Candidates()) != 1 {
		t.Errorf("the ERP and MES press should be one pending identity candidate, got %d", len(st.Candidates()))
	}
}
