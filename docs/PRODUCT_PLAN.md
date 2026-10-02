# Zyntra product plan

## One line

The decision layer across Zyvor: identify a problem, compare fixes, approve a change, and prove whether it worked.

## Positioning

Monitoring tells you something is wrong. Orchestration and infrastructure products change things. Zyntra sits between them and owns the decision:

1. **Identify:** which KPIs miss target, how badly, and how much the business cares (criticality).
2. **Compare:** simulate candidate actions and action pairs through the KPI graph, with bounds, delays and uncertainty ranges, and drop anything that breaks a hard constraint.
3. **Approve:** route the change through role-based, policy-driven approval (two-person rule, expiry, maintenance windows, Keep isolation).
4. **Prove:** revalidate before execution, then observe the outcome over a window and mark it verified, regressed or inconclusive, with a compensating action ready.

Every step lands in a **decision record**: "we recommended this, these inputs supported it, this person approved it, and this was the measured result." That record is why Zyntra sits above monitoring, orchestration and infrastructure products rather than competing with them.

[ExperienceFlow](https://experienceflow.ai) (EDNS) sells "sense, simulate, act" to business functions using graph neural networks and multi-agent reinforcement learning; their buyer is the CFO or COO. Zyntra applies the loop to **infrastructure and the business services it carries**:

- the KPIs are already measured (Prometheus, DCGM, cloud billing), so there's no data-integration project before the first value;
- the actions are concrete and automatable (scale, repartition, preempt, migrate), and Zyvor products already perform them;
- buyers (platform, SRE, FinOps and ML-platform leads) already use the rest of Zyvor.

| | ExperienceFlow EDNS | Zyntra |
|---|---|---|
| Domain | Business functions | Infrastructure ops and the business services on top (GPU, Kubernetes, VMs, edge) |
| Graph | Roles and business KPIs | Infra and service KPIs (SLOs, capacity, cost) with owners and criticality |
| Simulation | GNN + multi-agent RL | Deterministic, bounded propagation with uncertainty bands; learned weights stay editable |
| Explainability | Claimed | Every number traced to an input, edge, effect, bound or saturation step |
| Action | Recommendations to people | Policy-gated, revalidated, executed through Zyvor adapters, outcome-verified |
| Deployment | SaaS via partners | Single binary, on-prem / air-gap friendly |

## Five foundations

These make every use case below credible. Build-order step 1 delivers all five.

| Foundation | What it means |
|---|---|
| **Business priorities and hard constraints** | KPIs carry a criticality weight. Constraints (floors, ceilings, must-not-worsen) are never traded off: a cost saving cannot compensate for breaching a critical availability target. |
| **A realistic simulator** | Multi-action plans, absolute and relative effects, KPI bounds, delayed effects, saturation curves and low/high uncertainty ranges. A KPI that starts at zero can rise; availability stops at 100%. |
| **Verified outcomes and rollback** | Observation windows, success criteria, guardrail regression detection and compensating actions. Records distinguish **dry-run validated**, **applied** and **outcome verified**. |
| **Signal freshness and model confidence** | Last-success timestamps, stale-data limits and missing-input warnings. Recommendations expose uncertainty, and execution is blocked where fresh data is required. Learned relationships stay editable and are never presented as proven causality. |
| **Enterprise approval controls** | OIDC identity, viewer/proposer/approver/executor roles, two-person approval, expiry, maintenance windows and mandatory revalidation before execution. "Block if Keep is unavailable" is an explicit policy. |

## Use-case packs

Each pack ships **connectors, KPI definitions, targets, dependency models, action templates, approval rules and outcome checks**.

| Use case | Features to add | Example decision |
|---|---|---|
| **SRE and incident response** | Multi-window SLO burn rates, incident timelines, deployment correlation, competing cause hypotheses, ranked runbooks | "Latency increased after deployment: roll back, scale, or fix networking?" |
| **GPU and AI infrastructure** | GPU memory pressure, queue deadlines, inference throughput, cost per million tokens, MIG-versus-full-GPU scenarios | "Should training wait, move, or release capacity for inference?" |
| **FinOps** | Cloud billing connectors, OpenCost integration, idle-resource detection, budget forecasts, savings verification | "Which changes reduce spending while preserving our SLO?" |
| **Kubernetes operations** | Pending-pod diagnosis, requests/limits recommendations, HPA/VPA interaction checks, disruption-budget constraints | "Will adding nodes solve this, or is placement the problem?" |
| **VMware migration and VM fleets** | Migration-wave planning, destination headroom, downtime constraints, dependency mapping, post-migration verification | "Which VMs should Transiva move first?" |
| **Network operations** | Netra signal correlation, service-path dependencies, congestion scenarios, approved network-policy changes | "Is the bottleneck TCP loss, DNS, storage traffic, or application load?" |
| **Storage and databases** | Capacity-exhaustion forecasts, IOPS/latency models, replication lag, backup freshness, restore-test results | "Expand storage, rebalance, or reduce workload concurrency?" |
| **Edge and disconnected sites** | Site-level graphs, offline decision queues, bandwidth-aware scheduling, maintenance windows, fleet-wide comparisons | "Which sites need intervention before connectivity disappears?" |
| **Release engineering** | KubeFlight risk inputs, canary analysis, baseline comparisons, promotion/rollback proposals | "Can this release proceed without consuming too much error budget?" |
| **Disaster recovery** | RPO/RTO tracking, recovery dependency graphs, failover simulations, drill evidence | "Can the secondary site absorb production today?" |
| **Security operations** | Vulnerability-to-service mapping, exposure-aware priorities, isolation proposals, availability-impact simulation | "Which remediation removes the most exposure with acceptable disruption?" |
| **MSPs and enterprise teams** | Tenant isolation, per-customer graphs, delegated approval, cross-fleet reporting, SLA evidence | "Which customer needs attention, and who can authorize the fix?" |

## Industry packs

Industry packs connect infrastructure health to business outcomes. For each industry Zyntra answers: **what is at risk, which action helps most, what trade-offs does it create, and did the action work?** These are proposed extensions beyond today's infrastructure capabilities.

**First three** (closest to existing signals and Zyvor products):

1. **Banking and payments:** transaction-service reliability, capacity and recovery decisions.
2. **Manufacturing and edge:** factory-site infrastructure continuity and inspection workloads.
3. **AI service providers and research:** GPU allocation, inference SLOs and cost optimization.

Retail, telecom and healthcare follow as connector coverage and outcome verification mature.

| Industry | Application | KPIs to connect | Features to add |
|---|---|---|---|
| **Banking and payments** | Protect transaction services during demand spikes | Transaction success, payment latency, queue depth, availability, cost | Transaction dependency graphs, peak-demand simulation, capacity recommendations, dual approval |
| **Insurance** | Keep claims and underwriting pipelines moving | Claims turnaround, backlog, document-processing latency, compute cost | Workflow-stage monitoring, bottleneck analysis, deadline-aware processing priorities |
| **Manufacturing** | Connect factory IT and edge infrastructure to production continuity | Telemetry freshness, inspection latency, gateway availability, production interruptions | Machine/line/site hierarchy, maintenance-window planning, edge-failure scenarios, MES connectors |
| **Healthcare** | Maintain availability of clinical systems and imaging services | PACS retrieval latency, imaging backlog, application availability, recovery time | Clinical-service criticality, imaging GPU priorities, downtime constraints, recovery planning |
| **Retail and e-commerce** | Prepare checkout and fulfilment systems for sales events | Checkout success, response time, order backlog, inventory-sync lag, infrastructure cost | Promotion-demand scenarios, checkout dependency maps, peak-capacity planning, rollback proposals |
| **Logistics and transport** | Maintain tracking, routing and warehouse operations | Tracking freshness, route-computation latency, warehouse backlog, connectivity | Fleet/site graphs, bandwidth-aware processing, offline queues, operational deadline constraints |
| **Telecom** | Prioritize intervention across network and edge sites | Packet loss, service availability, congestion, site capacity, restoration time | Network-service dependency graphs, site comparisons, maintenance simulation, traffic-shift proposals |
| **Energy and utilities** | Keep monitoring and analytics available across remote assets | Telemetry delay, data gaps, edge availability, analytics latency | Asset/site hierarchy, disconnected operation, redundant-path analysis, approved IT failover |
| **Government and public services** | Keep citizen portals available during peak demand | Submission success, processing backlog, availability, recovery readiness | Sovereign deployment, department ownership, seasonal-demand templates, approval evidence |
| **Education and research** | Allocate shared computing fairly during deadlines | GPU queue wait, job completion, lab availability, departmental spending | Academic-calendar forecasts, fair-share scenarios, project budgets, reservation planning |
| **Media and entertainment** | Deliver rendering, transcoding and streaming workloads on time | Render deadlines, encoding throughput, buffering, GPU cost | Deadline-aware batch planning, GPU-placement scenarios, streaming-capacity forecasts |
| **SaaS and software companies** | Protect customer experience while controlling costs | Tenant SLOs, API latency, deployment failures, cost per tenant | Tenant-level graphs, noisy-neighbour detection, canary decisions, verified rightsizing |
| **Construction and engineering** | Schedule BIM, simulation and digital-twin workloads | Simulation turnaround, GPU availability, project deadlines, storage growth | Project-aware resource allocation, workstation/VDI capacity planning, archival recommendations |
| **Agriculture and food processing** | Maintain field telemetry and processing analytics | Sensor freshness, connectivity, image-analysis backlog, cold-chain data gaps | Intermittent-connectivity support, seasonal workload forecasts, edge inference scheduling |
| **MSPs and data centres** | Manage service commitments across customers | Customer SLA, capacity headroom, incident backlog, margin | Customer isolation, delegated approvals, cross-customer capacity scenarios, service reports |

### Shared cross-industry capabilities

One Zyntra engine, with industry packs: shared simulation, policy and approval underneath; industry-specific services, KPIs and workflows above.

| Shared capability | What it enables |
|---|---|
| **Business-service graph** | Map technical resources to checkout, payments, imaging, production lines or citizen services |
| **Business-impact scoring** | Rank incidents using affected users, deadlines, service criticality and customer-supplied financial impact |
| **Industry template packs** | Ship KPI graphs, connectors, scenarios and approval rules for each sector |
| **Event-aware forecasting** | Account for festivals, examinations, billing cycles, promotions and production schedules |
| **Constrained planning** | Respect availability floors, budgets, data residency, maintenance windows and recovery requirements |
| **Decision evidence** | Preserve inputs, model version, prediction, approval, execution and measured outcome |
| **Connector SDK** | Let customers integrate ERP, MES, payment, hospital, logistics and other domain systems |
| **Historical replay** | Evaluate whether a proposed policy would have helped during previous incidents |

## Build order

| Order | Deliverable | Purpose |
|---|---|---|
| **1** | Reliable decision engine: constraints, freshness, bounded simulation, verified outcomes, decision record, enterprise approval controls | Establish trust in recommendations |
| **2** | Three ready-to-use packs: **SRE, GPU/AI, FinOps** | Immediate, measurable use cases |
| **3** | Kubernetes and GitOps executors with rollback and approval policies | Broaden the actions Zyntra can complete |
| **4** | VM migration, network, storage and edge packs | Connect more of Zyvor's portfolio |
| **5** | Multi-cluster/MSP operation and calibrated learned models | Support larger deployments |

Industry packs start after step 2, beginning with banking and payments, manufacturing and edge, and AI providers and research.

## Feature map

| Area | v0.2 | v0.3 (build order step 1, this repo) | Next |
|------|------|------|------|
| KPI graph | YAML model, DAG validation, owners, targets; lab model (20 KPIs) | Criticality, min/max bounds, freshness limits, hard constraints, edge confidence and provenance, model version hash | Pack templates; graph editor in the UI; business-service graph |
| Sense | Netra `/metrics`, Netra/Gravia/Fabric/Keep JSON, Prometheus, Kubernetes, per-source health | Per-KPI last success, stale detection, static vs live inputs, `/api/v1/freshness` | Cloud billing, OpenCost, DCGM direct, Fleet/Relay APIs, connector SDK |
| Simulate | Linear relative propagation with a trace | Multi-action plans, absolute effects, saturation, bounds, delays, low/high bands, constraint checks | Time-stepped simulation, historical replay |
| Plan | Severity drop minus risk penalty | Weighted severity, blocked list for constraint violations, uncertainty and staleness penalties, action pairs, confidence labels | Business-impact scoring, event-aware forecasts |
| Act | Approval inbox, Gravia CRD executors (dry-run default), Keep execution path | Revalidation before execution, blocked state, rollback templates | Kubernetes/GitOps executors, Fleet/Relay actions |
| Prove | Single predicted-vs-actual snapshot | Observation windows, success criteria, guardrail regression, verified/regressed/inconclusive, linked rollback proposals | Per-action scorecards, learned weight calibration |
| Govern | Shared key; one approver | OIDC (PKCE, group-to-role map), viewer/proposer/approver/executor/admin, two-person rule, expiry, maintenance windows, `keep: required` policy, hash-chained audit, signed decision export | SAML/SCIM, delegated approval, tenant isolation |
| UI | React console, 10 pages | Decision timeline, quorum/expiry/blocked reasons, plan bands and blocked list, SSO sign-in | Graph view, outcome history, pack catalog |

## Roadmap

- **v0.1 Simulate (done):** model, gaps, what-if, plan, CLI, REST and SSE API, dashboard, read-only Prometheus and Kubernetes adapters.
- **v0.2 Console, signals and approval gate (done):** web console, session auth, live Netra/Gravia/Fabric/Keep sources, grounded AI, approval inbox with audit, Gravia CRD executors in dry-run, Fabric Keep execution path, remote deploy and smoke test.
- **v0.3 Decision engine (build order step 1):** the five foundations and the decision record.
- **v0.4 Packs:** SRE, GPU/AI and FinOps packs; Kairo/KubeFlight deploy risk as an input.
- **v0.5 Executors:** Kubernetes and GitOps executors with rollback; Fleet/Relay actions.
- **Later:** VM migration, network, storage and edge packs; multi-cluster and MSP operation; learned edge weights with confidence intervals that stay visible and editable.

## Known limits (v0.3)

- On the lab host, Keep sandboxes cannot reach the egress broker: the FluxVM guest gateway is the netns bridge, not the host. With `keep: preferred` (the default), approved actions fall back to local execution and the audit labels them `zyntra (keep unavailable)`. Set `keep: required` in the policy to block instead.
- Uncertainty bands come from declared effect and edge confidence, not from calibrated history.
- Delays are reported as settle times; the simulator does not step through time.
- A model rewrite needs a Fabric InferenceEndpoint and an `fvai_` key. Without them, answers stay heuristic.

## Non-goals

- Making decisions with an LLM. An LLM may rewrite the grounded narrative, but never picks the action or computes the numbers.
- Running actions without approval in any release before an explicit, per-action auto-approve policy exists.
- Planning business functions (finance, supply chain, staffing). Industry packs connect infrastructure health to business KPIs; they do not plan the business itself.
- Clinical, safety or process-control decisions. Zyntra can recommend more imaging-processing capacity; diagnosis needs a separate validated system. Factory and utility integrations observe operational technology and propose changes through existing authorized control systems.
- Presenting learned relationships as proven causality.

## Open questions

- Licensing (resolved in v0.2): Zyvor Production License v1.0, free for non-production use. Production needs an annual subscription priced by managed clusters and KPI graphs; see [enterprise pricing](sales/enterprise-pricing.md).
- Domain: confirm `zyntra.dev` availability and register it.
