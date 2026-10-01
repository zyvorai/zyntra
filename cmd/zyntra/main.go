// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	keeppack "github.com/zyvorai/zyntra/keep"

	"github.com/zyvorai/zyntra/internal/adapters"
	"github.com/zyvorai/zyntra/internal/adapters/httpsrc"
	"github.com/zyvorai/zyntra/internal/adapters/kubernetes"
	"github.com/zyvorai/zyntra/internal/adapters/prometheus"
	"github.com/zyvorai/zyntra/internal/ai"
	"github.com/zyvorai/zyntra/internal/api"
	"github.com/zyvorai/zyntra/internal/approvals"
	"github.com/zyvorai/zyntra/internal/auth"
	"github.com/zyvorai/zyntra/internal/executor"
	"github.com/zyvorai/zyntra/internal/gaps"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/keep"
	"github.com/zyvorai/zyntra/internal/planner"
	"github.com/zyvorai/zyntra/internal/sim"
	"github.com/zyvorai/zyntra/internal/tlsutil"
	"github.com/zyvorai/zyntra/web"
)

var version = "0.2.0"

const usage = `zyntra - decision intelligence for infrastructure ops

Usage:
  zyntra graph    -f kpis.yaml            show KPIs and dependencies
  zyntra gaps     -f kpis.yaml            KPIs missing their targets, worst first
  zyntra simulate -f kpis.yaml -action ID what-if: predicted KPI changes and why
  zyntra plan     -f kpis.yaml            rank all actions (all pending approval)
  zyntra serve    -f kpis.yaml            web console, REST API and SSE pulse
  zyntra keep deploy|pubkey|credential    Fabric Keep executor agent
  zyntra fake-sources -addr :19700        dev: fake Netra/Gravia/Fabric/Keep sources
  zyntra exec-token                       print a random token for ZYNTRA_EXEC_TOKEN
  zyntra version

Common flags:
  -f FILE           KPI model (default examples/kpis.yaml)
  -o text|json      output format (default text)
  -prometheus URL   refresh prometheus-sourced KPIs before running
  -kubectl          refresh kubernetes-sourced KPIs via kubectl get nodes
  -kubeconfig FILE  kubeconfig for -kubectl

Live sources and integrations are configured by environment; see README.
`

