// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package review

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zyvorai/zyntra/internal/approvals"
)

// Precedent is a past decision that looks like the one at hand.
type Precedent struct {
	Proposal string    `json:"proposal"`
	Action   string    `json:"action"`
	Name     string    `json:"name"`
	At       time.Time `json:"at"`
	Score    float64   `json:"score"`
	Overlap  []string  `json:"overlap"`
	// Result is how it ended: the outcome verdict, or the proposal status.
	Result  string   `json:"result"`
	HitRate *float64 `json:"hit_rate,omitempty"`
	Text    string   `json:"text"`
}

// Precedents is the list plus a one-line summary for the inbox.
type Precedents struct {
	Items []Precedent `json:"items"`
	Text  string      `json:"text"`
}

func result(p approvals.Proposal) string {
	if p.Outcome != nil && p.Outcome.Done() {
		return string(p.Outcome.State)
	}
	if p.Execution != nil && !p.Execution.OK {
		return "failed"
	}
	return string(p.Status)
}

func kpiSet(p approvals.Proposal) map[string]bool {
	set := map[string]bool{}
	for k := range p.Predicted.KPIs {
		set[k] = true
	}
	for _, k := range p.Predicted.Closes {
		set[k] = true
	}
	if p.Simulation != nil {
		for _, v := range p.Simulation.Violations {
			set[v.KPI] = true
		}
	}
	return set
}

func tokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, t := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return r < 'a' || r > 'z' }) {
		if len(t) > 2 {
			out[t] = true
		}
	}
	return out
}

// Similar ranks past decisions by KPI-id overlap (Jaccard) with the
// current one, plus a bonus for the same action and for shared words in
// the action ids. No embeddings; the ranker is untouched.
func Similar(all []approvals.Proposal, cur approvals.Proposal, n int) Precedents {
	want := kpiSet(cur)
	words := tokens(strings.Join(append([]string{cur.Action}, cur.Actions...), " "))
	var items []Precedent
	for _, p := range all {
		if p.ID == cur.ID || p.Status == approvals.Pending || p.RollbackOf != "" {
			continue
		}
		have := kpiSet(p)
		var overlap []string
		union := map[string]bool{}
		for k := range want {
			union[k] = true
			if have[k] {
				overlap = append(overlap, k)
			}
		}
		for k := range have {
			union[k] = true
		}
		score := 0.0
		if len(union) > 0 {
			score = float64(len(overlap)) / float64(len(union))
		}
		if p.Action == cur.Action {
			score += 0.5
		} else {
			pw := tokens(strings.Join(append([]string{p.Action}, p.Actions...), " "))
			shared := 0
			for w := range words {
				if pw[w] {
					shared++
				}
			}
			if len(words) > 0 {
				score += 0.25 * float64(shared) / float64(len(words))
			}
		}
		if score < 0.2 {
			continue
		}
		sort.Strings(overlap)
		pr := Precedent{Proposal: p.ID, Action: p.Action, Name: p.ActionName, At: p.CreatedAt, Score: round3(score), Overlap: overlap, Result: result(p)}
		if p.Outcome != nil {
			if hr, ok := p.Outcome.HitRate(); ok {
				pr.HitRate = &hr
			}
		}
		pr.Text = fmt.Sprintf("%s on %s: %s", p.Action, p.CreatedAt.Format("2006-01-02"), pr.Result)
		if len(p.BlockedReasons) > 0 {
			pr.Text += " (" + p.BlockedReasons[0] + ")"
		} else if p.Explanation != nil {
			for _, f := range p.Explanation.Findings {
				if f.Kind != "hit" {
					pr.Text += " (" + f.Text + ")"
					break
				}
			}
		}
		items = append(items, pr)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return items[i].At.After(items[j].At)
	})
	if n > 0 && len(items) > n {
		items = items[:n]
	}
	return Precedents{Items: items, Text: precedentText(items, want)}
}

func precedentText(items []Precedent, want map[string]bool) string {
	if len(items) == 0 {
		return "No similar past decision on record."
	}
	type tally struct {
		order   int
		results map[string]int
	}
	by := map[string]*tally{}
	var order []string
	for _, it := range items {
		t := by[it.Action]
		if t == nil {
			t = &tally{order: len(order), results: map[string]int{}}
			by[it.Action] = t
			order = append(order, it.Action)
		}
		t.results[it.Result]++
	}
	var parts []string
	for _, a := range order {
		var rs []string
		for _, r := range sortedKeys(by[a].results) {
			c := by[a].results[r]
			if c == 1 {
				rs = append(rs, r)
			} else {
				rs = append(rs, fmt.Sprintf("%s %d times", r, c))
			}
		}
		parts = append(parts, a+" "+strings.Join(rs, ", "))
	}
	return fmt.Sprintf("Last %d time(s) %s looked like this: %s.", len(items), strings.Join(sortedKeys(want), ", "), strings.Join(parts, "; "))
}

func round3(v float64) float64 { return float64(int(v*1000+0.5)) / 1000 }
