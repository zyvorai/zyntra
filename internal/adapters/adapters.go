// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package adapters refreshes KPI values from live sources. All adapters are
// read-only.
package adapters

import (
	"context"
	"errors"
	"fmt"

	"github.com/zyvorai/zyntra/internal/adapters/kubernetes"
	"github.com/zyvorai/zyntra/internal/adapters/prometheus"
	"github.com/zyvorai/zyntra/internal/graph"
)

type Config struct {
	Prometheus *prometheus.Client
	Kubernetes kubernetes.Runner
}

// Refresh updates every KPI that has a source the config can serve. KPIs with
// unavailable sources keep their model value; the returned error lists them.
func Refresh(ctx context.Context, m *graph.Model, cfg Config) error {
	var errs []error
	var inv kubernetes.Inventory
	for i := range m.KPIs {
		k := &m.KPIs[i]
		if k.Source == nil || k.Source.Kind == "" {
			continue
		}
		switch k.Source.Kind {
		case "prometheus":
			if cfg.Prometheus == nil {
				continue
			}
			v, err := cfg.Prometheus.Query(ctx, k.Source.Query)
			if err != nil {
				errs = append(errs, fmt.Errorf("kpi %s: %w", k.ID, err))
				continue
			}
			k.Value = v
		case "kubernetes":
			if cfg.Kubernetes == nil {
				continue
			}
			if inv == nil {
				var err error
				if inv, err = kubernetes.Collect(ctx, cfg.Kubernetes); err != nil {
					errs = append(errs, err)
					cfg.Kubernetes = nil
					continue
				}
			}
			v, ok := inv[k.Source.Metric]
			if !ok {
				errs = append(errs, fmt.Errorf("kpi %s: kubernetes metric %q not available", k.ID, k.Source.Metric))
				continue
			}
			k.Value = v
		default:
			errs = append(errs, fmt.Errorf("kpi %s: unknown source kind %q", k.ID, k.Source.Kind))
		}
	}
	return errors.Join(errs...)
}
