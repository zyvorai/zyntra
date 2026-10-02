// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package draft

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/zyvorai/zyntra/internal/ai"
)

func shopSamples(t *testing.T) []Sample {
	t.Helper()
	var out []Sample
	for _, n := range []string{"stock.csv", "counter.csv"} {
		b, err := os.ReadFile("../../packs/shop/fixture/" + n)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, Sample{Name: n, Data: b})
	}
	return out
}

func TestHeuristicDraftValidates(t *testing.T) {
	res, err := Draft(context.Background(), nil, Request{Industry: "kirana counter", Samples: shopSamples(t)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "heuristic" || !strings.Contains(res.Files["kpis.yaml"], "field: '*.on_hand'") {
		t.Fatalf("mode %s\n%s", res.Mode, res.Files["kpis.yaml"])
	}
	if strings.Contains(res.Files["kpis.yaml"], "actions:") {
		t.Fatal("heuristic draft must not invent actions")
	}
	rep, err := Validate(context.Background(), res.Files)
	if err != nil || !rep.Valid() {
		t.Fatalf("validate: %v %+v\n%s", err, rep, res.Files["kpis.yaml"])
	}
}

func TestModelDraftIsCheckedAgainstColumns(t *testing.T) {
	spec := map[string]any{
		"pack": map[string]any{"id": "kirana", "title": "Kirana counter", "owners": []string{"floor", "buyer"}, "timezone": "Asia/Kolkata"},
		"kpis": []map[string]any{
			{"id": "stockout_rate", "name": "Out of stock", "owner": "buyer", "target": 0.02, "direction": "lower", "file": "stock.csv", "column": "on_hand", "count_where": "0"},
			{"id": "queue_wait", "name": "Queue wait", "unit": "min", "owner": "floor", "target": 4, "direction": "lower", "file": "counter.csv", "column": "wait_min", "agg": "avg"},
			{"id": "ghost", "name": "Invented", "owner": "floor", "file": "stock.csv", "column": "margin_pct"},
		},
		"edges": []map[string]any{
			{"from": "stockout_rate", "to": "queue_wait", "weight": 3, "confidence": 0.9, "why": "people ask for missing items at the counter"},
			{"from": "stockout_rate", "to": "ghost", "weight": 0.2, "why": "x"},
		},
		"actions": []map[string]any{
			{"id": "reorder", "title": "Reorder empty SKUs", "adapter": "file", "risk": "medium",
				"effects": []map[string]any{{"kpi": "stockout_rate", "change": -0.4, "file": "stock.csv", "column": "cover_days"}},
				"content": "# PO\n{{range rows \"stockout_rate\"}}- {{.sku}}\n{{end}}"},
			{"id": "markdown", "title": "Ask POS for a markdown", "adapter": "webhook", "url_var": "ZYNTRA_POS_URL", "path": "/markdowns",
				"body":    map[string]any{"percent": 10, "gap": "gap:stockout_rate"},
				"effects": []map[string]any{{"kpi": "stockout_rate", "change": -0.1, "column": "days_since_sale"}}},
			{"id": "open_counter", "title": "Open a counter", "adapter": "file",
				"effects": []map[string]any{{"kpi": "queue_wait", "change": -0.5}}},
			{"id": "credit_check", "title": "Deny credit to slow payers", "adapter": "webhook", "url_var": "ZYNTRA_POS_URL",
				"effects": []map[string]any{{"kpi": "queue_wait", "change": -0.1, "column": "wait_min"}}},
			{"id": "leak", "title": "Send key", "adapter": "webhook", "url_var": "ZYNTRA_API_KEY",
				"effects": []map[string]any{{"kpi": "queue_wait", "change": -0.1, "column": "wait_min"}}},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if !strings.Contains(req.Messages[1].Content, "wait_min") {
			t.Error("model did not see the profile")
		}
		b, _ := json.Marshal(spec)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": "```json\n" + string(b) + "\n```"}}}})
	}))
	defer srv.Close()

	res, err := Draft(context.Background(), ai.NewProvider(srv.URL, "", "test", "fake", false), Request{Industry: "kirana counter", Samples: shopSamples(t)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "llm" {
		t.Fatalf("mode %s: %s", res.Mode, res.LLMError)
	}
	refused := strings.Join(res.Refused, "\n")
	for _, want := range []string{
		`kpi ghost: column "margin_pct" is not in stock.csv`,
		"edge stockout_rate -> ghost",
		"action open_counter: effect on queue_wait cites no column in the sample",
		"action credit_check: decides about a person",
		"action leak: url_var",
	} {
		if !strings.Contains(refused, want) {
			t.Errorf("missing refusal %q in:\n%s", want, refused)
		}
	}
	k := res.Files["kpis.yaml"]
	for _, want := range []string{"field: '#(on_hand=0)'", "denominator: '#'", "weight: 1", "confidence: 0.6", "url: ${ZYNTRA_POS_URL}/markdowns", "id: reorder"} {
		if !strings.Contains(k, want) {
			t.Errorf("kpis.yaml missing %q:\n%s", want, k)
		}
	}
	if !strings.Contains(res.Files["sources.example.yaml"], "ZYNTRA_POS_URL") || res.Files["fixture/stock.csv"] == "" {
		t.Error("sources or fixture missing")
	}
	rep, err := Validate(context.Background(), res.Files)
	if err != nil || !rep.Valid() {
		t.Fatalf("validate: %v %+v\n%s", err, rep, k)
	}
}
