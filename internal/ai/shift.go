// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Narrate rewrites a deterministic draft in plainer words when a model is
// configured. The draft, not the narrative, is what gets stored and hashed.
func (e *Engine) Narrate(ctx context.Context, question, draft string, facts any) Answer {
	a := Answer{Text: draft, Mode: "heuristic"}
	if e.LLM == nil {
		return a
	}
	text, err := e.LLM.Rewrite(ctx, question, draft, facts)
	if err != nil {
		a.LLMError = err.Error()
		return a
	}
	a.Text, a.Mode, a.Model = text, "llm", e.LLM.Model
	return a
}

// RuleReading is the model's view that an action breaks a rule line.
type RuleReading struct {
	Action string `json:"action"`
	Line   int    `json:"line"`
	Why    string `json:"why"`
}

const rulesPrompt = `You check a pack's own rules against its actions. RULES are numbered lines from the pack README. ACTIONS are the actions the live plan scores as approvable, with their preconditions, window, approvers and kind.
Return JSON {"findings":[{"action":"<action id from ACTIONS>","line":<rule line number>,"why":"<one sentence>"}]} listing only clear violations: the rule forbids or limits something the action, as defined, would allow. Return {"findings":[]} when nothing is clearly broken. Never invent actions, rules or numbers.`

// ReadRules asks the model which approvable actions break rule lines the
// deterministic checks do not cover. The caller keeps only findings that
// name a real action and a real line.
func (e *Engine) ReadRules(ctx context.Context, rules, actions any) ([]RuleReading, error) {
	if e.LLM == nil {
		return nil, nil
	}
	rb, _ := json.Marshal(rules)
	ab, _ := json.Marshal(actions)
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	text, err := e.LLM.Chat(ctx, rulesPrompt, "RULES:\n"+string(rb)+"\n\nACTIONS:\n"+string(ab), true)
	if err != nil {
		return nil, err
	}
	var out struct {
		Findings []RuleReading `json:"findings"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, err
	}
	return out.Findings, nil
}

// ShiftFacts is what a close-of-window note adds to the snapshot.
type ShiftFacts struct {
	Owner  string `json:"owner,omitempty"`
	Window string `json:"window"`
	// Open says whether the window is open now; Next is when it next opens
	// (or closes, when open).
	Open bool       `json:"open"`
	Next *time.Time `json:"next,omitempty"`
	// LastMiss is the first miss finding of the latest decided proposal.
	LastMiss         string `json:"last_miss,omitempty"`
	LastMissProposal string `json:"last_miss_proposal,omitempty"`
	// StaleBefore lists KPIs whose data expires before the window opens
	// again, with the time each goes stale.
	StaleBefore []StaleAt `json:"stale_before,omitempty"`
}

// StaleAt is a KPI and when its value stops being fresh.
type StaleAt struct {
	KPI string    `json:"kpi"`
	At  time.Time `json:"at"`
}

// ShiftDigest is the close-of-window note for one owner: three open gaps,
// the action that closes the most of them, what the last approved change
// got wrong, and what goes stale before the next window.
func ShiftDigest(s Snapshot, f ShiftFacts) Answer {
	a := Answer{Intent: "shift-digest"}
	var parts []string
	who := "everyone"
	if f.Owner != "" {
		who = f.Owner
	}
	head := fmt.Sprintf("Close of %s for %s.", f.Window, who)
	if f.Open && f.Next != nil {
		head = fmt.Sprintf("%s window for %s closes at %s.", f.Window, who, f.Next.Format("15:04"))
	}
	parts = append(parts, head)
	if len(s.Gaps) == 0 {
		parts = append(parts, "No open gaps.")
	} else {
		var g []string
		for i, x := range s.Gaps {
			if i == 3 {
				break
			}
			g = append(g, fmt.Sprintf("%s (%.0f%% off)", x.Name, x.Severity*100))
			a.Grounding = append(a.Grounding, "gap:"+x.KPI)
		}
		parts = append(parts, fmt.Sprintf("%d open gap(s): %s.", len(s.Gaps), strings.Join(g, ", ")))
	}
	best := -1
	for i, r := range s.Plan {
		if r.Status != "pending-approval" {
			continue
		}
		if best < 0 || len(r.Result.GapsClosed) > len(s.Plan[best].Result.GapsClosed) {
			best = i
		}
	}
	if best >= 0 {
		r := s.Plan[best]
		a.Grounding = append(a.Grounding, "plan:"+r.Action)
		if n := len(r.Result.GapsClosed); n >= 2 {
			parts = append(parts, fmt.Sprintf("One action closes %d: %s (closes %s), pending approval.", n, r.Name, joinAnd(r.Result.GapsClosed)))
		} else {
			parts = append(parts, fmt.Sprintf("Best next action: %s, pending approval.", r.Name))
		}
	}
	if f.LastMiss != "" {
		parts = append(parts, "Last approved change got this wrong: "+f.LastMiss)
		a.Grounding = append(a.Grounding, "explanation:"+f.LastMissProposal)
	}
	if len(f.StaleBefore) > 0 {
		var st []string
		for _, x := range f.StaleBefore {
			st = append(st, fmt.Sprintf("%s at %s", x.KPI, x.At.Format("Mon 15:04")))
			a.Grounding = append(a.Grounding, "freshness:"+x.KPI)
		}
		when := "the next window"
		if f.Next != nil && !f.Open {
			when = "the window opens again at " + f.Next.Format("Mon 15:04")
		}
		parts = append(parts, "Goes stale before "+when+": "+strings.Join(st, ", ")+".")
	}
	a.Text = strings.Join(parts, " ")
	a.Mode = "heuristic"
	return a
}

// Shift is ShiftDigest plus the optional rewrite.
func (e *Engine) Shift(ctx context.Context, s Snapshot, f ShiftFacts) Answer {
	return e.finish(ctx, "Write the close-of-window handover note", ShiftDigest(s, f), s)
}
