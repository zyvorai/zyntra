// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package executor renders approved actions into Gravia custom resources and
// applies them with kubectl. Server-side dry-run is the default.
package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// Rendered is what an action will do: Display is shown to the approver, Args
// and Stdin are passed to kubectl (without the dry-run flag).
type Rendered struct {
	Template string   `json:"template"`
	Display  string   `json:"display"`
	Args     []string `json:"args"`
	Stdin    string   `json:"-"`
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
var Templates = []string{"gravia.priority", "gravia.gpu-sharing", "gravia.job-suspend"}

// Render turns an action into a kubectl invocation.
func Render(a graph.Action) (Rendered, error) {
	if a.Execute == nil {
		return Rendered{}, fmt.Errorf("action %q has no execute block", a.ID)
	}
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
}

type Result struct {
	Mode   Mode     `json:"mode"`
	Args   []string `json:"args"`
	Output string   `json:"output"`
	OK     bool     `json:"ok"`
	Error  string   `json:"error,omitempty"`
}

// Execute runs the rendered command; in dry-run mode with --dry-run=server.
func (e *Executor) Execute(ctx context.Context, r Rendered) Result {
	args := append([]string(nil), r.Args...)
	if e.Mode != ModeApply {
		args = append(args, "--dry-run=server")
	}
	res := Result{Mode: e.Mode, Args: args}
	if res.Mode == "" {
		res.Mode = ModeDryRun
	}
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
