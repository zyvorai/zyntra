# Zyntra product plan

**One engine. Every industry is a pack.**

A KPI is a number with an owner, a unit, a target and a direction. An edge is "when this moves, that moves, and here is why." An action is a change a named human approves. The simulator does not know what a GPU is, and it does not know what a shop is. v0.2 only looked like an infrastructure product because its adapters were Netra, Gravia, Fabric, Keep and kubectl.

This plan removes that ceiling. Infrastructure stays pack zero because its signals are already live. It is not the product boundary.

## Non-negotiables

- No LLM picks, ranks or runs an action. A model may rewrite a grounded narrative. Every number comes from an input, an edge or an effect.
- Nothing runs until a named human approves, unless that action has an explicit auto-approve policy and a change window.
- Dry-run by default. Predicted versus actual is recorded on every execution.
- Single binary, on-prem, air-gap friendly. Packs are files. No SaaS dependency to get a plan.

Status markers below: **v0.3** shipped in the decision engine release, **A** shipped in Phase A and **B** in Phase B (both v0.4.0-dev, deployed to the lab host on 2026-10-02), **open** not built yet.

## 1. What already exists

**v0.2 (2026-10-02).** YAML model (KPIs, acyclic edges, actions, gap severity, topological propagation); CLI `graph`, `gaps`, `simulate`, `plan`, `serve`, `keep`, `fake-sources`; ten-page console with HMAC sessions; Prometheus, Kubernetes, Netra, Gravia, Fabric and Keep sources; z-score anomalies, time-to-breach forecasts and a grounded digest / ask / explain; proposal and approval inbox, Gravia CRD render, kubectl dry-run by default, Keep-brokered executor, hash-chained audit.

**v0.3, the decision engine.** Criticality and hard constraints; bounded simulation with absolute effects, saturation, delays and low/high uncertainty bands; action pairs; per-KPI freshness with execution blocked on stale required inputs; revalidation before execution; observation windows with verified, regressed, missed and inconclusive outcomes and linked rollback proposals; OIDC with a group-to-role map; viewer, proposer, approver, executor and admin roles; two-person approval, expiry and maintenance windows; signed decision export.

**Known hole, still open.** On the lab host, Keep sandboxes cannot reach the egress broker, so approved actions fall back to local execution and the audit says `zyntra (keep unavailable)`. `keep: required` blocks instead. The fallback stays labelled until egress works.

## 2. Engine work (blocks every industry)

A pack that cannot read a CSV or post a webhook is a demo, so this comes before twenty packs.

### 2.1 Pack format (A)

```text
packs/<id>/
  pack.yaml             # id, title, industry, version, owners, timezone, calendar(s)
  kpis.yaml             # kpis, edges, actions (existing schema, extended)
  sources.example.yaml  # every ${ZYNTRA_*} variable and file the pack reads
  README.md             # the three gaps this pack is judged on, one simulate trace
  fixture/              # sample exports used by `pack validate` and CI
```

```yaml
id: shop
title: Counter and stock
industry: retail
owners: [owner, floor, buyer]
timezone: Asia/Kolkata
calendar: shop-hours
```

Commands: `zyntra pack list`, `zyntra pack validate packs/shop`, and `-f packs/shop` (a directory or a single `kpis.yaml`) for `graph`, `gaps`, `simulate`, `plan` and `serve`. `pack validate` checks the manifest, owners, documented variables, that file sources read the fixture, that every action simulates and renders, and that a plan runs. The simulator never imports a pack by name.

Open: move the lab GPU model to `packs/gpu` (Phase C).

### 2.2 Generic sources (A)

| Kind | Reads | Use |
| --- | --- | --- |
| `file` | CSV, JSON, YAML or Prometheus text on disk; reloads when mtime or size changes | Tally export, meter dump, lab file |
| `http` | GET JSON or Prometheus text; field paths as before | POS, billing, any REST API |
| `webhook-in` | JSON POSTed to `/api/v1/ingest/<channel>` with the ingest token; last document wins; stale after `staleAfter` | UPI callback, SCADA gateway, alert |
| `sheet` | CSV over HTTP, same parser as `file` | Published Google Sheet, ERP export |
| `manual` | A value an operator enters in the console; every entry is audited | A count you cannot instrument yet |

