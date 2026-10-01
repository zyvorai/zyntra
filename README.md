# Zyntra

[![CI](https://github.com/zyvorai/zyntra/actions/workflows/ci.yml/badge.svg)](https://github.com/zyvorai/zyntra/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

**Decision intelligence for infrastructure ops. Sense, simulate, act.**

Zyntra keeps a live graph of the KPIs your infrastructure is judged on (SLOs, latency, queue wait, capacity headroom, spend), shows which ones are missing target and by how much, simulates candidate actions through the dependency graph, and ranks them. Every recommendation is explained step by step and waits for human approval.

> **Maturity (honest):** v0.1 simulates and recommends; it executes nothing. The simulator is a deterministic linear model over relative changes, and you supply the edge weights. It does not learn them yet. Prometheus and Kubernetes (`kubectl get nodes`) adapters are read-only. The approval gate, connectors that run actions through Zyvor products, and learned weights are on the [roadmap](docs/PRODUCT_PLAN.md).

## Why

Infra teams answer "what should we do next?" with a dozen dashboards and a meeting. Adding nodes shortens the queue but blows the budget. MIG frees GPUs but adds latency. Zyntra makes those trade-offs explicit:

- **Gaps:** which KPIs miss target, who owns them, and how far off they are.
- **What-if:** the predicted change to every KPI if you take an action, with the full propagation path.
- **Plan:** all actions ranked by total gap reduction minus a risk penalty, flagging any gap an action would *open*.
- **No hallucinations:** no LLM makes decisions. Every number traces back to an input, an edge or an action effect.

## Quick start

```bash
make build
./bin/zyntra gaps
./bin/zyntra simulate -action preempt_batch_to_spot
./bin/zyntra plan
./bin/zyntra serve        # dashboard + API on :8080
```

```text
$ ./bin/zyntra simulate -action preempt_batch_to_spot
What if: Move batch training to spot GPUs (preempt_batch_to_spot)

KPI                    BEFORE  AFTER  CHANGE  TARGET
capacity_headroom      8       14.4   +80.0%  missed
queue_wait_minutes     35      12.6   -64.0%  met (closed)
p99_inference_latency  420     252    -40.0%  met (closed)
slo_availability       99.82   99.92  +0.10%  met (closed)
monthly_cost           180     171    -5.0%   met

Why:
  preempt_batch_to_spot changes capacity_headroom by +80.0% (direct effect)
  preempt_batch_to_spot changes monthly_cost by -5.0% (direct effect)
  capacity_headroom +80.0% -> queue_wait_minutes -64.0% (weight -0.8): free GPUs drain the training queue
  capacity_headroom +80.0% -> p99_inference_latency -40.0% (weight -0.5): inference pods stop competing for saturated GPUs
  p99_inference_latency -40.0% -> slo_availability +0.10% (weight -0.0025): latency spikes trip request timeouts

Gap severity: 2.334 -> 0.280 (-2.054)
Closes: queue_wait_minutes, p99_inference_latency, slo_availability
```

## The model

A model file ([examples/kpis.yaml](examples/kpis.yaml)) has three parts:

```yaml
kpis:
  - {id: capacity_headroom, unit: "%", owner: platform, value: 8, target: 20, direction: higher}
edges:      # +10% in `from` causes weight * 10% in `to`; must be acyclic
  - {from: capacity_headroom, to: queue_wait_minutes, weight: -0.8, why: free GPUs drain the queue}
actions:
  - id: preempt_batch_to_spot
    adapter: zynera
    risk: medium
    effects: [{kpi: capacity_headroom, change: 0.8}]
```

- **Gap severity** is the relative shortfall against target (`0.4` = 40% off). The plan minimises the sum across KPIs.
- **Propagation** runs in topological order. Direct effects and propagated effects add up, and a KPI can't drop below zero.
- **Score** = gap reduction − risk penalty (low 0, medium 0.05, high 0.15). Actions that don't reduce total severity are dropped.

### Live values

Add a `source` to any KPI and pass the adapter flags:

```yaml
- id: p99_inference_latency
  value: 420            # fallback
  target: 300
  direction: lower
  source:
    kind: prometheus
    query: 1000 * histogram_quantile(0.99, sum(rate(http_request_duration_seconds_bucket{job="inference"}[5m])) by (le))
- id: gpu_nodes
  value: 8
  source: {kind: kubernetes, metric: nodes_ready}   # nodes_total | nodes_ready | gpu_allocatable | cpu_allocatable
```

```bash
zyntra gaps -f examples/prometheus-kpis.yaml -prometheus http://prometheus:9090 -kubectl
```

## CLI

| Command | What it does |
|---------|--------------|
| `zyntra graph` | KPIs, targets, owners, sources, dependencies and actions |
| `zyntra gaps` | KPIs missing target, worst first |
| `zyntra simulate -action ID` | Predicted KPI changes and the propagation trace |
| `zyntra plan` | All actions ranked, each `pending-approval` |
| `zyntra serve` | REST API, SSE pulse and dashboard |

Common flags: `-f FILE`, `-o text|json`, `-prometheus URL`, `-kubectl`, `-kubeconfig FILE`. `serve` also takes `-addr` and `-interval`.

## API

| Method | Path | Returns |
|--------|------|---------|
| `GET` | `/healthz` | `{"status":"ok"}` |
| `GET` | `/api/graph` | Current model and last refresh time |
| `GET` | `/api/gaps` | Gaps and total severity |
| `GET` | `/api/plan` | Ranked recommendations |
| `POST` | `/api/simulate` | `{"action":"id"}`, or `{"custom":{...action}}` for an ad-hoc what-if |
| `GET` | `/api/events` | Server-sent `pulse` events: gaps plus the top three actions on every interval |

## Where it fits in Zyvor

Zyntra recommends; other Zyvor products act and measure:

- **Zynera:** GPU placement, MIG and spot moves (the `adapter: zynera` actions)
- **Kairo / KubeFlight:** deploy blast radius, to be fed in as a risk input
- **Fleet / Relay:** site actions with verified outcomes, closing the loop back into the graph

## Develop

```bash
make check      # gofmt, vet, unit tests, build
make test-e2e   # CLI + API smoke test
docker build -t zyntra .
```

See [SECURITY.md](SECURITY.md) and [CONTRIBUTING.md](CONTRIBUTING.md). Licensed under Apache-2.0.
