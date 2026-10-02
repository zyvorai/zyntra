// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/zyvorai/zyntra/internal/actions"
	"github.com/zyvorai/zyntra/internal/adapters"
	"github.com/zyvorai/zyntra/internal/api"
	"github.com/zyvorai/zyntra/internal/connector"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/ontology"
	"github.com/zyvorai/zyntra/internal/pack"
	"github.com/zyvorai/zyntra/internal/policy"
	"github.com/zyvorai/zyntra/internal/scenario"
)

// buildOntology opens the pack's business-object layer. A pack without
// ontology.yaml yields the zero value, which switches the layer off.
func buildOntology(path string, m *graph.Model, pol *policy.Policy, stateDir string) (api.OntologyOptions, error) {
	def, dir, err := pack.LoadOntology(path, m)
	if err != nil || def == nil {
		return api.OntologyOptions{}, err
	}
	// ZYNTRA_ONTOLOGY_STORE=sqlite keeps objects in ontology.db (one write per
	// ingest batch, change log on disk). An existing ontology.db is used even
	// when the variable is unset, so a migrated install keeps working.
	file := "ontology.json"
	if strings.EqualFold(env("ZYNTRA_ONTOLOGY_STORE", ""), "sqlite") {
		file = "ontology.db"
	} else if _, err := os.Stat(filepath.Join(stateDir, "ontology.db")); err == nil {
		file = "ontology.db"
	}
	st, err := ontology.Open(filepath.Join(stateDir, file), def.Schema())
	if err != nil {
		return api.OntologyOptions{}, fmt.Errorf("ontology: %w", err)
	}
	ac := &ontology.Access{Schema: def.Schema()}
	if pol != nil {
		ac.Rules = pol.Access
	}
	scn, err := scenario.Open(filepath.Join(stateDir, "scenarios.json"))
	if err != nil {
		return api.OntologyOptions{}, fmt.Errorf("scenarios: %w", err)
	}
	load := adapters.NewFileCache().Load
	opt := connector.Options{Kubeconfig: env("ZYNTRA_KUBECONFIG", "")}
	sched, err := connector.NewScheduler(st, def, dir, load, opt, filepath.Join(stateDir, "connectors.json"))
	if err != nil {
		return api.OntologyOptions{}, fmt.Errorf("connectors: %w", err)
	}
	return api.OntologyOptions{Def: def, Store: st, Dir: dir, Access: ac,
		Actions: actions.New(def, st, ac), Scenarios: scn, Load: load, Scheduler: sched, Connector: opt}, nil
}

// memoryOntology loads the pack's ontology into a throwaway store.
func memoryOntology(c *common, m *graph.Model) (*ontology.Definition, *ontology.Store, error) {
	def, dir, err := pack.LoadOntology(c.file, m)
	if err != nil {
		return nil, nil, err
	}
	if def == nil {
		return nil, nil, fmt.Errorf("%s has no %s", c.file, ontology.FileName)
	}
	st, err := ontology.Open("", def.Schema())
	if err != nil {
		return nil, nil, err
	}
	if _, err := st.IngestMappings(def, dir, "cli", adapters.NewFileCache().Load, time.Now().UTC()); err != nil {
		return nil, nil, err
	}
	return def, st, nil
}

func ontologyCmd(ctx context.Context, c *common, fs *flag.FlagSet, args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("ontology: want validate, dump or impact")
	}
	sub := args[0]
	from := fs.String("from", "", "migrate: source store (ontology.json or .db)")
	to := fs.String("to", "", "migrate: destination store (a new .db or .json file)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	m, err := c.load(ctx)
	if err != nil {
		return err
	}
	if sub == "migrate" {
		def, _, err := pack.LoadOntology(c.file, m)
		if err != nil || def == nil {
			return fmt.Errorf("migrate: the pack has no %s: %v", ontology.FileName, err)
		}
		if *from == "" || *to == "" {
			return fmt.Errorf("ontology migrate -f PACK -from STORE -to STORE")
		}
		o, l, err := ontology.Migrate(*from, *to, def.Schema())
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "copied %d objects and %d links from %s to %s\n", o, l, *from, *to)
		return nil
	}
	def, st, err := memoryOntology(c, m)
	if err != nil {
		return err
	}
	switch sub {
	case "validate":
		fmt.Fprintf(out, "ok: %d object types, %d link types, %d typed actions, %d views, %d objects, %d links, %d identity candidates\n",
			len(def.Objects), len(def.Links), len(def.Actions), len(def.Views),
			len(st.List("")), countLinks(st), len(st.Candidates()))
	case "dump":
		if c.output == "json" {
			return emit(out, map[string]any{"objects": st.List(""), "candidates": st.Candidates()})
		}
		tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tLINKS")
		for _, o := range st.List("") {
			name := ""
			if v, ok := o.Props["name"]; ok {
				name = fmt.Sprint(v.V)
			}
			fmt.Fprintf(tw, "%s\t%s\t%d\n", o.ID, name, len(st.Links(o.ID)))
		}
		return tw.Flush()
	case "impact":
		if fs.NArg() != 1 {
			return fmt.Errorf("ontology impact: want an object id")
		}
		im := st.Impact(fs.Arg(0), 0)
		if c.output == "json" {
			return emit(out, im)
		}
		if len(im) == 0 {
			fmt.Fprintln(out, "nothing depends on", fs.Arg(0))
		}
		for _, i := range im {
			fmt.Fprintf(out, "%s%s  (%s, via %s)\n", strings.Repeat("  ", i.Depth-1), i.Object.ID, i.Object.Type, i.Via)
		}
	default:
		return fmt.Errorf("ontology: unknown subcommand %q", sub)
	}
	return nil
}