Rows can be filtered (`where`), aggregated (`sum`, `avg`, `min`, `max`, `count`, `first`, `last`) and divided by a second aggregate (`denominator`), so "share of SKUs at zero stock" is one source line. Only `${ZYNTRA_*}` variables are expanded in URLs and headers, Zyntra's own credentials (access key, session secret, exec, ingest, Keep, OIDC and AI secrets) never are, and URLs are stripped from error messages so tokens do not leak.

Existing kinds stay: prometheus, kubernetes, metrics, json, netra, gravia, fabric, keep.

Every source reports `ok`, `stale`, `error` or `fallback`. A KPI on fallback keeps its declared value and is marked, so the plan cannot pretend the number is live.

### 2.3 Generic actions (A)

| Kind | Dry-run | Apply |
| --- | --- | --- |
| `noop` | Show the predicted trace | Record the approval only, for industries that act on paper |
| `webhook` | Print method, URL, headers and body (secrets stay as `${ZYNTRA_*}` references) | Send the body with `Idempotency-Key` set to the proposal id; store the status code and response sha256 |
| `file` | Print the file that would be written | Write it under `ZYNTRA_OUTPUT_DIR`, never overwriting and never outside that directory |
| `kubectl` | Server-side dry-run (v0.2) | Apply (v0.2) |
| `keep` | v0.2 | v0.2 |

```yaml
- id: reorder_fast_movers
  title: Raise PO for SKUs below cover
  adapter: file
  risk: medium
  window: buy-hours
  approvers: 1
  preconditions:
    - {kpi: stockout_rate, worse_than: 0.02}
  invariants:
    - {kpi: gross_margin, max_worsen: 0.03}
  compensate: cancel_open_po
  effects:
    - {kpi: stockout_rate, change: -0.4}
  file:
    path: po/{{.Stamp}}-reorder.md
    content: |
      ...
```

Webhook bodies may reference live values with `kpi:<id>` and `gap:<id>`. File content is a Go template over the action, KPIs, gaps and date.

- **Preconditions fail closed.** The action is shown and scored with status `precondition-failed`, but cannot be proposed; a stale input counts as a failure. Revalidation checks them again before execution.
- **Invariants** (`max_worsen`, a relative fraction) are checked against the nominal and pessimistic simulation. A break blocks the action and moves it to the blocked list.
- **Compensate** is a second action linked on the proposal and offered as the rollback. It never runs on its own.

### 2.4 Units, calendars, owners (A)

- Units are free strings plus a class: `percent`, `count`, `currency`, `duration`, `ratio`. Currency carries an ISO code (INR, USD). Gap math stays relative to target, so rupees and milliseconds share one scorer.
- One calendar implementation (weekly windows with days, start, end and timezone) is shared by KPIs, actions and the approval policy. Outside its window a KPI keeps its last in-window value, reported as `held`. A shop that misses target at 2am is not a gap.
- Owner cut: `zyntra gaps -owner floor`, `zyntra plan -owner floor`, `?owner=` on `/gaps`, `/plan` and `/ai/digest`, and an owner filter in the console.

### 2.5 Simulation upgrades

1. **Outcome record (v0.3 + A).** v0.3 records baseline, observation window and verdict on every proposal. Phase A adds predicted, actual, absolute error and hit or miss per KPI, and a hit rate. Learned weights and auto-approve depend on this.
2. **Bands (v0.3 + B).** Edges and effects carry confidence; after-values render as a range. Ranking now uses the pessimistic improvement alone (B); an action that wins only when every edge holds is marked optimistic-only and ranks below the rest.
3. **Pairs (v0.3 + B).** Pair search; the combined trace must beat either action alone. A pair whose members move the same KPI in opposite directions by 1% or more is flagged as working against itself and drops to medium confidence (B).
4. **Clamp (v0.3).** KPI bounds and saturation stop effects past a limit (a queue cannot go below zero wait).
5. **Learned weights (open, Phase D).** Estimate from history, show confidence, keep the weight editable, flag edges whose error stays high. Never hide the weight.