type common struct {
	file, output, prom, kubeconfig string
	kubectl                        bool
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.file, "f", "examples/kpis.yaml", "KPI model file")
	fs.StringVar(&c.output, "o", "text", "output format: text|json")
	fs.StringVar(&c.prom, "prometheus", env("ZYNTRA_PROMETHEUS_URL", ""), "Prometheus base URL")
	fs.BoolVar(&c.kubectl, "kubectl", envBool("ZYNTRA_KUBECTL"), "refresh kubernetes KPIs via kubectl")
	fs.StringVar(&c.kubeconfig, "kubeconfig", env("ZYNTRA_KUBECONFIG", ""), "kubeconfig path")
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func envBool(k string) bool {
	switch strings.ToLower(os.Getenv(k)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// endpoints builds JSON/metrics clients for the integrations configured in
// the environment.
func endpoints() map[string]*httpsrc.Client {
	insecure := envBool("ZYNTRA_ENDPOINT_INSECURE")
	out := map[string]*httpsrc.Client{}
	add := func(name, urlKey, tokKey string) *httpsrc.Client {
		u := env(urlKey, "")
		if u == "" {
			return nil
		}
		c := httpsrc.New(name, u, insecure)
		if t := env(tokKey, ""); t != "" {
			c.WithBearer(t)
		}
		out[name] = c
		return c
	}
	add("netra", "ZYNTRA_NETRA_URL", "ZYNTRA_NETRA_TOKEN")
	add("gravia", "ZYNTRA_GRAVIA_URL", "ZYNTRA_GRAVIA_TOKEN")
	add("keep", "ZYNTRA_KEEP_URL", "ZYNTRA_KEEP_TOKEN")
	if c := add("fabric", "ZYNTRA_FABRIC_URL", "ZYNTRA_FABRIC_TOKEN"); c != nil {
		if user, pass := env("ZYNTRA_FABRIC_USER", "admin"), env("ZYNTRA_FABRIC_PASSWORD", ""); pass != "" {
			c.WithLogin(fabricLogin(user, pass))
		}
	}
	return out
}

// fabricLogin exchanges Fabric admin credentials for a session JWT.
func fabricLogin(user, pass string) httpsrc.TokenFunc {
	return func(ctx context.Context, c *httpsrc.Client) (string, error) {
		var out struct {
			Token string `json:"token"`
		}
		if err := c.PostJSONAnon(ctx, "/api/v1/auth/login", map[string]string{"username": user, "password": pass}, &out); err != nil {
			return "", fmt.Errorf("fabric login: %w", err)
		}
		if out.Token == "" {
			return "", fmt.Errorf("fabric login returned no token")
		}
		return out.Token, nil
	}
}

func (c *common) adapterConfig() adapters.Config {
	cfg := adapters.Config{Endpoints: endpoints(), Rates: adapters.NewRateTracker()}
	if c.prom != "" {
		cfg.Prometheus = prometheus.New(c.prom)
	}
	if c.kubectl {
		cfg.Kubernetes = kubernetes.Kubectl(c.kubeconfig)
	}
	return cfg
}

func (c *common) live() bool { return c.prom != "" || c.kubectl || len(endpoints()) > 0 }

func (c *common) load(ctx context.Context) (*graph.Model, error) {
	m, err := graph.Load(c.file)
	if err != nil {
		return nil, err
	}
	if c.live() {
		if _, err := adapters.Refresh(ctx, m, c.adapterConfig()); err != nil {
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
		addr := fs.String("addr", env("ZYNTRA_LISTEN", ":8080"), "listen address")
		interval := fs.Duration("interval", 15*time.Second, "refresh and pulse interval")
		if err := fs.Parse(args); err != nil {
			return err
		}
		return serve(ctx, &c, *addr, *interval)
	case "keep":
		return keepCmd(ctx, args, out)
	case "fake-sources":
		addr := flag.NewFlagSet(cmd, flag.ContinueOnError)
		a := addr.String("addr", "127.0.0.1:19700", "listen address")
		if err := addr.Parse(args); err != nil {
			return err
		}
		return fakeSources(ctx, *a)
	case "exec-token":
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		fmt.Fprintln(out, hex.EncodeToString(b))
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
		refresh = func(ctx context.Context, m *graph.Model) ([]adapters.Status, error) {
			return adapters.Refresh(ctx, m, cfg)
		}
	}

	stateDir := env("ZYNTRA_STATE_DIR", "state")
	store, err := approvals.Open(filepath.Join(stateDir, "approvals.json"))
	if err != nil {
		return err
	}
	mode, err := executor.ParseMode(env("ZYNTRA_EXECUTE", "dry-run"))
	if err != nil {
		return err
	}
	engine := &ai.Engine{}
	if u := env("ZYNTRA_AI_BASE_URL", ""); u != "" {
		engine.LLM = ai.NewProvider(u, env("ZYNTRA_AI_API_KEY", ""), env("ZYNTRA_AI_MODEL", ""),
			env("ZYNTRA_AI_LABEL", "Fabric AI gateway"), envBool("ZYNTRA_AI_INSECURE"))
	}
	execToken := env("ZYNTRA_EXEC_TOKEN", "")
	opts := api.Options{
		Model: m, Refresh: refresh, Interval: interval, Static: web.FS(),
		Auth:     auth.New(env("ZYNTRA_API_KEY", ""), execToken),
		AI:       engine,
		History:  ai.NewHistory(0),
		Store:    store,
		Executor: &executor.Executor{Mode: mode, Run: executor.Kubectl(c.kubeconfig)},
		StateDir: stateDir, Version: version, Host: env("ZYNTRA_HOST", hostname()),
		ApprovalMode:       env("ZYNTRA_APPROVAL_MODE", api.ModeLocal),
		KeepDoubleApproval: envBool("ZYNTRA_KEEP_DOUBLE_APPROVAL"),
	}
	if u := env("ZYNTRA_KEEP_URL", ""); u != "" {
		opts.Keep = keep.NewBridge(keep.New(u, env("ZYNTRA_KEEP_TOKEN", "")))
	}
	if opts.ApprovalMode != api.ModeLocal && opts.ApprovalMode != api.ModeKeep {
		return fmt.Errorf("ZYNTRA_APPROVAL_MODE must be local or keep")
	}
	if opts.ApprovalMode == api.ModeKeep && (opts.Keep == nil || execToken == "") {
		return fmt.Errorf("keep approvals need ZYNTRA_KEEP_URL and ZYNTRA_EXEC_TOKEN")
	}
	if !opts.Auth.Required() {
		log.Printf("warning: ZYNTRA_API_KEY is not set; the console and API are open")
	}

	execAddr := env("ZYNTRA_EXEC_TLS_ADDR", "")
	var (
		execCert  tls.Certificate
		execFiles tlsutil.Files
	)
	if execAddr != "" && execToken != "" {
		if execFiles, execCert, err = tlsutil.Ensure(filepath.Join(stateDir, "tls")); err != nil {
			return fmt.Errorf("exec tls: %w", err)
		}
		opts.ExecURL = "https://" + execAddr
	}
	opts.ExecURL = env("ZYNTRA_EXEC_URL", opts.ExecURL)
	if opts.ApprovalMode == api.ModeKeep && opts.ExecURL == "" {
		return fmt.Errorf("keep approvals need ZYNTRA_EXEC_TLS_ADDR or ZYNTRA_EXEC_URL")
	}

	s := api.New(opts)
	go s.Run(ctx)

	var servers []*http.Server
	if execFiles.CA != "" {
		es := &http.Server{Addr: execAddr, Handler: s.ExecHandler(), ReadHeaderTimeout: 10 * time.Second,
			TLSConfig: &tls.Config{Certificates: []tls.Certificate{execCert}, MinVersion: tls.VersionTLS12}}
		servers = append(servers, es)
		go func() {
			log.Printf("exec listener on https://%s (CA %s)", execAddr, execFiles.CA)
			if err := es.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				log.Printf("exec listener: %v", err)
			}
		}()
	}

	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	servers = append(servers, srv)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, x := range servers {
			_ = x.Shutdown(shutdown)
		}
	}()
	log.Printf("zyntra %s serving %q on %s (approvals %s, execute %s, ai %s)", version, m.Name, addr,
		opts.ApprovalMode, mode, engine.Status().Mode)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	s.Wait()
	return nil
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func keepCmd(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("keep: want deploy, pubkey or credential")
	}
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("keep "+args[0], flag.ContinueOnError)
	seed := fs.String("seed", filepath.Join(home, ".config/zyvor/keep-signer.seed"), "Keep signer seed (never leaves this machine)")
	url := fs.String("url", env("ZYNTRA_KEEP_URL", "http://127.0.0.1:9096"), "Keep agent runtime URL")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	pack, err := keeppack.Executor()
	if err != nil {
		return err
	}
	switch args[0] {
	case "credential":
		_, err := out.Write(append(pack.Credential, '\n'))
		return err
	case "pubkey":
		priv, err := keep.LoadSigner(*seed)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, keep.PublicHex(priv))
	case "deploy":
		priv, err := keep.LoadSigner(*seed)
		if err != nil {
			return err
		}
		token := os.Getenv("ZYNTRA_KEEP_TOKEN")
		if token == "" {
			return fmt.Errorf("set ZYNTRA_KEEP_TOKEN to the Keep operator token")
		}
		c := keep.New(*url, token)
		a, err := c.Deploy(ctx, priv, pack.Name, pack.Bundle, pack.Manifest)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "deployed %s version %s (signer %s…)\n", a.Name, a.Version, keep.PublicHex(priv)[:8])
	default:
		return fmt.Errorf("keep: unknown subcommand %q", args[0])
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
