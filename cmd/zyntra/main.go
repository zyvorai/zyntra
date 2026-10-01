// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/zyvorai/zyntra/internal/adapters"
	"github.com/zyvorai/zyntra/internal/adapters/kubernetes"
	"github.com/zyvorai/zyntra/internal/adapters/prometheus"
	"github.com/zyvorai/zyntra/internal/api"
	"github.com/zyvorai/zyntra/internal/gaps"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/planner"
	"github.com/zyvorai/zyntra/internal/sim"
	"github.com/zyvorai/zyntra/web"
)

var version = "0.1.0"

const usage = `zyntra - decision intelligence for infrastructure ops

Usage:
  zyntra graph    -f kpis.yaml            show KPIs and dependencies
  zyntra gaps     -f kpis.yaml            KPIs missing their targets, worst first
  zyntra simulate -f kpis.yaml -action ID what-if: predicted KPI changes and why
  zyntra plan     -f kpis.yaml            rank all actions (all pending approval)
  zyntra serve    -f kpis.yaml            REST API, SSE pulse and dashboard
  zyntra version

Common flags:
  -f FILE           KPI model (default examples/kpis.yaml)
  -o text|json      output format (default text)
  -prometheus URL   refresh prometheus-sourced KPIs before running
  -kubectl          refresh kubernetes-sourced KPIs via kubectl get nodes
  -kubeconfig FILE  kubeconfig for -kubectl
`

type common struct {
	file, output, prom, kubeconfig string
	kubectl                        bool
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.file, "f", "examples/kpis.yaml", "KPI model file")
	fs.StringVar(&c.output, "o", "text", "output format: text|json")
	fs.StringVar(&c.prom, "prometheus", "", "Prometheus base URL")
	fs.BoolVar(&c.kubectl, "kubectl", false, "refresh kubernetes KPIs via kubectl")
	fs.StringVar(&c.kubeconfig, "kubeconfig", "", "kubeconfig path for -kubectl")
}

func (c *common) adapterConfig() adapters.Config {
	var cfg adapters.Config
	if c.prom != "" {
		cfg.Prometheus = prometheus.New(c.prom)
	}
	if c.kubectl {
		cfg.Kubernetes = kubernetes.Kubectl(c.kubeconfig)
	}
	return cfg
}

func (c *common) live() bool { return c.prom != "" || c.kubectl }

func (c *common) load(ctx context.Context) (*graph.Model, error) {
	m, err := graph.Load(c.file)
	if err != nil {
		return nil, err
	}
	if c.live() {
		if err := adapters.Refresh(ctx, m, c.adapterConfig()); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		}
	}
	return m, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1], os.Args[2:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "zyntra:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cmd string, args []string, out io.Writer) error {
	var c common
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	c.register(fs)
	switch cmd {
	case "graph":
		if err := fs.Parse(args); err != nil {
			return err
		}
		m, err := c.load(ctx)
		if err != nil {
			return err
		}
		if c.output == "json" {
			return emit(out, m)
		}
		printGraph(out, m)
	case "gaps":
		if err := fs.Parse(args); err != nil {
			return err
		}
		m, err := c.load(ctx)
		if err != nil {
			return err
		}
		g := gaps.Detect(m)
		if c.output == "json" {
			return emit(out, g)
		}
		printGaps(out, g, gaps.Total(m, nil))
	case "simulate":
		action := fs.String("action", "", "action id to simulate")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *action == "" {
			return fmt.Errorf("simulate: -action is required")
		}
		m, err := c.load(ctx)
		if err != nil {
			return err
		}
		r, err := sim.Simulate(m, *action)
		if err != nil {
			return err
		}
		if c.output == "json" {
			return emit(out, r)
		}
		printSim(out, r)
	case "plan":
		if err := fs.Parse(args); err != nil {
			return err
		}
		m, err := c.load(ctx)
		if err != nil {
			return err
		}
		recs, err := planner.Plan(m)
		if err != nil {
			return err
		}
		if c.output == "json" {
			return emit(out, recs)
		}
		printPlan(out, recs)
	case "serve":
		addr := fs.String("addr", ":8080", "listen address")
		interval := fs.Duration("interval", 15*time.Second, "refresh and pulse interval")
		if err := fs.Parse(args); err != nil {
			return err
		}
		return serve(ctx, &c, *addr, *interval)
	case "version", "-v", "--version":
		fmt.Fprintln(out, "zyntra", version)
	case "help", "-h", "--help":
		fmt.Fprint(out, usage)
	default:
		return fmt.Errorf("unknown command %q (run zyntra help)", cmd)
	}
	return nil
}

