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

| Area | v0.1 (this repo) | Next |
|------|------------------|------|
| KPI graph | YAML model, DAG validation, owners, targets | Templates (GPU cluster, KubeVirt fleet, edge sites); graph editor in the UI |
| Sense | Prometheus instant queries; `kubectl get nodes` inventory | Cloud billing, DCGM direct, Zynera/Fleet/Relay APIs |
| Gaps | Relative shortfall, ranked, per owner | Trend and forecast ("misses target in 6 days") |
| Simulate | Linear relative propagation with a full trace | Non-linear edges (saturation curves), multi-action combos, uncertainty bands |
| Plan | Ranking by gap reduction minus risk | Constraint-aware search (budget caps, change windows), per-owner views |
| Act | None: everything is `pending-approval` | Approval gate with audit, then execution via adapters with verified outcomes |
| Learn | None | Fit edge weights from history; compare predicted vs actual after each action |
| UI | Decision pulse dashboard (SSE) | Graph view, approval inbox, outcome history |

## Roadmap

- **v0.1 Simulate (done):** model, gaps, what-if, plan, CLI, REST and SSE API, dashboard, read-only Prometheus and Kubernetes adapters.
- **v0.2 Approval gate:** persistent recommendations, approve/reject with identity (OIDC via Haven), audit log, and a dry-run mode for executors.
- **v0.3 Zyvor adapters:** execute approved actions through Zynera (placement, MIG, spot), Fleet/Relay (site actions with verified outcomes) and Kairo/KubeFlight (deploy-risk input), then re-measure and record predicted vs actual.
- **v0.4 Learned weights:** estimate edge weights from metric history with confidence intervals, flag edges whose predictions keep missing, and keep the model explainable (weights stay visible and editable).

## Non-goals

- Making decisions with an LLM. An LLM may later write the narrative summary, but never picks the action or computes the numbers.
- Running actions without approval in any release before an explicit, per-action auto-approve policy exists.
- Business-function planning (finance, supply chain). That is ExperienceFlow's market, not ours.

## Open questions

- Pricing unit: per cluster, per KPI or per managed node?
- Open-core split: is the simulator open source with adapters and the approval gate commercial (like Argus and Argus Enterprise)?
- Domain: confirm `zyntra.dev` availability and register it.
