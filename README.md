# Zyntra

[![CI](https://github.com/zyvorai/zyntra/actions/workflows/ci.yml/badge.svg)](https://github.com/zyvorai/zyntra/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

**Decision intelligence for infrastructure ops. Sense, simulate, act.**

Zyntra keeps a live graph of the KPIs your infrastructure is judged on (SLOs, latency, queue wait, capacity headroom, spend), shows which ones are missing target and by how much, simulates candidate actions through the dependency graph, and ranks them. Every recommendation is explained step by step and waits for human approval.

> **Maturity (honest):** v0.2 adds a web console, live signals from Netra (eBPF), Gravia (GPU) and Fabric (hosts and Keep), grounded AI insights, and an approval inbox that executes Gravia CRDs **only after a human approves**, in `kubectl --dry-run=server` mode by default. The simulator is still a deterministic linear model over relative changes, and you supply the edge weights. It does not learn them yet. All adapters are read-only. The AI layer explains and forecasts; it never picks or runs an action. Learned weights are on the [roadmap](docs/PRODUCT_PLAN.md).

## Why

Infra teams answer "what should we do next?" with a dozen dashboards and a meeting. Adding nodes shortens the queue but blows the budget. MIG frees GPUs but adds latency. Zyntra makes those trade-offs explicit:

- **Gaps:** which KPIs miss target, who owns them, and how far off they are.
- **What-if:** the predicted change to every KPI if you take an action, with the full propagation path.
- **Plan:** all actions ranked by total gap reduction minus a risk penalty, flagging any gap an action would *open*.
- **No hallucinations:** no LLM makes decisions. Every number traces back to an input, an edge or an action effect.

## Quick start

```bash
make build                # console (web/, needs Node 22) + Go binary
./bin/zyntra gaps
./bin/zyntra simulate -action preempt_batch_to_spot
./bin/zyntra plan
ZYNTRA_API_KEY=dev ./bin/zyntra serve   # console + API on :8080
```

Try the lab model against fake Netra/Gravia/Fabric/Keep sources:

```bash
make run-lab              # http://127.0.0.1:8080, operator: any name, access key: dev
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

### Live sources (Netra, Gravia, Fabric, Keep)

`kind: metrics` scrapes Prometheus text (Netra's `/metrics`). `kind: json` reads a field from a JSON endpoint; `kind: netra`, `gravia`, `fabric` and `keep` are shorthands that also pick the endpoint. Zyntra ships no eBPF code of its own; it consumes the Netra agent.

```yaml
- id: tcp_retransmits_per_s
  source: {kind: metrics, endpoint: netra, metric: netra_tcp_retransmissions, agg: sum, rate: true}
- id: ebpf_health_score
  source: {kind: netra, path: /api/v1/ebpf/health, field: summary.healthScore}
- id: root_disk_percent
  source: {kind: fabric, path: /api/v1/system/info, field: "filesystems.#(mountpoint=/).usage_percent"}
```

Fields support `a.b.0`, `list.#` (count), `list.#(k=v)` (count matches), `list.#(k=v).f` (field of the first match) and `list.*.f`, plus `scale`, `agg: sum|avg|max|min` and `rate`. See [examples/lab-kpis.yaml](examples/lab-kpis.yaml) for a full lab model (20 KPIs).

## Console

`zyntra serve` embeds a React console styled like Netra, with Fabric's two-step sign-in (operator name, then access key). Its pages are Overview, Gaps, Plan, Simulate, Approvals, Audit, Signals, Insights, Ask and Model, with light and dark themes. Sign-in sets an HMAC session cookie (12 h, or 7 days with "remember me"). Scripts can use `Authorization: Bearer $ZYNTRA_API_KEY` instead.

## AI (grounded, read-only)

- **Anomalies:** z-score of each KPI against its own history.
- **Forecasts:** least-squares trend with time to breach. A forecast needs at least 6 samples spanning 10 minutes.
- **Digest, Ask and Explain:** built from gaps, plan, anomalies and source health, with the grounding facts listed. Set `ZYNTRA_AI_BASE_URL` (an OpenAI-compatible endpoint such as the Fabric AI gateway) and the model rewrites the grounded answer. Without it, the heuristic answer is returned.

## Approvals and execution

1. **Propose** an action from the plan. Zyntra stores the prediction and baseline and renders the change it would make (a `GryviaPriority`, `GryviaGPUSharingPolicy` or Job suspend).
2. **Approve** or reject it in the inbox. The decision is recorded with the operator's name and an optional reason.
3. **Execute:** `kubectl apply --dry-run=server` by default (`ZYNTRA_EXECUTE=apply` to apply for real). Once KPIs refresh, the proposal shows predicted vs actual values.

With `ZYNTRA_APPROVAL_MODE=keep`, approved proposals run through **Fabric Keep**:

1. Keep starts a signed `zyntra-executor` agent in a FluxVM sandbox.
2. The agent calls Zyntra's loopback TLS exec endpoint using the brokered `zyntra-exec` credential. Keep holds the egress approval, and Zyntra decides it, so every execution has a Keep receipt and a hash-chained audit entry.
3. Rejected proposals are mirrored into Keep's audit as denied approvals.

If Keep can't start the session before an approval exists, the already-approved proposal runs locally, and the audit records the executor as `zyntra (keep unavailable)`.

```bash
zyntra keep pubkey                       # signer public key (add it to ZYVOR_AGENT_POLICY_TRUSTED_SIGNERS)
ZYNTRA_KEEP_TOKEN=... zyntra keep deploy -url http://127.0.0.1:9096   # sign + deploy; the seed never leaves this machine
zyntra keep credential                   # zyntra-exec descriptor for ZYVOR_AGENT_CREDENTIALS_FILE
```

## Configuration

| Variable | Purpose |
|----------|---------|
| `ZYNTRA_API_KEY` | Console access key and Bearer token. If unset, the console runs open (dev) |
| `ZYNTRA_LISTEN`, `ZYNTRA_STATE_DIR` | Listen address; directory for approvals, history and exec TLS |
| `ZYNTRA_NETRA_URL` / `_TOKEN` | Netra API (eBPF metrics and health) |
| `ZYNTRA_GRAVIA_URL` / `_TOKEN` | Gravia API (GPU cluster, quota, costs) |
| `ZYNTRA_FABRIC_URL` / `_USER` / `_PASSWORD` or `_TOKEN` | Fabric host metrics (logs in for a token) |
| `ZYNTRA_ENDPOINT_INSECURE=1` | Accept self-signed certificates on the endpoints above (lab) |
| `ZYNTRA_EXECUTE` | `dry-run` (default) or `apply` |
| `ZYNTRA_KUBECONFIG` | kubeconfig for execution and the `-kubectl` adapter |
| `ZYNTRA_APPROVAL_MODE` | `local` (default) or `keep` |
| `ZYNTRA_KEEP_URL` / `_TOKEN` | Keep agent runtime (status, sessions, approvals, audit) |
| `ZYNTRA_EXEC_TOKEN`, `ZYNTRA_EXEC_TLS_ADDR` | Token Keep injects, and the loopback TLS listener it calls |
| `ZYNTRA_AI_BASE_URL` / `_API_KEY` / `_MODEL` | Optional OpenAI-compatible model for answer rewriting |

## CLI

| Command | What it does |
|---------|--------------|
| `zyntra graph` | KPIs, targets, owners, sources, dependencies and actions |
| `zyntra gaps` | KPIs missing target, worst first |
| `zyntra simulate -action ID` | Predicted KPI changes and the propagation trace |
| `zyntra plan` | All actions ranked, each `pending-approval` |
| `zyntra serve` | Console, REST API and SSE pulse |
| `zyntra keep deploy\|pubkey\|credential` | Fabric Keep executor agent |
| `zyntra fake-sources` | Fake Netra/Gravia/Fabric/Keep endpoints for development |
| `zyntra exec-token` | Random token for `ZYNTRA_EXEC_TOKEN` |

Common flags: `-f FILE`, `-o text|json`, `-prometheus URL`, `-kubectl`, `-kubeconfig FILE`. `serve` also takes `-addr` and `-interval`.

## API

All routes except `/healthz`, `/api/v1/meta` and `POST /api/v1/session` need the session cookie or a Bearer key.

| Method | Path | Returns |
|--------|------|---------|
| `GET` | `/healthz` | `{"status":"ok"}` |
| `GET` | `/api/v1/meta` | Version, host, model, source health and modes (for the sign-in page) |
| `POST`/`DELETE` | `/api/v1/session` | Sign in (`{operator, token, remember}`) and out; `GET /api/v1/whoami` |
| `GET` | `/api/v1/graph`, `/gaps`, `/plan`, `/sources` | Model, gaps, ranked actions, source health |
| `POST` | `/api/v1/simulate` | `{"action":"id"}` or `{"custom":{...}}` |
| `GET` | `/api/v1/kpis/{id}/history` | Recorded values |
| `GET`/`POST` | `/api/v1/ai/status`, `/digest`, `/insights`, `/ask`, `/explain` | Grounded AI |
| `GET`/`POST` | `/api/v1/proposals`, `/proposals/{id}`, `/approve`, `/reject` | Approval inbox |
| `GET` | `/api/v1/audit` | Local audit trail |
| `GET` | `/api/v1/keep/status`, `/approvals`, `/receipts`, `/audit` | Fabric Keep views |
| `GET` | `/api/v1/events` | Server-sent `pulse` events every interval |

## Deploy

```bash
./scripts/deploy-remote.sh 212.8.248.187 sus     # build, install, configure Keep, deploy agent, smoke
./scripts/smoke-remote.sh                         # re-run the smoke test against .deploy-last
./scripts/deploy-remote.sh 212.8.248.187 sus --uninstall
```

The deploy script cross-compiles locally and installs `zyntra.service`. It writes `/etc/zyntra/zyntra.env` (root:zyntra, 0640) from credentials already on the host: the Netra k8s secret, the Gravia API key, Fabric's admin password and the Keep token. They are never printed. It also adds the `zyntra-exec` credential and exec CA to Keep (after backing up Keep's env file) and signs the executor agent on your workstation. Options: `--port`, `--exec-port`, `--no-keep`, `--skip-web`, `--dry-run`, `--skip-smoke`.

## Where it fits in Zyvor

- **Netra:** eBPF network signals (retransmits, drops, latency, datapath health)
- **Gravia:** GPU utilisation, queue and cost; executes priority, MIG-sharing and job-suspend CRDs
- **Fabric:** host metrics, the AI gateway, and Keep for sandboxed, audited execution
- **Kairo / KubeFlight:** deploy blast radius, to be fed in as a risk input

## Develop

```bash
make check      # gofmt, vet, unit tests, build
make test-e2e   # CLI + API + console smoke test against fake sources
cd web && ZYNTRA_DEV_API=http://127.0.0.1:8080 npm run dev   # console with hot reload
docker build -t zyntra .
```

See [SECURITY.md](SECURITY.md) and [CONTRIBUTING.md](CONTRIBUTING.md). Licensed under Apache-2.0.
