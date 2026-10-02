// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package review

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/planner"
)

// Rule is a line of the pack README that states a rule about an action.
type Rule struct {
	Line   int    `json:"line"`
	Text   string `json:"text"`
	Action string `json:"action,omitempty"`
}

// Contradiction is a scored action that breaks one of the pack's own rules.
type Contradiction struct {
	Action string `json:"action"`
	Rule   Rule   `json:"rule"`
	Why    string `json:"why"`
	// By is "rule" for a deterministic check or "model" when a model read
	// the rule; model findings must name a real action and a real rule.
	By string `json:"by"`
}

var (
	ruleWord   = regexp.MustCompile(`(?i)\b(only|never|cannot|can't|must not|must|may not|not allowed|requires?|two-person|precondition|compensate)\b`)
	onlyIn     = regexp.MustCompile("(?i)only (?:in|during|inside) (?:the )?`([a-z0-9_-]+)`")
	precondRe  = regexp.MustCompile("(?i)(?:precondition:?|only (?:when|if))[^`|]*`?([a-z][a-z0-9_]+)`?")
	compRe     = regexp.MustCompile("(?i)compensate:?\\s*`([a-z0-9_]+)`")
	neverRe    = regexp.MustCompile(`(?i)\b(never|cannot|can't|must not|may not|not allowed)\b.*\b(approv|run|propos|execut)`)
	twoRe      = regexp.MustCompile(`(?i)two-person|two approvers|approvers:\s*2|second approver`)
	backticked = regexp.MustCompile("`([a-z0-9_]+)`")
)

// Rules pulls rule lines out of a pack README: lines with a rule word that
// name an action id (in backticks or plain).
func Rules(readme string, m *graph.Model) []Rule {
	var out []Rule
	for i, line := range strings.Split(readme, "\n") {
		if !ruleWord.MatchString(line) {
			continue
		}
		r := Rule{Line: i + 1, Text: strings.TrimSpace(line)}
		for _, g := range backticked.FindAllStringSubmatch(line, -1) {
			if _, ok := m.Action(g[1]); ok {
				r.Action = g[1]
				break
			}
		}
		if r.Action == "" {
			for _, a := range m.Actions {
				if strings.Contains(line, a.ID) {
					r.Action = a.ID
					break
				}
			}
		}
		out = append(out, r)
	}
	return out
}

func approvable(plan []planner.Recommendation) map[string]bool {
	out := map[string]bool{}
	for _, r := range plan {
		if r.Status == planner.StatusPendingApproval {
			for _, id := range strings.Split(r.Action, "+") {
				out[id] = true
			}
		}
	}
	return out
}

// Check compares each rule that names an action with that action's
// definition and its place in the live plan. It is a linter: it flags,
// it never changes the plan.
func Check(m *graph.Model, plan []planner.Recommendation, rules []Rule) []Contradiction {
	scored := approvable(plan)
	var out []Contradiction
	flag := func(a string, r Rule, why string) {
		out = append(out, Contradiction{Action: a, Rule: r, Why: why, By: "rule"})
	}
	for _, r := range rules {
		if r.Action == "" {
			continue
		}
		a, _ := m.Action(r.Action)
		if g := onlyIn.FindStringSubmatch(r.Text); g != nil {
			win := g[1]
			ok := a.Window == win || (a.Policy != nil && slices.Contains(a.Policy.Windows, win))
			if !ok {
				flag(a.ID, r, fmt.Sprintf("the rule limits %s to the %s window, but the action has window %q", a.ID, win, a.Window))
			}
		}
		if g := compRe.FindStringSubmatch(r.Text); g != nil && a.Compensate != g[1] {
			flag(a.ID, r, fmt.Sprintf("the rule names %s as the undo, but the action's compensate is %q", g[1], a.Compensate))
		}
		if g := precondRe.FindStringSubmatch(r.Text); g != nil {
			if _, isKPI := m.KPI(g[1]); isKPI {
				has := false
				for _, pc := range a.Preconditions {
					has = has || pc.KPI == g[1]
				}
				if !has {
					flag(a.ID, r, fmt.Sprintf("the rule makes %s a precondition, but the action has no precondition on it", g[1]))
				}
			}
		}
		if twoRe.MatchString(r.Text) {
			n := a.Approvers
			if a.Policy != nil && a.Policy.Approvals > n {
				n = a.Policy.Approvals
			}
			if n < 2 {
				flag(a.ID, r, "the rule asks for two people, but the action needs one approval")
			}
		}
		if neverRe.MatchString(r.Text) && scored[a.ID] {
			flag(a.ID, r, "the rule says it must not be approved, yet it is scored and approvable in the live plan")
		}
	}
	return out
}

// Unchecked returns rules naming an action that no deterministic check
// covered, for a model to read.
func Unchecked(rules []Rule) []Rule {
	var out []Rule
	for _, r := range rules {
		if r.Action == "" {
			continue
		}
		if onlyIn.MatchString(r.Text) || compRe.MatchString(r.Text) || precondRe.MatchString(r.Text) || twoRe.MatchString(r.Text) || neverRe.MatchString(r.Text) {
			continue
		}
		out = append(out, r)
	}
	return out
}
