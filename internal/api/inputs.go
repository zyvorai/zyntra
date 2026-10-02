// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package api

import (
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/zyvorai/zyntra/internal/auth"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/inputs"
)

// channelKPIs returns the KPIs fed by a webhook-in channel.
func channelKPIs(m *graph.Model, name string) []string {
	var out []string
	for _, k := range m.KPIs {
		if k.Source != nil && k.Source.Kind == graph.SourceWebhookIn && k.Source.Name == name {
			out = append(out, k.ID)
		}
	}
	return out
}

// handleIngest stores a document POSTed by a gateway; the last one wins.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	m, _ := s.snapshot()
	kpis := channelKPIs(m, name)
	if len(kpis) == 0 {
		writeErr(w, http.StatusNotFound, "no KPI reads webhook-in channel "+name)
		return
	}
	var body any
	if !decode(w, r, &body) {
		return
	}
	who := auth.FromContext(r.Context())
	if err := s.opt.Inputs.Ingest(name, body, who.Subject, time.Now()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.RefreshOnce(r.Context())
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "channel": name, "kpis": kpis})
}

// handleManualValue records an operator-entered value for a manual KPI.
// Every entry is written to the audit chain.
func (s *Server) handleManualValue(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, _ := s.snapshot()
	k, ok := m.KPI(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown kpi")
		return
	}
	if k.Source == nil || k.Source.Kind != graph.SourceManual {
		writeErr(w, http.StatusBadRequest, id+" is not a manual KPI")
		return
	}
	var req struct {
		Value  *float64 `json:"value"`
		Reason string   `json:"reason"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Value == nil || math.IsNaN(*req.Value) || math.IsInf(*req.Value, 0) {
		writeErr(w, http.StatusBadRequest, "value must be a number")
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if len(req.Reason) > 500 {
		writeErr(w, http.StatusBadRequest, "reason must be at most 500 characters")
		return
	}
	who := auth.FromContext(r.Context())
	entry := inputs.Manual{Value: *req.Value, At: time.Now(), By: who.Subject, Reason: req.Reason}
	if err := s.opt.Inputs.SetManual(id, entry); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	note := fmt.Sprintf("manual value %s = %g %s", id, *req.Value, k.DisplayUnit())
	if req.Reason != "" {
		note += ": " + req.Reason
	}
	if err := s.opt.Store.Note("manual:"+id, who.Subject, strings.TrimSpace(note)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.RefreshOnce(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"kpi": id, "entry": entry})
}

type manualView struct {
	KPI   string         `json:"kpi"`
	Name  string         `json:"name"`
	Unit  string         `json:"unit,omitempty"`
	Owner string         `json:"owner,omitempty"`
	Value float64        `json:"value"`
	Entry *inputs.Manual `json:"entry,omitempty"`
}

type channelView struct {
	Name     string     `json:"name"`
	KPIs     []string   `json:"kpis"`
	Received *time.Time `json:"received_at,omitempty"`
	From     string     `json:"from,omitempty"`
}

// handleInputs lists manual KPIs and webhook-in channels.
func (s *Server) handleInputs(w http.ResponseWriter, _ *http.Request) {
	m, _ := s.snapshot()
	manual := []manualView{}
	channels := []channelView{}
	seen := map[string]bool{}
	for _, k := range m.KPIs {
		if k.Source == nil {
			continue
		}
		switch k.Source.Kind {
		case graph.SourceManual:
			v := manualView{KPI: k.ID, Name: k.Name, Unit: k.DisplayUnit(), Owner: k.Owner, Value: k.Value}
			if e, ok := s.opt.Inputs.Manual(k.ID); ok {
				v.Entry = &e
			}
			manual = append(manual, v)
		case graph.SourceWebhookIn:
			if seen[k.Source.Name] {
				continue
			}
			seen[k.Source.Name] = true
			c := channelView{Name: k.Source.Name, KPIs: channelKPIs(m, k.Source.Name)}
			if d, ok := s.opt.Inputs.Webhook(k.Source.Name); ok {
				at := d.At
				c.Received, c.From = &at, d.From
			}
			channels = append(channels, c)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"manual": manual, "webhooks": channels})
}