### 2.6 Identity, policy, audit

- OIDC (v0.3); the shared API key remains the air-gap fallback. A separate ingest token (A) can only post to `/api/v1/ingest/<channel>`.
- Approval record (v0.3 + A): subject, reason, model hash, prediction, rendered payload, and now pack id, action kinds, compensate link and response hash. The audit chain itself now carries `payload_sha256` and `response_sha256` on each event, so verification fails if either is altered (B).
- Two-person approval and change windows (v0.3); actions can now raise `approvers` and set their own `window` (A).
- Auto-approve (open): off until an action has a pack-level outcome hit rate, low risk, a window and held invariants on the last N runs. Default stays off.
- Keep stays available for packs that want a sandboxed executor. Webhook packs do not require Keep.

### 2.7 Console

No new product surface; the existing pages are extended.

- **Plan (A):** precondition and invariant states, owner filter; pairs and bands from v0.3.
- **Approvals (A):** webhook bodies, files and noop decisions render next to the Gravia CRD in the same inbox, with the compensating action.
- **Signals (A):** source state for every kind, manual value entry, webhook-in channels and when they last received data.
- **Decision record (A):** predicted versus actual per KPI with hit or miss.
- **Model (A + open):** shows the pack and action kinds. Open: load and validate a pack, edit a weight, graph view.
- **Ask (B):** same contract; grounding facts start with `pack:<id>@<version>`.
- **Plan (B):** worst-case column, optimistic-only and works-against-itself pills. **Decision record (B):** payload and response hashes on audit events.

## 2b. Business ontology (Phase E, in development)

An ontology layer for business objects, kept separate from the KPI simulator. Delivered on branch `feat/ontology`:

1. **Objects and provenance:** `internal/ontology` (schema, store, stable ids, impact traversal), `ontology.yaml` in packs, resolution queue, console Objects page.
2. **Permission-aware AI and typed actions:** access rules in the policy file, `internal/actions`, Ask citations, `/ai/propose`.
3. **Scenarios, workflows, verified outcomes:** `internal/scenario`, declarative views, object-level outcome checks.
4. **Packs, connector SDK, fleet shape:** `packs/manufacturing`, `internal/connector`, rollout in the signed decision, `make eval`.

5. **Scale and live data:** SQLite object store with migration, Kubernetes and SQL connectors with a scheduler, calibration of edge weights, the rollout contract for deployment tooling, tenant-scoped KPIs, three more packs, console component tests.

Still open: holding objects outside memory (the store is write-efficient but memory-resident), a larger connector catalogue (SAP, MES, PACS, object storage), per-tenant simulators (one instance now serves several packs, each with its own KPI graph, but tenants within a pack share it), and delivery itself (belongs to the deployment tooling, not Zyntra).

## 3. Pack catalog

Each pack is a file set, not a fork. The first wave is the one a buyer can feed with a CSV this week; the second needs a live system. Weights are starting points, marked editable, not truth.

