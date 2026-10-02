// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/approvals"
	"github.com/zyvorai/zyntra/internal/graph"
)

func mpServer(t *testing.T, kpi string, infos []PackInfo) *Server {
	t.Helper()
	m, err := graph.Parse([]byte("name: " + kpi + "\nkpis: [{id: " + kpi + ", name: " + kpi + ", value: 1}]\nedges: []\nactions: [{id: fix_" + kpi + ", name: Fix, effects: [{kpi: " + kpi + ", change: -0.1}]}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := approvals.Open("")
	return New(Options{Model: m, Interval: time.Hour, Store: st, Packs: infos})
}

func mpGet(t *testing.T, h http.Handler, path, pack string, out any) int {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	if pack != "" {
		r.Header.Set(PackHeader, pack)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if out != nil {
		_ = json.Unmarshal(w.Body.Bytes(), out)
	}
	return w.Code
}

func TestPackMuxRoutesByHeaderQueryAndDefault(t *testing.T) {
	infos := []PackInfo{{ID: "gpu", Title: "GPU", Default: true}, {ID: "shop", Title: "Shop"}}
	h := PackMux(map[string]*Server{"gpu": mpServer(t, "queue", infos), "shop": mpServer(t, "orders", infos)}, "gpu")

	kpis := func(path, pack string) string {
		var g struct {
			Model struct{ KPIs []struct{ ID string } }
		}
		if c := mpGet(t, h, path, pack, &g); c != 200 || len(g.Model.KPIs) != 1 {
			t.Fatalf("%s as %q = %d %+v", path, pack, c, g)
		}
		return g.Model.KPIs[0].ID
	}
	if got := kpis("/api/v1/graph", ""); got != "queue" {
		t.Errorf("default pack = %q", got)
	}
	if got := kpis("/api/v1/graph", "shop"); got != "orders" {
		t.Errorf("header pack = %q", got)
	}
	if got := kpis("/api/v1/graph?pack=shop", ""); got != "orders" {
		t.Errorf("query pack = %q", got)
	}
	// The header wins over the query, so a stale link cannot override the console's choice.
	r := httptest.NewRequest("GET", "/api/v1/graph?pack=gpu", nil)
	r.Header.Set(PackHeader, "shop")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "orders") {
		t.Errorf("header did not win: %s", w.Body.String())
	}
	var list []PackInfo
	if c := mpGet(t, h, "/api/v1/packs", "", &list); c != 200 || len(list) != 2 || !list[0].Default {
		t.Errorf("packs = %d %+v", c, list)
	}
	if c := mpGet(t, h, "/api/v1/graph", "nope", nil); c != 404 {
		t.Errorf("unknown pack = %d, want 404", c)
	}
}

func TestPacksKeepTheirOwnApprovals(t *testing.T) {
	a, b := mpServer(t, "queue", nil), mpServer(t, "orders", nil)
	if _, _, err := a.opt.Store.Create(approvals.Proposal{Action: "fix_queue"}, "ann"); err != nil {
		t.Fatal(err)
	}
	h := PackMux(map[string]*Server{"gpu": a, "shop": b}, "gpu")
	count := func(pack string) int {
		var ps struct{ Proposals []approvals.Proposal }
		if c := mpGet(t, h, "/api/v1/proposals", pack, &ps); c != 200 {
			t.Fatalf("proposals as %q = %d", pack, c)
		}
		return len(ps.Proposals)
	}
	if count("gpu") != 1 || count("shop") != 0 {
		t.Errorf("proposals leaked between packs: gpu=%d shop=%d", count("gpu"), count("shop"))
	}
	if len(a.opt.Store.Audit()) == 0 || len(b.opt.Store.Audit()) != 0 {
		t.Error("audit chains are not separate")
	}
}

func TestSinglePackHasNoPackList(t *testing.T) {
	var list []PackInfo
	if c := mpGet(t, mpServer(t, "queue", nil).Handler(), "/api/v1/packs", "", &list); c != 200 || list == nil || len(list) != 0 {
		t.Errorf("single pack list = %d %v, want an empty JSON array", c, list)
	}
}
