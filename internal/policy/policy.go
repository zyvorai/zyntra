// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package policy decides who must approve a proposal, for how long the
// approval holds, when it may run and how strictly its inputs are checked.
// Rules come from an optional YAML file; with no file the defaults keep
// single-approver behaviour.
package policy

import (
	"fmt"
	"github.com/zyvorai/zyntra/internal/ontology"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/zyvorai/zyntra/internal/calendar"
	"github.com/zyvorai/zyntra/internal/graph"
)

// Keep modes.
const (
	KeepRequired  = "required"
	KeepPreferred = "preferred"
	KeepOff       = "off"
)

// Defaults used when neither the policy file nor the action sets a value.
const (
	DefaultPendingExpiry  = 24 * time.Hour
	DefaultApprovedExpiry = time.Hour
	DefaultMaxDrift       = 0.5
)

// Match selects actions. Empty fields match everything; a rule with an
// empty match applies to every action.
type Match struct {
	Actions  []string `yaml:"actions,omitempty" json:"actions,omitempty"`
	Risk     []string `yaml:"risk,omitempty" json:"risk,omitempty"`
	Adapters []string `yaml:"adapters,omitempty" json:"adapters,omitempty"`
}

func (m Match) matches(a graph.Action) bool {
	return in(m.Actions, a.ID) && in(m.Risk, string(orLow(a.Risk))) && in(m.Adapters, a.Adapter)
}

func orLow(r graph.Risk) graph.Risk {
	if r == "" {
		return graph.RiskLow
	}
	return r
}

func in(list []string, v string) bool {
	if len(list) == 0 {
		return true
	}
	for _, x := range list {
		if x == v || x == "*" {
			return true
		}
	}
	return false
}

// Rule sets requirements for matching actions. Unset fields leave earlier
// values alone; later rules override earlier ones.
type Rule struct {
	Name  string `yaml:"name,omitempty" json:"name,omitempty"`
	Match Match  `yaml:"match,omitempty" json:"match"`
	// Approvals is how many distinct approvers are needed.
	Approvals int `yaml:"approvals,omitempty" json:"approvals,omitempty"`
	// DistinctFromProposer forbids the proposer from approving.
	DistinctFromProposer *bool          `yaml:"distinctFromProposer,omitempty" json:"distinct_from_proposer,omitempty"`
	PendingExpiry        graph.Duration `yaml:"pendingExpiry,omitempty" json:"pending_expiry,omitempty"`
	ApprovedExpiry       graph.Duration `yaml:"approvedExpiry,omitempty" json:"approved_expiry,omitempty"`
	Windows              []string       `yaml:"maintenanceWindows,omitempty" json:"maintenance_windows,omitempty"`
	RequireFresh         *bool          `yaml:"requireFresh,omitempty" json:"require_fresh,omitempty"`
	Keep                 string         `yaml:"keep,omitempty" json:"keep,omitempty"`
}

// Window is a recurring maintenance window. End before Start wraps past
// midnight.
type Window = calendar.Window

// User is a local account for installs without an identity provider.
type User struct {
	Name         string   `yaml:"name" json:"name"`
	PasswordHash string   `yaml:"passwordHash" json:"-"`
	Roles        []string `yaml:"roles" json:"roles"`
}

type Policy struct {
	Rules        []Rule       `yaml:"rules,omitempty" json:"rules,omitempty"`
	Windows      calendar.Set `yaml:"maintenanceWindows,omitempty" json:"maintenance_windows,omitempty"`
	Revalidation struct {
		// MaxDrift is the largest fall in predicted improvement, as a
		// fraction, tolerated between approval and execution.
		MaxDrift *float64 `yaml:"maxDrift,omitempty" json:"max_drift,omitempty"`
	} `yaml:"revalidation,omitempty" json:"revalidation"`
	Users []User `yaml:"users,omitempty" json:"-"`
	// Access limits which business objects, properties and typed actions
	// each role may use. It is enforced on every read and on proposals.
	Access []ontology.Rule `yaml:"access,omitempty" json:"access,omitempty"`
}

// Effective is the merged policy for one proposal.
type Effective struct {
	Rules                []string      `json:"rules,omitempty"`
	Approvals            int           `json:"approvals"`
	DistinctFromProposer bool          `json:"distinct_from_proposer"`
	PendingExpiry        time.Duration `json:"-"`
	ApprovedExpiry       time.Duration `json:"-"`
	PendingExpiryText    string        `json:"pending_expiry"`
	ApprovedExpiryText   string        `json:"approved_expiry"`
	Windows              []string      `json:"maintenance_windows,omitempty"`
	RequireFresh         bool          `json:"require_fresh"`
	Keep                 string        `json:"keep"`
}

