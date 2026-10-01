# Zyntra product plan

## One line

Decision intelligence for infrastructure ops: a live KPI graph, explainable what-if simulation, and approval-gated actions across the Zyvor portfolio.

## Positioning

[ExperienceFlow](https://experienceflow.ai) (EDNS, the "Enterprise Digital Nervous System") sells "sense, simulate, act" to business functions such as finance, supply chain and support, using graph neural networks and multi-agent reinforcement learning. Their buyer is the CFO or COO.

Zyntra applies the same loop to **infrastructure**, where:

- the KPIs are already measured (Prometheus, DCGM, cloud billing), so there's no data-integration project before the first value;
- the actions are concrete and automatable (scale, repartition, preempt, migrate), and Zyvor products already perform them;
- buyers (platform, SRE, FinOps and ML-platform leads) are the ones already using the rest of Zyvor.

| | ExperienceFlow EDNS | Zyntra |
|---|---|---|
| Domain | Business functions | Infrastructure ops (GPU, Kubernetes, VMs, edge) |
| Graph | Roles and business KPIs | Infra KPIs (SLOs, capacity, cost) with owners |
| Simulation | GNN + multi-agent RL | Deterministic propagation (v0.1); learned weights later |
| Explainability | Claimed | Every number traced to an input, edge or effect |
| Action | Recommendations to people | Approval-gated, executed through Zyvor adapters |
| Deployment | SaaS via partners | Single binary, on-prem / air-gap friendly |

## Feature map

| Area | v0.1 | v0.2 (this repo) | Next |
|------|------|------------------|------|
| KPI graph | YAML model, DAG validation, owners, targets | Lab model over Netra, Gravia, Fabric and Keep (20 KPIs) | Templates (GPU cluster, KubeVirt fleet, edge sites); graph editor in the UI |
| Sense | Prometheus queries; `kubectl get nodes` | Netra `/metrics` (eBPF), Netra/Gravia/Fabric/Keep JSON APIs, per-source health | Cloud billing, DCGM direct, Fleet/Relay APIs |
| Gaps | Relative shortfall, ranked | Same, plus KPI history and sparklines | Per-owner views and SLO burn rates |
| AI | None | Grounded digest, ask and explain; z-score anomalies; time-to-breach forecasts; optional OpenAI-compatible rewrite (Fabric AI gateway) | Anomaly-to-action suggestions, explanations of model misses |
| Simulate | Linear propagation with a trace | Unchanged | Non-linear edges, multi-action combos, uncertainty bands |
| Act | None | Approval inbox with audit; Gravia CRD execution (priority, MIG sharing, job suspend), dry-run by default; predicted vs actual | Fleet/Relay actions, change windows, per-action auto-approve policy |
| Keep | None | Signed executor agent; executions brokered through Keep with receipts and a hash-chained audit; rejections mirrored | Keep approvals as the single inbox; OIDC identity via Haven |
| UI | SSE dashboard | React console (Netra design, Fabric sign-in), 10 pages, light/dark | Graph view, outcome history |
| Deploy | Docker image | `deploy-remote.sh` and `smoke-remote.sh` (systemd, configures Keep, signs the agent on the workstation) | Helm chart, Fabric app catalog entry |

## Roadmap

- **v0.1 Simulate (done):** model, gaps, what-if, plan, CLI, REST and SSE API, dashboard, read-only Prometheus and Kubernetes adapters.
- **v0.2 Console, signals and approval gate (done):** web console, session auth, live Netra/Gravia/Fabric/Keep sources, grounded AI, approval inbox with audit, Gravia CRD executors in dry-run, Fabric Keep execution path, remote deploy and smoke test.
- **v0.3 Closed loop:** OIDC identity via Haven, Fleet/Relay executors with verified outcomes, Kairo/KubeFlight deploy risk as an input, and predicted-vs-actual scoring per action.
- **v0.4 Learned weights:** estimate edge weights from metric history with confidence intervals, flag edges whose predictions keep missing, and keep the model explainable (weights stay visible and editable).

## Known limits (v0.2)

- On the lab host, Keep sandboxes cannot reach the egress broker: the FluxVM guest gateway is the netns bridge, not the host. Approved actions therefore fall back to local execution, which the audit labels `zyntra (keep unavailable)`. Starting sessions, agent deploys, record-mode mirrors and Keep audit all work.
- A model rewrite needs a Fabric InferenceEndpoint and an `fvai_` key. Without them, answers stay heuristic.

## Non-goals

- Making decisions with an LLM. An LLM may rewrite the grounded narrative, but never picks the action or computes the numbers.
- Running actions without approval in any release before an explicit, per-action auto-approve policy exists.
- Business-function planning (finance, supply chain). That is ExperienceFlow's market, not ours.

## Open questions

- Pricing unit: per cluster, per KPI or per managed node?
- Open-core split: is the simulator open source with adapters and the approval gate commercial (like Argus and Argus Enterprise)?
- Domain: confirm `zyntra.dev` availability and register it.