| Pack | Sources | KPIs | Actions | Status |
| --- | --- | --- | --- | --- |
| **GPU cluster** (pack zero) | Netra, Gravia, Fabric, Keep, Prometheus, kubectl | Lab model (20 KPIs) | Preempt batch, MIG share, job suspend, scale node pool | **`packs/gpu`** (live sources; no fixture, validated against the model) |
| **Shop and counter** | POS, stock, settlement and supplier exports; manual counters | Stockout rate, gross margin, dead-stock days, queue wait, daily sales, cash, supplier lead time | Reorder fast movers (file PO), capped markdown (webhook), second counter in the evening window (noop), drop a supplier (file); compensate: cancel PO, reverse markdown | **A: `packs/shop` with fixture** |
| **Manufacturing and plant** | MQTT/OPC-UA gateway via webhook-in, energy meter CSV, schedule file, Fabric | OEE, scrap rate, kWh per unit, schedule adherence, downtime | Move a job (file work order), hold a batch (noop plus ticket webhook), shed load in a tariff window (webhook) | open |
| **Pharma and batch** | Batch-record export, environmental monitoring CSV, qualification-expiry file | Deviations, release cycle time, chamber uptime, qualification days left | Quarantine a lot (webhook, two-person), reschedule a campaign (file), fail over to a qualified spare (precondition: spare qualified) | open |
| **Energy and utility** | Meter file or webhook-in, outage tickets, renewable forecast | SAIDI, SAIFI, peak versus contract, curtailment, kWh cost | Shed a feeder tier (webhook, window, two-person), shift a pump schedule (file), pre-cool before the tariff peak | open |
| **Payments and banking ops** | Auth latency and declines, fraud-queue depth, scheme-fee file | p99 auth, approval rate, queue depth, cost per transaction | Shed a noisy tenant (two-person), fail over an issuer route (window), raise standby capacity; no auto-approve | open |
| **Telecom site** | Site KPI file, backhaul loss, energy per site, ticket age | Drop rate, congestion, kWh per site, ticket age | Carrier shutdown (window), move traffic, open a field window | open |
| **Warehouse and logistics** | WMS export, dock scans, carrier ETA webhook-in | Order cycle time, dock dwell, pick rate, cost per order | Rebalance a wave, hold a carrier, open a shift (pairs matter) | open |
| **Clinic operations** (capacity only) | Bed board CSV, theatre list, lab turnaround | ED wait, boarding hours, staffed-bed headroom, theatre utilisation | Open a surge bay (two-person), divert a category, reassign a list. No action describes a diagnosis or treatment. | open |
| **Campus and lab** | KubeVirt/Zorvia queue, Atlas pool, Haven login failures | Lab wait, storage headroom, login failure rate | Preempt an image build, grow a pool, drain a noisy project | open |
| **Public sector desk** | Ticket export, sensor uptime, permit queue | Time to acknowledge, device health, backlog age | Escalate a ward, reroute a crew, pause an integration; file source and file action suffice air-gapped | open |

**Second wave**, after the generic executor is boring: hotel occupancy, fleet maintenance, agri cold-chain, insurance claims queue, media CDN cost versus p95, construction equipment idle time. Same schema, no engine change. A pack that needs a new source kind is rejected until the kind is generic.

## 4. Build order

### Phase A: engine, one binary (done on this branch)

- [x] `file`, `http`, `sheet`, `webhook-in` and `manual` sources with ok, stale, error and fallback states.
- [x] `webhook`, `file` and `noop` actions; preconditions, invariants, compensate link.
- [x] Pack directory, loader, `pack list`, `pack validate`.
- [x] Predicted versus actual per KPI on the outcome record.
- [x] Owner filter; currency and duration units; one weekly-window calendar.
- [x] Console renders webhook bodies and files the way it renders a CRD.
- [x] Tests: the shop pack against its fixture; the plan closes stockout and refuses the 30% markdown because it breaks the margin invariant.

Exit: `zyntra plan -f packs/shop` ranks a plan from the CSVs in `packs/shop/fixture/` and `zyntra simulate -f packs/shop -action reorder_fast_movers` prints a dry-run PO, with no Kubernetes in the loop.

### Phase B: trust

- [x] Rank strictly on the pessimistic band; flag pairs that cancel.
- [x] Audit chain includes the rendered payload and response hash.
- [x] Pack id in Ask grounding.
- [ ] Keep egress fixed. Until then the fallback stays labelled `zyntra (keep unavailable)` in the audit and the smoke test. Not hidden.

Already shipped in v0.3 and extended in Phase A: two-person approval, change windows, OIDC with API-key fallback, capped pair search.

Exit: a high-risk webhook cannot be approved by one operator outside the window, and a pair outranks a single action when the fixture says it should.

### Phase C: packs and pilots

Ship files, not claims.

