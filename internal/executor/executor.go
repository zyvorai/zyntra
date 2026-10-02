// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package executor renders approved actions and runs them: Gravia custom
// resources through kubectl, webhooks, files written to an output
// directory, or noop for changes people carry out. Dry-run is the default:
// kubectl runs with --dry-run=server, webhooks are printed but not sent and
// files are shown but not written.
package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/zyvorai/zyntra/internal/graph"
)

type Mode string

const (
	ModeDryRun Mode = "dry-run"
	ModeApply  Mode = "apply"
)

func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case "", ModeDryRun:
		return ModeDryRun, nil
	case ModeApply:
		return ModeApply, nil
	}
	return "", fmt.Errorf("execute mode must be dry-run or apply, got %q", s)
}

// Rendered is what an action will do: Display is shown to the approver.
// For kubectl, Args and Stdin are passed to kubectl (without the dry-run
// flag). For webhooks, URL and Headers keep their ${ZYNTRA_*} references
// until execution so secrets never reach the proposal.
type Rendered struct {
	Kind     string            `json:"kind"`
	Template string            `json:"template"`
	Display  string            `json:"display"`
	Args     []string          `json:"args"`
	Stdin    string            `json:"-"`
	Method   string            `json:"method,omitempty"`
	URL      string            `json:"url,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Body     string            `json:"body,omitempty"`
	Path     string            `json:"path,omitempty"`
	Content  string            `json:"-"`
	// Key is an idempotency key (the proposal id) sent with webhooks.
	Key string `json:"-"`
}

var dns1123 = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func need(p map[string]string, keys ...string) error {
	var missing []string
	for _, k := range keys {
		if strings.TrimSpace(p[k]) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing params: %s", strings.Join(missing, ", "))
	}
	return nil
}

func name(p map[string]string, key string) (string, error) {
	n := p[key]
	if len(n) > 63 || !dns1123.MatchString(n) {
		return "", fmt.Errorf("param %s=%q is not a valid Kubernetes name", key, n)
	}
	return n, nil
}

func yamlStr(s string) string { b, _ := json.Marshal(s); return string(b) }

const managedBy = `    app.kubernetes.io/managed-by: zyntra
`

// Templates lists supported template names.
var Templates = []string{"gravia.priority", "gravia.gpu-sharing", "gravia.job-suspend", "gravia.priority-delete", "gravia.gpu-sharing-delete"}

// Rollback returns the execute block that undoes a: the action's own
// rollback block when it has one, otherwise the inverse of its template.
func Rollback(a graph.Action) (*graph.Execute, bool) {
	if a.Rollback != nil {
		r := *a.Rollback
		return &r, true
	}
	if a.Execute == nil {
		return nil, false
	}
	p := map[string]string{}
	maps.Copy(p, a.Execute.Params)
	switch a.Execute.Template {
	case "gravia.priority":
		return &graph.Execute{Template: "gravia.priority-delete", Params: map[string]string{"name": p["name"]}}, true
	case "gravia.gpu-sharing":
		return &graph.Execute{Template: "gravia.gpu-sharing-delete", Params: map[string]string{"name": p["name"]}}, true
	case "gravia.job-suspend":
		p["suspend"] = map[bool]string{true: "true", false: "false"}[p["suspend"] == "false"]
		return &graph.Execute{Template: "gravia.job-suspend", Params: p}, true
	}
	return nil, false
}

// RollbackAction returns a synthetic action that undoes a.
func RollbackAction(a graph.Action) (graph.Action, bool) {
	ex, ok := Rollback(a)
	if !ok {
		return graph.Action{}, false
	}
	return graph.Action{ID: a.ID + ".rollback", Name: "Roll back: " + a.Name, Risk: a.Risk, Adapter: a.Adapter, Execute: ex}, true
}

// RenderRollback renders the command that undoes a.
func RenderRollback(a graph.Action) (Rendered, error) {
	ra, ok := RollbackAction(a)
	if !ok {
		return Rendered{}, fmt.Errorf("action %q has no rollback", a.ID)
	}
	return Render(ra)
}

// Render renders an action without model context; webhook body
// references and file templates that need KPIs are left unresolved.
func Render(a graph.Action) (Rendered, error) { return RenderIn(nil, a) }

// RenderIn renders an action against the current model.
func RenderIn(m *graph.Model, a graph.Action) (Rendered, error) {
	var (
		r   Rendered
		err error
	)
	switch k := a.Kind(); k {
	case graph.KindKubectl:
		r, err = renderKubectl(a)
	case graph.KindWebhook:
		r, err = renderWebhook(m, a)
	case graph.KindFile:
		r, err = renderFile(m, a)
	case graph.KindNoop:
		r = Rendered{Template: graph.KindNoop, Display: noopDisplay(a)}
	default:
		return Rendered{}, fmt.Errorf("action %q has nothing to run", a.ID)
	}
	r.Kind = a.Kind()
	return r, err
}

// Template is the name recorded on a proposal for an action: the kubectl
// template, or the kind for webhook, file and noop actions.
func Template(a graph.Action) string {
	if a.Execute != nil {
		return a.Execute.Template
	}
	return a.Kind()
}

func renderKubectl(a graph.Action) (Rendered, error) {
	p := a.Execute.Params
	if p == nil {
		p = map[string]string{}
	}
	r := Rendered{Template: a.Execute.Template}
	switch a.Execute.Template {
	case "gravia.priority":
		if err := need(p, "name", "value"); err != nil {
			return r, err
		}
		n, err := name(p, "name")
		if err != nil {
			return r, err
		}
		v, err := strconv.Atoi(p["value"])
		if err != nil {
			return r, fmt.Errorf("param value=%q must be an integer", p["value"])
		}
		policy := p["preemptionPolicy"]
		if policy == "" {
			policy = "PreemptLowerPriority"
		}
		if policy != "PreemptLowerPriority" && policy != "Never" {
			return r, fmt.Errorf("preemptionPolicy must be PreemptLowerPriority or Never")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "apiVersion: gryvia.io/v1alpha1\nkind: GryviaPriority\nmetadata:\n  name: %s\n  labels:\n%s  annotations:\n    zyntra.dev/action: %s\nspec:\n  value: %d\n  preemptionPolicy: %s\n",
			n, managedBy, yamlStr(a.ID), v, policy)
		if q := p["maxQueueTimeMinutes"]; q != "" {
			m, err := strconv.Atoi(q)
			if err != nil {
				return r, fmt.Errorf("param maxQueueTimeMinutes must be an integer")
			}
			fmt.Fprintf(&b, "  sla:\n    maxQueueTimeMinutes: %d\n", m)
		}
		r.Display, r.Stdin = b.String(), b.String()
		r.Args = []string{"apply", "-f", "-"}
	case "gravia.gpu-sharing":
		if err := need(p, "name", "profile"); err != nil {
			return r, err
		}
		n, err := name(p, "name")
		if err != nil {
			return r, err
		}
		count := 1
		if c := p["count"]; c != "" {
			if count, err = strconv.Atoi(c); err != nil || count < 1 {
				return r, fmt.Errorf("param count must be a positive integer")
			}
		}
		var b strings.Builder
		fmt.Fprintf(&b, "apiVersion: gryvia.io/v1alpha1\nkind: GryviaGPUSharingPolicy\nmetadata:\n  name: %s\n  labels:\n%s  annotations:\n    zyntra.dev/action: %s\nspec:\n  strategy: mig\n",
			n, managedBy, yamlStr(a.ID))
		sel := selector(p)
		if len(sel) > 0 {
			b.WriteString("  nodeSelector:\n")
			for _, kv := range sel {
				fmt.Fprintf(&b, "    %s: %s\n", yamlStr(kv[0]), yamlStr(kv[1]))
			}
		}
		fmt.Fprintf(&b, "  mig:\n    enabled: true\n    profiles:\n      - name: %s\n        count: %d\n", yamlStr(p["profile"]), count)
		r.Display, r.Stdin = b.String(), b.String()
		r.Args = []string{"apply", "-f", "-"}
	case "gravia.job-suspend":
		if err := need(p, "job", "namespace"); err != nil {
			return r, err
		}
		job, err := name(p, "job")
		if err != nil {
			return r, err
		}
		ns, err := name(p, "namespace")
		if err != nil {
			return r, err
		}
		suspend := p["suspend"] != "false"
		patch := fmt.Sprintf(`{"spec":{"suspend":%t}}`, suspend)
		r.Args = []string{"patch", "gryviaaijobs.gryvia.io", job, "-n", ns, "--type", "merge", "-p", patch}
		r.Display = fmt.Sprintf("# kubectl patch gryviaaijob %s -n %s --type merge\n%s\n", job, ns, patch)
	case "gravia.priority-delete", "gravia.gpu-sharing-delete":
		if err := need(p, "name"); err != nil {
			return r, err
		}
		n, err := name(p, "name")
		if err != nil {
			return r, err
		}
		kind := map[string]string{"gravia.priority-delete": "gryviapriorities.gryvia.io", "gravia.gpu-sharing-delete": "gryviagpusharingpolicies.gryvia.io"}[a.Execute.Template]
		r.Args = []string{"delete", kind, "-l", "app.kubernetes.io/managed-by=zyntra", "--field-selector", "metadata.name=" + n, "--ignore-not-found"}
		r.Display = fmt.Sprintf("# kubectl delete %s %s (only if managed by zyntra)\n", kind, n)
	default:
		return r, fmt.Errorf("unknown execute template %q (supported: %s)", a.Execute.Template, strings.Join(Templates, ", "))
	}
	return r, nil
}

// selector reads params named "nodeSelector.<label>".
func selector(p map[string]string) [][2]string {
	var out [][2]string
	for k, v := range p {
		if l, ok := strings.CutPrefix(k, "nodeSelector."); ok && l != "" {
			out = append(out, [2]string{l, v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// Runner executes kubectl with args and stdin.
type Runner func(ctx context.Context, args []string, stdin string) (string, error)

func Kubectl(kubeconfig string) Runner {
	return func(ctx context.Context, args []string, stdin string) (string, error) {
		if kubeconfig != "" {
			args = append([]string{"--kubeconfig", kubeconfig}, args...)
		}
		cmd := exec.CommandContext(ctx, "kubectl", args...)
		cmd.Stdin = strings.NewReader(stdin)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return strings.TrimSpace(out.String()), err
	}
}

type Executor struct {
	Mode Mode
	Run  Runner
	// OutDir is where file actions write; empty refuses file applies.
	OutDir string
	// HTTP sends webhooks; nil uses a client with a 15s timeout.
	HTTP *http.Client
}

type Result struct {
	Mode   Mode     `json:"mode"`
	Kind   string   `json:"kind,omitempty"`
	Args   []string `json:"args"`
	Output string   `json:"output"`
	OK     bool     `json:"ok"`
	Error  string   `json:"error,omitempty"`
	// Status is the webhook response code.
	Status int `json:"status,omitempty"`
	// ResponseHash is the SHA-256 of the webhook response body.
	ResponseHash string `json:"response_hash,omitempty"`
	// Written is the file a file action wrote.
	Written string `json:"written,omitempty"`
}

// Execute runs the rendered action. In dry-run mode kubectl gets
// --dry-run=server and other kinds only describe what they would do.
func (e *Executor) Execute(ctx context.Context, r Rendered) Result {
	switch r.Kind {
	case graph.KindWebhook:
		return e.webhook(ctx, r)
	case graph.KindFile:
		return e.file(r)
	case graph.KindNoop:
		res := Result{Mode: e.mode(), Kind: r.Kind, OK: true, Args: []string{"noop"}}
		res.Output = "approval recorded; no system call (the change is carried out by people)"
		return res
	}
	return e.kubectl(ctx, r)
}

func (e *Executor) mode() Mode {
	if e.Mode == "" {
		return ModeDryRun
	}
	return e.Mode
}

func (e *Executor) kubectl(ctx context.Context, r Rendered) Result {
	args := append([]string(nil), r.Args...)
	if e.Mode != ModeApply {
		args = append(args, "--dry-run=server")
	}
	res := Result{Mode: e.mode(), Kind: graph.KindKubectl, Args: args}
	if e.Run == nil {
		res.Error = "no kubectl runner configured"
		return res
	}
	out, err := e.Run(ctx, args, r.Stdin)
	res.Output = out
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.OK = true
	return res
}
