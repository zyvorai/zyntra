// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zyvorai/zyntra/internal/graph"
)

func act(tpl string, p map[string]string) graph.Action {
	return graph.Action{ID: "a1", Execute: &graph.Execute{Template: tpl, Params: p}}
}

func TestRender(t *testing.T) {
	r, err := Render(act("gravia.priority", map[string]string{"name": "inference-high", "value": "1000", "maxQueueTimeMinutes": "10"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"kind: GryviaPriority", "name: inference-high", "value: 1000", "preemptionPolicy: PreemptLowerPriority", "maxQueueTimeMinutes: 10", "managed-by: zyntra"} {
		if !strings.Contains(r.Stdin, want) {
			t.Errorf("priority yaml missing %q:\n%s", want, r.Stdin)
		}
	}

	r, err = Render(act("gravia.gpu-sharing", map[string]string{"name": "mig-a100", "profile": "all-1g.10gb", "count": "7", "nodeSelector.gpu-type": "a100"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"kind: GryviaGPUSharingPolicy", "strategy: mig", `"gpu-type": "a100"`, `name: "all-1g.10gb"`, "count: 7"} {
		if !strings.Contains(r.Stdin, want) {
			t.Errorf("sharing yaml missing %q:\n%s", want, r.Stdin)
		}
	}

	r, err = Render(act("gravia.job-suspend", map[string]string{"job": "train-1", "namespace": "ml"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.Args, " ") != `patch gryviaaijobs.gryvia.io train-1 -n ml --type merge -p {"spec":{"suspend":true}}` {
		t.Fatalf("suspend args %v", r.Args)
	}

	bad := []graph.Action{
		{ID: "none"},
		act("nope", nil),
		act("gravia.priority", map[string]string{"name": "x"}),
		act("gravia.priority", map[string]string{"name": "Bad_Name", "value": "1"}),
		act("gravia.priority", map[string]string{"name": "ok", "value": "high"}),
		act("gravia.priority", map[string]string{"name": "ok", "value": "1", "preemptionPolicy": "Always"}),
		act("gravia.gpu-sharing", map[string]string{"name": "ok", "profile": "p", "count": "0"}),
		act("gravia.job-suspend", map[string]string{"job": "j; rm -rf /", "namespace": "ml"}),
	}
	for _, a := range bad {
		if _, err := Render(a); err == nil {
			t.Errorf("expected error for %+v", a.Execute)
		}
	}
}

func TestExecuteModes(t *testing.T) {
	var gotArgs []string
	var gotStdin string
	run := func(_ context.Context, args []string, stdin string) (string, error) {
		gotArgs, gotStdin = args, stdin
		return "gryviapriority.gryvia.io/x created (server dry run)", nil
	}
	r, _ := Render(act("gravia.priority", map[string]string{"name": "x", "value": "5"}))

	res := (&Executor{Run: run}).Execute(context.Background(), r)
	if !res.OK || res.Mode != ModeDryRun || gotArgs[len(gotArgs)-1] != "--dry-run=server" || !strings.Contains(gotStdin, "GryviaPriority") {
		t.Fatalf("dry-run: %+v args=%v", res, gotArgs)
	}
	res = (&Executor{Mode: ModeApply, Run: run}).Execute(context.Background(), r)
	if !res.OK || strings.Contains(strings.Join(gotArgs, " "), "dry-run") {
		t.Fatalf("apply: %+v args=%v", res, gotArgs)
	}
	fail := func(context.Context, []string, string) (string, error) { return "forbidden", errors.New("exit 1") }
	res = (&Executor{Run: fail}).Execute(context.Background(), r)
	if res.OK || res.Error == "" || res.Output != "forbidden" {
		t.Fatalf("failure: %+v", res)
	}
	if _, err := ParseMode("yolo"); err == nil {
		t.Fatal("ParseMode accepted junk")
	}
}