// Load reads a policy file; an empty path returns the default policy.
func Load(path string) (*Policy, error) {
	p := &Policy{}
	if path == "" {
		return p, p.validate()
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(p); err != nil {
		return nil, fmt.Errorf("policy %s: %w", path, err)
	}
	if err := p.validate(); err != nil {
		return nil, fmt.Errorf("policy %s: %w", path, err)
	}
	return p, nil
}

// Parse reads a policy from YAML text.
func Parse(text string) (*Policy, error) {
	p := &Policy{}
	dec := yaml.NewDecoder(strings.NewReader(text))
	dec.KnownFields(true)
	if err := dec.Decode(p); err != nil {
		return nil, err
	}
	return p, p.validate()
}

func (p *Policy) validate() error {
	if err := p.Windows.Compile(nil); err != nil {
		return err
	}
	for i, r := range p.Rules {
		if r.Approvals < 0 {
			return fmt.Errorf("rule %d: approvals must be >= 0", i)
		}
		if !graph.ValidKeepMode(r.Keep) {
			return fmt.Errorf("rule %d: keep must be required, preferred or off", i)
		}
		for _, w := range r.Windows {
			if _, ok := p.Windows[w]; !ok {
				return fmt.Errorf("rule %d: unknown maintenance window %q", i, w)
			}
		}
	}
	if d := p.Revalidation.MaxDrift; d != nil && (*d < 0 || *d > 1) {
		return fmt.Errorf("revalidation.maxDrift must be between 0 and 1")
	}
	seen := map[string]bool{}
	for _, u := range p.Users {
		if u.Name == "" || u.PasswordHash == "" {
			return fmt.Errorf("users need a name and passwordHash")
		}
		if seen[u.Name] {
			return fmt.Errorf("duplicate user %q", u.Name)
		}
		seen[u.Name] = true
	}
	return nil
}

// UseModel adds the model's calendars as windows. Windows in the policy
// file win over model calendars with the same name.
func (p *Policy) UseModel(m *graph.Model) {
	if len(m.Calendars) == 0 {
		return
	}
	if p.Windows == nil {
		p.Windows = calendar.Set{}
	}
	for name, w := range m.Calendars {
		if _, ok := p.Windows[name]; !ok {
			p.Windows[name] = w
		}
	}
}

// CheckModel reports actions that name unknown windows. Call UseModel first
// so model calendars count.
func (p *Policy) CheckModel(m *graph.Model) error {
	for _, a := range m.Actions {
		var names []string
		if a.Window != "" {
			names = append(names, a.Window)
		}
		if a.Policy != nil {
			names = append(names, a.Policy.Windows...)
		}
		for _, w := range names {
			if _, ok := p.Windows[w]; !ok {
				return fmt.Errorf("action %s: unknown window %q", a.ID, w)
			}
		}
	}
	return nil
}

// MaxDrift returns the drift tolerance for revalidation.
func (p *Policy) MaxDrift() float64 {
	if p == nil || p.Revalidation.MaxDrift == nil {
		return DefaultMaxDrift
	}
	return *p.Revalidation.MaxDrift
}

func (p *Policy) one(a graph.Action) Effective {
	e := Effective{Approvals: 1, PendingExpiry: DefaultPendingExpiry, ApprovedExpiry: DefaultApprovedExpiry, Keep: KeepPreferred}
	apply := func(r Rule) {
		if r.Approvals > 0 {
			e.Approvals = r.Approvals
		}
		if r.DistinctFromProposer != nil {
			e.DistinctFromProposer = *r.DistinctFromProposer
		}
		if r.PendingExpiry > 0 {
			e.PendingExpiry = r.PendingExpiry.D()
		}
		if r.ApprovedExpiry > 0 {
			e.ApprovedExpiry = r.ApprovedExpiry.D()
		}
		if len(r.Windows) > 0 {
			e.Windows = r.Windows
		}
		if r.RequireFresh != nil {
			e.RequireFresh = *r.RequireFresh
		}
		if r.Keep != "" {
			e.Keep = r.Keep
		}
	}
	if p != nil {
		for _, r := range p.Rules {
			if r.Match.matches(a) {
				apply(r)
				if r.Name != "" {
					e.Rules = append(e.Rules, r.Name)
				}
			}
		}
	}
	if a.Approvers > 0 || a.Window != "" {
		r := Rule{Approvals: a.Approvers}
		if a.Window != "" {
			r.Windows = []string{a.Window}
		}
		apply(r)
		e.Rules = append(e.Rules, "action:"+a.ID)
	}
	if ap := a.Policy; ap != nil {
		apply(Rule{Approvals: ap.Approvals, Keep: ap.Keep, RequireFresh: ap.RequireFresh, Windows: ap.Windows})
		e.Rules = append(e.Rules, "action:"+a.ID)
	}
	return e
}

var keepRank = map[string]int{KeepOff: 0, KeepPreferred: 1, KeepRequired: 2}

// For merges the policy for one or more actions taken together, keeping the
// strictest requirement of each kind.
func (p *Policy) For(actions ...graph.Action) Effective {
	var e Effective
	for i, a := range actions {
		x := p.one(a)
		if i == 0 {
			e = x
			continue
		}
		e.Rules = append(e.Rules, x.Rules...)
		e.Approvals = max(e.Approvals, x.Approvals)
		e.DistinctFromProposer = e.DistinctFromProposer || x.DistinctFromProposer
		e.PendingExpiry = min(e.PendingExpiry, x.PendingExpiry)
		e.ApprovedExpiry = min(e.ApprovedExpiry, x.ApprovedExpiry)
		e.Windows = append(e.Windows, x.Windows...)
		e.RequireFresh = e.RequireFresh || x.RequireFresh
		if keepRank[x.Keep] > keepRank[e.Keep] {
			e.Keep = x.Keep
		}
	}
	if len(actions) == 0 {
		e = p.one(graph.Action{})
	}
	e.Windows = dedupe(e.Windows)
	e.Rules = dedupe(e.Rules)
	e.PendingExpiryText, e.ApprovedExpiryText = e.PendingExpiry.String(), e.ApprovedExpiry.String()
	return e
}

func dedupe(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

// InWindow reports whether t falls in any of the named windows. With no
// windows every time is allowed. The returned text explains a refusal.
func (p *Policy) InWindow(names []string, t time.Time) (bool, string) {
	if len(names) == 0 {
		return true, ""
	}
	if p.Windows.Any(names, t) {
		return true, ""
	}
	return false, "outside window " + strings.Join(names, ", ")
}

// User looks up a local account.
func (p *Policy) User(name string) (User, bool) {
	if p == nil {
		return User{}, false
	}
	for _, u := range p.Users {
		if u.Name == name {
			return u, true
		}
	}
	return User{}, false
}