func serve(ctx context.Context, c *common, addr string, interval time.Duration) error {
	m, err := graph.Load(c.file)
	if err != nil {
		return err
	}
	var refresh api.RefreshFunc
	if c.live() {
		cfg := c.adapterConfig()
		refresh = func(ctx context.Context, m *graph.Model) error { return adapters.Refresh(ctx, m, cfg) }
	}
	s := api.New(m, refresh, interval, web.FS)
	go s.Run(ctx)

	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("zyntra %s serving %q on %s", version, m.Name, addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func emit(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func target(k graph.KPI) string {
	if k.Target == nil {
		return "-"
	}
	op := ">="
	if k.Direction == graph.LowerIsBetter {
		op = "<="
	}
	return op + " " + num(*k.Target)
}

func printGraph(w io.Writer, m *graph.Model) {
	fmt.Fprintf(w, "%s: %d KPIs, %d edges, %d actions\n\n", m.Name, len(m.KPIs), len(m.Edges), len(m.Actions))
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "KPI\tVALUE\tTARGET\tOWNER\tSOURCE")
	for _, k := range m.KPIs {
		src := "model"
		if k.Source != nil && k.Source.Kind != "" {
			src = k.Source.Kind
		}
		fmt.Fprintf(tw, "%s\t%s %s\t%s\t%s\t%s\n", k.ID, num(k.Value), k.Unit, target(k), k.Owner, src)
	}
	tw.Flush()
	fmt.Fprintln(w, "\nDependencies:")
	for _, e := range m.Edges {
		fmt.Fprintf(w, "  %s -> %s (weight %+.3g)\n", e.From, e.To, e.Weight)
	}
	fmt.Fprintln(w, "\nActions:")
	for _, a := range m.Actions {
		fmt.Fprintf(w, "  %s  %s [risk %s, via %s]\n", a.ID, a.Name, orDefault(string(a.Risk), "low"), orDefault(a.Adapter, "manual"))
	}
}

func printGaps(w io.Writer, g []gaps.Gap, total float64) {
	if len(g) == 0 {
		fmt.Fprintln(w, "All KPIs are on target.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "KPI\tOWNER\tNOW\tTARGET\tOFF BY")
	for _, x := range g {
		op := ">="
		if x.Direction == string(graph.LowerIsBetter) {
			op = "<="
		}
		fmt.Fprintf(tw, "%s\t%s\t%s %s\t%s %s\t%.1f%%\n", x.KPI, x.Owner, num(x.Value), x.Unit, op, num(x.Target), x.Severity*100)
	}
	tw.Flush()
	fmt.Fprintf(w, "\nTotal gap severity: %.3f\n", total)
}

func printSim(w io.Writer, r sim.Result) {
	fmt.Fprintf(w, "What if: %s (%s)\n\n", r.ActionName, r.Action)
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "KPI\tBEFORE\tAFTER\tCHANGE\tTARGET")
	for _, k := range r.KPIs {
		if k.Change == 0 {
			continue
		}
		status := "-"
		if k.HasTarget {
			status = map[bool]string{true: "met", false: "missed"}[k.MetAfter]
			if k.MetBefore != k.MetAfter {
				status += map[bool]string{true: " (closed)", false: " (opened)"}[k.MetAfter]
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", k.KPI, num(k.Before), num(k.After), sim.Pct(k.Change), status)
	}
	tw.Flush()
	fmt.Fprintln(w, "\nWhy:")
	for _, s := range r.Trace {
		fmt.Fprintln(w, "  "+s.Text)
	}
	fmt.Fprintf(w, "\nGap severity: %.3f -> %.3f (%+.3f)\n", r.SeverityBefore, r.SeverityAfter, -r.Improvement())
	if len(r.GapsClosed) > 0 {
		fmt.Fprintf(w, "Closes: %s\n", strings.Join(r.GapsClosed, ", "))
	}
	if len(r.GapsOpened) > 0 {
		fmt.Fprintf(w, "Opens:  %s\n", strings.Join(r.GapsOpened, ", "))
	}
}

func printPlan(w io.Writer, recs []planner.Recommendation) {
	if len(recs) == 0 {
		fmt.Fprintln(w, "No action reduces total gap severity.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "RANK\tACTION\tRISK\tGAIN\tSCORE\tCLOSES\tOPENS\tSTATUS")
	for _, r := range recs {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%.3f\t%.3f\t%s\t%s\t%s\n", r.Rank, r.Action, orDefault(string(r.Risk), "low"),
			r.Improvement, r.Score, list(r.Result.GapsClosed), list(r.Result.GapsOpened), r.Status)
	}
	tw.Flush()
}

func num(v float64) string {
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
}

func list(s []string) string {
	if len(s) == 0 {
		return "-"
	}
	return strings.Join(s, ",")
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