func countLinks(st *ontology.Store) int {
	n := 0
	for _, o := range st.List("") {
		for _, l := range st.Links(o.ID) {
			if l.From == o.ID {
				n++
			}
		}
	}
	return n
}

// scenarioCmd runs or compares what-if plans without saving them.
//
//	zyntra scenario run -f PACK [-set kpi=value] a+b
//	zyntra scenario compare -f PACK name=a+b name2=c
func scenarioCmd(ctx context.Context, c *common, fs *flag.FlagSet, args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("scenario: want run or compare")
	}
	sub := args[0]
	set := fs.String("set", "", "assumptions, comma separated kpi=value")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	m, err := c.load(ctx)
	if err != nil {
		return err
	}
	assume := map[string]float64{}
	for _, kv := range strings.FieldsFunc(*set, func(r rune) bool { return r == ',' }) {
		k, v, ok := strings.Cut(kv, "=")
		f, perr := strconv.ParseFloat(v, 64)
		if !ok || perr != nil {
			return fmt.Errorf("scenario: bad assumption %q", kv)
		}
		assume[strings.TrimSpace(k)] = f
	}
	var rd *ontology.Reader
	data := ""
	if _, st, err := memoryOntology(c, m); err == nil {
		r := st.As(nil, ontology.Principal{Roles: []string{"admin"}})
		rd, data = &r, st.Fingerprint()
	}
	var scs []scenario.Scenario
	for i, a := range fs.Args() {
		name, plan, ok := strings.Cut(a, "=")
		if !ok {
			name, plan = "scenario-"+strconv.Itoa(i+1), a
		}
		sc, err := scenario.Run(m, rd, data, scenario.Scenario{ID: name, Name: name, Actions: strings.Split(plan, "+"), Assumptions: assume}, time.Now().UTC())
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		scs = append(scs, sc)
	}
	switch sub {
	case "run":
		if len(scs) != 1 {
			return fmt.Errorf("scenario run: want one plan like a+b")
		}
		if c.output == "json" {
			return emit(out, scs[0])
		}
		printScenario(out, scs[0])
	case "compare":
		cmp, err := scenario.Compare(scs)
		if err != nil {
			return err
		}
		if c.output == "json" {
			return emit(out, cmp)
		}
		tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		fmt.Fprintf(tw, "KPI\tBEFORE\t%s\n", strings.Join(cmp.Scenarios, "\t"))
		for _, r := range cmp.Rows {
			if changed(r) {
				cells := make([]string, len(r.After))
				for i, v := range r.After {
					cells[i] = strconv.FormatFloat(v, 'f', -1, 64)
					if !r.Met[i] {
						cells[i] += " (miss)"
					}
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", r.KPI, strconv.FormatFloat(r.Before, 'f', -1, 64), strings.Join(cells, "\t"))
			}
		}
		fmt.Fprintf(tw, "weighted severity\t\t%s\n", joinF(cmp.Weighted))
		fmt.Fprintf(tw, "objects at risk\t\t%s\n", joinI(cmp.AtRisk))
		return tw.Flush()
	default:
		return fmt.Errorf("scenario: unknown subcommand %q", sub)
	}
	return nil
}

func changed(r scenario.Row) bool {
	for _, v := range r.After {
		if v != r.Before {
			return true
		}
	}
	return false
}

func joinF(v []float64) string {
	s := make([]string, len(v))
	for i, x := range v {
		s[i] = strconv.FormatFloat(x, 'f', 3, 64)
	}
	return strings.Join(s, "\t")
}

func joinI(v []int) string {
	s := make([]string, len(v))
	for i, x := range v {
		s[i] = strconv.Itoa(x)
	}
	return strings.Join(s, "\t")
}

func printScenario(out io.Writer, sc scenario.Scenario) {
	r := sc.Result
	fmt.Fprintf(out, "scenario %s: %s\n", sc.Name, strings.Join(sc.Actions, " + "))
	fmt.Fprintf(out, "weighted severity %.3f -> %.3f; closes %v; opens %v\n", r.WeightedBefore, r.WeightedAfter, r.Closes, r.Opens)
	for _, b := range r.Blocked {
		fmt.Fprintln(out, "blocked:", b)
	}
	fmt.Fprintf(out, "objects at risk %d -> %d, exposed %d -> %d\n", len(r.AtRiskBefore), len(r.AtRiskAfter), len(r.ExposedBefore), len(r.ExposedAfter))
}
