// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/zyvorai/zyntra/internal/approvals"
	"github.com/zyvorai/zyntra/internal/calibrate"
)

// calibrateCmd prints the backtest of a pack's edge weights against the
// decisions recorded in a state directory. It only reads.
func calibrateCmd(ctx context.Context, c *common, fs *flag.FlagSet, args []string, out io.Writer) error {
	state := fs.String("state", env("ZYNTRA_STATE_DIR", "state"), "state directory holding approvals.json")
	minN := fs.Int("min-decisions", 5, "decisions a KPI needs before a correction is suggested")
	if err := fs.Parse(args); err != nil {
		return err
	}
	m, err := c.load(ctx)
	if err != nil {
		return err
	}
	st, err := approvals.Open(filepath.Join(*state, "approvals.json"))
	if err != nil {
		return err
	}
	rep := calibrate.Analyze(m, st.List(), calibrate.Options{MinSamples: *minN})
	if c.output == "json" {
		return emit(out, rep)
	}
	if rep.Note != "" {
		fmt.Fprintln(out, rep.Note)
		return nil
	}
	fmt.Fprintf(out, "%d finished decision(s) backtested\n\n", rep.Decisions)
	fmt.Fprintf(out, "%-28s %4s %12s %9s %9s\n", "KPI", "N", "MEAN ERROR", "HIT RATE", "BIAS")
	for _, k := range rep.KPIs {
		fmt.Fprintf(out, "%-28s %4d %11.1f%% %8.0f%% %+8.1f%%\n", k.KPI, k.N, k.MeanAbsError*100, k.HitRate*100, k.Bias*100)
	}
	for _, s := range rep.Suggestions {
		fmt.Fprintf(out, "\n%s\n  suggested edges (review, then edit the pack's kpis.yaml; nothing is applied):\n%s\n", s.Why, s.YAML)
	}
	for _, s := range rep.ActionSuggestions {
		fmt.Fprintf(out, "\n%s\n  suggested action effect (review, then edit the action's effects in the pack; nothing is applied):\n%s\n", s.Why, s.YAML)
	}
	for _, n := range rep.Notes {
		fmt.Fprintln(out, "\nno change:", n)
	}
	return nil
}