- [x] Move the GPU lab model to `packs/gpu`.
- [x] Ship packs everywhere: the deploy script takes `--pack`; the container image, compose file and Helm chart carry `packs/`.
- [ ] Write plant, pharma, energy, payments and campus packs, each with a fixture and a README showing one simulate trace.
- [x] Webhook receiver example in `examples/receiver` so a pilot can see the PO land.
- [x] Owner shift digest: `?owner=&window=` on the digest, with selectors on the Overview.
- [x] `zyntra pack draft`: a sample CSV or JSON plus an industry line gives a pack that validates; actions without a cited column are refused.

Exit: six packs validate in CI, and two have run against a real export (shop CSV, plant CSV or the GPU lab).

### Phase D: learning, still explainable

- Learned weights with confidence, editable, missed edges flagged.
- [x] Miss explainer: per verdict, which edge overshot, which input was stale or on fallback, which precondition failed; the explanation hash sits in the audit event next to the verdict.
- [x] Proposed edges from correlated history, shown as "proposed, not in the model" with a weight band and YAML.
- [x] Similar past decisions as precedents in the inbox; the ranking is unchanged.
- [x] Pack README rules checked against the plan, preconditions and policy.
- [x] Payload fill from cited source rows, frozen on the proposal and covered by the payload hash.
- Auto-approve policy, default off, only where Phase B gates pass and the hit rate is real.
- Clamp edges for packs that saturate.

Exit: a weight changes only when history supports it, and the console shows the old weight, the new weight and the error.

### Explicitly not in this plan

- An LLM that chooses the action, adjusts a score, turns on auto-approve, or produces a forecast number. No chat box that acts.
- A marketplace, billing SaaS or mobile app before Phase A is done.
- A pack that recommends a clinical, legal or credit decision about a person. Capacity, queue, price list and route only.
- Hiding the Keep fallback.

## 5. How a pilot runs

1. Pick a pack: shop if they have a POS export, GPU if they run Gravia, plant if they have a meter CSV.
2. Bind sources to their files or URLs. Leave actions on dry-run.
3. Run for five business days. Gaps and the plan are the demo. No apply.
4. Approve one low-risk action as a file or a webhook into a test inbox. Compare predicted and actual the next morning.
5. Only then point the webhook at the real system, still with a window and a named approver.

Pricing stays the existing production license, by clusters and KPI graphs (see [enterprise pricing](sales/enterprise-pricing.md)). A shop is one graph. A plant with three lines is three graphs, or one graph with an owner per line. No second product to invoice it.

## 6. Done when

- [ ] Any pack in `packs/` runs through gaps, simulate, plan, approve, dry-run and outcome without a code change. (True for `shop` end to end in e2e; `gpu` runs gaps, simulate, plan, approve and dry-run on the lab host. A second file-only pack would close this.)
- [x] The shop fixture and the GPU lab model both pass CI (`pack validate` on every pack, plus e2e for both).
- [x] Someone who has never read the infra README can point `-f` at a CSV and get a ranked action with a why-trace.
- [x] Apply does nothing until that person approves.

## Status on the lab host

v0.4.0-dev runs on the reference lab (212.8.248.187) two ways:

- **systemd binary** on :19620 serving `packs/gpu` with live Netra, Gravia, Fabric and Keep sources. The remote smoke test proposes, approves and dry-runs a Gravia change end to end. Both shipped packs validate on the host.
- **k3s Helm release** in namespace `zyntra` on NodePort 30962 serving `packs/shop` from the container image with the test receiver. The smoke test proposes, approves and dry-runs a file action end to end.

The same image also passed a podman run with a read-only root and apply mode: an approved markdown reached the receiver with the proposal id as the idempotency key, and the audit chain verified after a restart.

## Known limits

- The Keep egress fallback on the lab host (above).
- Bands come from declared confidence, not calibrated history.
- Delays are reported as settle times; the simulator does not step through time.
- The calendar is weekly windows only: no holidays or one-off freezes yet.
- `webhook-in` keeps only the last document per channel; there is no history replay.

## Open questions

- Domain: confirm `zyntra.dev` availability and register it.
