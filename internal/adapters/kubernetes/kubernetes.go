// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package kubernetes derives capacity KPIs from node inventory via kubectl.
package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Runner returns the JSON output of `kubectl get nodes -o json`.
type Runner func(ctx context.Context) ([]byte, error)

func Kubectl(kubeconfig string) Runner {
	return func(ctx context.Context) ([]byte, error) {
		args := []string{"get", "nodes", "-o", "json"}
		if kubeconfig != "" {
			args = append([]string{"--kubeconfig", kubeconfig}, args...)
		}
		return exec.CommandContext(ctx, "kubectl", args...).Output()
	}
}

type nodeList struct {
	Items []struct {
		Status struct {
			Allocatable map[string]string `json:"allocatable"`
			Conditions  []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

// Inventory holds the metrics a KPI can bind to with source.kind=kubernetes.
type Inventory map[string]float64

const (
	MetricNodesTotal     = "nodes_total"
	MetricNodesReady     = "nodes_ready"
	MetricGPUAllocatable = "gpu_allocatable"
	MetricCPUAllocatable = "cpu_allocatable"
)

func Collect(ctx context.Context, run Runner) (Inventory, error) {
	out, err := run(ctx)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: %w", err)
	}
	return Parse(out)
}

func Parse(b []byte) (Inventory, error) {
	var nl nodeList
	if err := json.Unmarshal(b, &nl); err != nil {
		return nil, fmt.Errorf("kubernetes: decode nodes: %w", err)
	}
	inv := Inventory{MetricNodesTotal: float64(len(nl.Items))}
	for _, n := range nl.Items {
		for _, c := range n.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				inv[MetricNodesReady]++
			}
		}
		if g, ok := n.Status.Allocatable["nvidia.com/gpu"]; ok {
			v, err := strconv.ParseFloat(g, 64)
			if err != nil {
				return nil, fmt.Errorf("kubernetes: gpu allocatable %q: %w", g, err)
			}
			inv[MetricGPUAllocatable] += v
		}
		if c, ok := n.Status.Allocatable["cpu"]; ok {
			v, err := parseCPU(c)
			if err != nil {
				return nil, fmt.Errorf("kubernetes: cpu allocatable %q: %w", c, err)
			}
			inv[MetricCPUAllocatable] += v
		}
	}
	return inv, nil
}

// parseCPU handles the cores ("8") and millicores ("7500m") forms that the
// API server reports for allocatable CPU.
func parseCPU(s string) (float64, error) {
	if m, ok := strings.CutSuffix(s, "m"); ok {
		v, err := strconv.ParseFloat(m, 64)
		return v / 1000, err
	}
	return strconv.ParseFloat(s, 64)
}
