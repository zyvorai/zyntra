# Zyntra

[![CI](https://github.com/zyvorai/zyntra/actions/workflows/ci.yml/badge.svg)](https://github.com/zyvorai/zyntra/actions/workflows/ci.yml)
[![License: Zyvor Production v1.0](https://img.shields.io/badge/License-Zyvor%20Production%20v1.0-orange.svg)](LICENSE)
[![Version](https://img.shields.io/github/v/release/zyvorai/zyntra?label=version&color=informational)](CHANGELOG.md)
[![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)](go.mod)
[![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=black)](web/package.json)

[![Book a demo](https://img.shields.io/badge/Book_a_demo-0071e3?style=for-the-badge)](https://zyvor.dev/schedule?utm_source=github&utm_medium=zyntra&utm_campaign=readme_hero)
[![30-day PoC](https://img.shields.io/badge/30--day_PoC-1d1d1f?style=for-the-badge)](https://zyvor.dev/poc?utm_source=github&utm_medium=zyntra&utm_campaign=readme_hero)
[![Pricing](https://img.shields.io/badge/Pricing-7c3aed?style=for-the-badge)](docs/sales/enterprise-pricing.md)

![Zyntra — decision intelligence for operations: live signals and CSV exports, KPI graph, approval gate, Fabric Keep](docs/social/zyntra-share-card.png)

### Stop arguing over dashboards. Know the next best action — and why.

**Decision intelligence for infrastructure ops, and for any business that can export a CSV. Sense, simulate, act — with a human in the loop.**

**Explainable, not generative** · **Approval-gated** · **Dry-run by default** · **Read-only sources** · **Packs are files**

Zyntra keeps a live graph of the KPIs your infrastructure is judged on (SLOs, latency, queue wait, capacity headroom, spend), shows which ones are missing target and by how much, simulates candidate actions through the dependency graph, and ranks them. Every recommendation is explained step by step and waits for human approval. The engine is domain-agnostic: infrastructure is pack zero, and [packs](#packs-any-industry) such as [shop](packs/shop) run the same loop on CSV exports and webhooks.

![Zyntra console — Overview](docs/ux/overview.png)

> **Maturity (honest):** v0.3 turned the approval inbox into a decision engine: criticality-weighted scoring, hard constraints, prediction ranges, per-KPI freshness, combined actions, quorum approvals with roles and SSO, re-checks right before execution, outcome verification with linked rollbacks, and signed, hash-chained decision records. v0.4 (in development, Phase A of the [plan](docs/PRODUCT_PLAN.md)) adds packs, file/http/sheet/webhook-in/manual sources, webhook/file/noop actions with preconditions and invariants, owner filters and predicted-versus-actual per KPI. Two packs ship: [shop](packs/shop) and [gpu](packs/gpu) (the lab model). Ranking now uses the pessimistic case, pairs that work against themselves are flagged, and the audit chain covers the rendered payload and the response hash. Zyntra runs as a single binary, a container image, or a Helm release. Changes still execute **only after human approval** and in dry-run by default. The simulator is a deterministic model over the edge weights and effects you supply; it does not learn them, and its ranges come from the uncertainty you declare, not from data. Sources only read. The AI layer explains and forecasts; it never picks or runs an action.

## Why Zyntra

Infra teams answer "what should we do next?" with a dozen dashboards and a meeting. Adding nodes shortens the queue but blows the budget. MIG frees GPUs but adds latency. Zyntra makes those trade-offs explicit:

| When this happens… | Zyntra gives you… |
|---|---|
| Five dashboards are red and nobody agrees what matters | **Gaps:** which KPIs miss target, who owns them, and how far off they are |
| "If we add nodes, what happens to latency and spend?" | **What-if:** the predicted change to every KPI, with the full propagation path |
| Three fixes are proposed in the incident channel | **Plan:** all actions ranked by gap closed minus risk, flagging any gap an action would *open* |
| You don't trust an AI to touch production | **No hallucinations:** no LLM makes decisions. Every number traces back to an input, an edge or an action effect, and nothing runs until a named human approves |

![How Zyntra works — live sources, KPI graph, what-if, plan, approve and run](docs/ux/readme-how-it-works.jpg)

## See it

| | |
|---|---|
| ![Plan — every action ranked by improvement minus risk](docs/ux/plan.png) | ![Simulate — what-if with before, after and target](docs/ux/simulate.png) |
| **Plan:** every action ranked; nothing runs until approved | **Simulate:** before/after for each KPI, plus the propagation trace |
| ![Approvals — proposal with prediction and rendered Gravia CRD](docs/ux/approvals.png) | ![Signals — live Netra, Gravia, Fabric and Keep KPIs](docs/ux/signals.png) |
| **Approvals:** prediction, baseline and the exact change (Gravia CRD, webhook or file) | **Signals:** live sources with trend and target status |
| ![Ask Zyntra — grounded answer with sources](docs/ux/ask.png) | ![Overview in dark mode](docs/ux/overview-dark.png) |
| **Ask:** grounded answers that list their facts | **Dark mode**, styled like Netra |

![Capabilities at a glance — Sense, Decide, AI, Act](docs/ux/readme-capabilities.jpg)

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

Or run a shop from CSV exports, with no Kubernetes and no live sources:

```bash
./bin/zyntra plan -f packs/shop
./bin/zyntra simulate -f packs/shop -action reorder_fast_movers   # prints the dry-run purchase order
ZYNTRA_API_KEY=dev ./bin/zyntra serve -f packs/shop
make run-shop             # same, with webhooks landing in the test receiver on :9099
```

Sign in to the console as **`admin` / `Admin@321`**. That account exists only while the policy file defines no users. The console shows a warning banner until you change the password with `ZYNTRA_ADMIN_PASSWORD`. Set it, or define users in the policy file, before anyone else can reach the console.

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

- **Gap severity** is the relative shortfall against target (`0.4` = 40% off), multiplied by the KPI's criticality weight (`critical` 4, `high` 2, `normal` 1, `low` 0.5). The plan minimises the weighted sum.
- **Propagation** runs in topological order with interval arithmetic: every effect and edge carries a low/nominal/high band, so predictions come with a range. Effects are relative (a fraction) or `mode: absolute` (in the KPI's unit, so a KPI at zero can move), can `saturate` and take a `delay`. Results are clamped to `min`/`max`; KPIs without bounds can't drop below zero.
- **Hard constraints** (`constraints:`) set a floor, ceiling or `mustNotWorsen` on a KPI. Critical KPIs with a target are constraints automatically. An action that would breach one, even at the pessimistic end of its band, is listed as blocked instead of ranked.
- **Score** = weighted gap reduction in the **pessimistic** case (every edge and effect at the bad end of its declared uncertainty) − risk penalty (low 0, medium 0.05, high 0.15) − 0.1 per stale input. `plan` shows both the nominal gain and the worst case. An action that only wins when every edge holds is marked *optimistic only* and ranks below the ones that win anyway. Zyntra also tries pairs of actions that touch different KPIs and keeps a pair only if it beats both actions alone; a pair whose members push the same KPI in opposite directions (by 1% or more) is flagged *works against itself* and drops to medium confidence. Each recommendation has a confidence (high, medium, low).
- **Preconditions and invariants** (v0.4). An action whose precondition does not hold (or whose input is stale) is ranked after the approvable ones with status `precondition-failed` and cannot be proposed. An action whose simulation worsens an invariant KPI by more than `max_worsen`, at the nominal or pessimistic end, is blocked like a constraint breach.

### Model reference (v0.3)

```yaml
kpis:
  - id: app_availability
    value: 99.95
    target: 99.9
    direction: higher
    criticality: critical         # weights severity; critical + target = hard floor
    min: 0
    max: 100                      # results are clamped to bounds
    freshness: {maxAge: 1m, required: true}   # required: block execution when stale
constraints:
  - {kpi: host_memory_percent, ceiling: 90, why: OOM above 90%}
  - {kpi: gravia_available_gpus, mustNotWorsen: true}
edges:
  - {from: queue, to: latency, weight: 0.05, confidence: 0.6, provenance: learned, delay: 5m}
actions:
  - id: raise-inference-priority
    effects:
      - {kpi: gpu_queue_wait_min, change: -0.35, uncertainty: 0.3, delay: 5m}
      - {kpi: gpu_utilization, change: 10, mode: absolute, saturation: 30}
    execute: {template: gravia.priority, params: {name: inference-critical, value: "900000"}}
    rollback: {template: gravia.priority-delete, params: {name: inference-critical}}  # optional; derived for Gravia templates
    outcome:                       # how to judge the action after it runs
      window: 20m
      samples: 3                   # consecutive fresh samples that must pass
      successCriteria: [{kpi: gpu_queue_wait_min, op: met}]   # met | < | <= | > | >= with value
      guardrails: [app_availability]
      tolerance: 0.05
    policy: {approvals: 2, keep: required, requireFresh: true, maintenanceWindows: [weeknights]}
```

### Model reference (v0.4 additions)

```yaml
timezone: Asia/Kolkata            # default for calendars (a pack sets it in pack.yaml)
calendar: shop-hours              # default calendar for every KPI
calendars:                        # weekly windows; end before start wraps midnight
  shop-hours: {start: "09:00", end: "21:30"}
  buy-hours: {days: [mon, tue, wed, thu, fri, sat], start: "10:00", end: "17:00"}
kpis:
  - id: daily_sales
    unitClass: currency           # percent | count | currency | duration | ratio
    currency: INR                 # ISO code; shown as the unit
    calendar: shop-hours          # outside the window the last in-window value is held
    source: {kind: file, file: fixture/pos.csv, field: "*.amount"}
  - id: cashiers_open
    source: {kind: manual, staleAfter: 12h}    # entered in the console, audited
  - id: line_temp
    source: {kind: webhook-in, name: plant-gateway, field: temp_c, staleAfter: 10m}
actions:
  - id: markdown_capped
    title: Mark down dead stock 10%   # title is an alias for name
    adapter: webhook                  # webhook | file | noop, or a v0.3 adapter with execute
    window: evening                   # approved runs wait for this calendar window
    approvers: 2                      # raises the policy quorum for this action
    preconditions: [{kpi: stockout_rate, worse_than: 0.02}]   # or better_than
    invariants: [{kpi: gross_margin, max_worsen: 0.03}]       # relative fraction
    compensate: reverse_markdown      # linked undo, offered as the rollback; never auto-run
    webhook:
      method: POST
      url: ${ZYNTRA_POS_URL}/markdowns
      headers: {Authorization: "Bearer ${ZYNTRA_POS_TOKEN}"}
      body: {percent: 10, gap: "gap:dead_stock_days", now: "kpi:dead_stock_days"}
  - id: reorder_fast_movers
    adapter: file
    file:
      path: "po/{{.Stamp}}-reorder.md"    # relative, inside ZYNTRA_OUTPUT_DIR
      content: |
        Stockout {{pct (index .KPIs "stockout_rate").Value}} on {{.Date}}
```

| Source field | Meaning |
|---|---|
| `kind` | `file`, `http`, `sheet`, `webhook-in`, `manual`, or a v0.3 kind (`prometheus`, `kubernetes`, `metrics`, `json`, `netra`, `gravia`, `fabric`, `keep`) |
| `file` / `url` | Path relative to the model (or pack) directory / URL; `${ZYNTRA_*}` is expanded |
| `format` | `csv`, `json`, `yaml` or `prometheus`; guessed from the extension or content type |
| `field` | Field path into the document, as for `json` sources; `*.amount` collects a column |
| `where` | Keep only rows whose columns equal these values |
| `agg` | `sum` (default), `avg`, `min`, `max`, `count`, `first`, `last` |
| `denominator` | Second field path; the KPI is `field / denominator` |
| `headers` | Request headers for `http` and `sheet` |
| `name` | The `webhook-in` channel (`POST /api/v1/ingest/<name>`) |
| `staleAfter` | Age after which a `webhook-in` or `manual` value is stale |

Webhook bodies resolve `kpi:<id>` to the live value and `gap:<id>` to the gap (value, target, severity). File content is a Go template with `.Action`, `.KPIs`, `.Gaps`, `.Date`, `.Time` and `.Stamp`, plus `pct` and `num`; a missing key is an error, not an empty string.

Edge `confidence` below 1 widens the band; `provenance: learned` marks the relationship as assumed in the trace. A KPI's freshness defaults to three refresh intervals; values older than that are `stale`, never-fetched ones `missing`, and KPIs without a source `static`.

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

Fields support `a.b.0`, `list.#` (count), `list.#(k=v)` (count matches), `list.#(k=v).f` (field of the first match) and `list.*.f`, plus `scale`, `agg: sum|avg|max|min` and `rate`. See [packs/gpu](packs/gpu) for a full lab model (20 KPIs) that uses every v0.3 field.

## Packs (any industry)

The engine knows nothing about GPUs or shops. A **pack** is a directory of files: `pack.yaml` (id, owners, timezone, calendars), `kpis.yaml`, `sources.example.yaml`, a README and a `fixture/` of sample exports. [packs/shop](packs/shop) runs a shop from CSV exports with no Kubernetes in the loop:

```bash
zyntra pack list
zyntra pack validate packs/shop
zyntra plan -f packs/shop                                   # ranks a reorder, refuses a markdown that breaks the margin invariant
zyntra simulate -f packs/shop -action reorder_fast_movers   # prints the dry-run purchase order
zyntra gaps -f packs/shop -owner floor
```

**Generic sources.** `file` (CSV, JSON, YAML or Prometheus text, reloaded on change), `http` and `sheet` (the same over HTTP), `webhook-in` (a gateway POSTs JSON to `/api/v1/ingest/<channel>` with `ZYNTRA_INGEST_TOKEN`) and `manual` (entered in the console, audited). Rows can be filtered, aggregated and divided:

```yaml
- id: stockout_rate               # share of SKUs with nothing on hand
  unitClass: ratio
  source: {kind: file, file: fixture/stock.csv, field: "#(on_hand=0)", denominator: "#"}
- id: daily_sales
  currency: INR
  calendar: shop-hours            # outside the window the last in-window value is held
  source: {kind: file, file: fixture/pos.csv, field: "*.amount"}   # summed
- id: erp_open_pos
  source: {kind: http, url: "${ZYNTRA_ERP_URL}/po?status=open", headers: {Authorization: "Bearer ${ZYNTRA_ERP_TOKEN}"}, field: "#"}
```

Every source reports `ok`, `stale`, `error` or `fallback`. Only `${ZYNTRA_*}` variables are expanded, and they stay unexpanded in rendered proposals.

**Generic actions.** Besides kubectl and Keep, an action can be a `webhook` (dry-run prints method, URL, headers and body; apply sends it with an `Idempotency-Key` and records the status and response hash), a `file` (written under `ZYNTRA_OUTPUT_DIR`, never overwritten) or `noop` (people do it; the approval is recorded). Actions can also declare:

```yaml
  window: buy-hours                                  # approved runs wait for the window
  approvers: 2                                       # two-person
  preconditions: [{kpi: stockout_rate, worse_than: 0.02}]   # fail closed: shown, scored, not approvable
  invariants: [{kpi: gross_margin, max_worsen: 0.03}]       # a simulated break blocks the action
  compensate: cancel_open_po                         # linked on the proposal as the undo; never auto-run
```

After an apply, the decision record compares predicted and actual per KPI and marks each a hit or a miss. Every audit event for an action also carries `payload_sha256` (what was approved and sent) and, after a webhook, `response_sha256`; both are inside the hash chain, so `GET /api/v1/audit/verify` fails if either is altered.

**Test inbox.** `examples/receiver` (built as `bin/zyntra-receiver`) accepts webhook deliveries, stores one JSON file per delivery, answers a repeated `Idempotency-Key` with `200 {"duplicate":true}` instead of recording it twice, and redacts `Authorization`, `Cookie` and `X-Api-Key`. Point a pack's URLs at it during a pilot (`make run-shop` does) and open `http://127.0.0.1:9099` to see what landed. Shipped packs: [shop](packs/shop), [gpu](packs/gpu) (the lab model), [manufacturing](packs/manufacturing), [logistics](packs/logistics), [payments](packs/payments) and [imaging-ops](packs/imaging-ops) (capacity and flow only: no patient data, no clinical decisions). The newer ones are starting points: their weights are declared, not measured. See [docs/PRODUCT_PLAN.md](docs/PRODUCT_PLAN.md) for the pack catalog and build order.

## Business ontology (new, v0.4 in development)

The KPI graph says how numbers move. The ontology says which business things sit behind them: which orders depend on an inspection service, which customers on a cluster. A pack adds an `ontology.yaml` next to `kpis.yaml`:

```yaml
objects:
  - {name: Cluster, kpis: [gpu_queue_wait_min], properties: [{name: name, type: string}]}
  - {name: Service, properties: [{name: name, type: string}]}
links:
  - {name: runs_on, from: Service, to: Cluster}
mappings:
  - {source: fixture/services.csv, type: Service, namespace: erp, key: service_id,
     props: {name: name}, links: [{type: runs_on, column: cluster_id, to: Cluster, namespace: erp}]}
```

```bash
./bin/zyntra ontology impact -f packs/manufacturing Cluster:infra:gpu-a   # what depends on this cluster
./bin/zyntra scenario compare -f packs/manufacturing gpus=add_gpu_capacity site=alternate_inspection_site
```

- **Separate from the simulator.** Links never carry numbers. An object points at the KPIs that measure it; only declared KPI edges predict.
- **Provenance on every fact.** Source, observed time, ingest time and transforms; Ask cites them.
- **Permissions on every read.** Object types, tenants, properties and typed actions per role, enforced in retrieval, so an answer cannot cite what you could not open.
- **Typed actions** take objects as inputs, are checked for role and object state, still need approval and run dry by default. An `evidence:` rule can require named facts to be present and no older than, say, `1h`. The proposal records a digest of the objects and the action contract; if a fact changed, or evidence went stale, between approval and execution, the run is blocked and a new proposal is needed.
- **Change history** per object (`GET /api/v1/ontology/objects/{id}/history`): before and after values with provenance, filtered by your current access, so a property that is now restricted does not show in old entries.
- **Push ingest.** `POST /api/v1/ontology/ingest/{tenant}` takes a bounded batch of normalized records from a connector with `ZYNTRA_INGEST_TOKEN`. It needs an explicit grant (`ingest_tenants` in a policy `access:` rule; admins always may), is all-or-nothing, and cannot touch another tenant's objects, even through an alias. `examples/ontology/connector.py` (stdlib only, dry-run by default, HTTPS required off localhost, no redirects) maps an upstream JSON export to records; `--jsonl` feeds an exec connector instead.
- **Optional model-assisted object selection.** With `ZYNTRA_AI_BASE_URL` set, the model may narrow which already-permitted objects an answer covers. It sees only objects the asker can read, can only choose among them, and invalid or empty output is ignored. Text and citations always come from the records.
- **Honest limits:** the object store is a JSON file (fine for thousands of objects, not millions); entity resolution is deterministic aliases plus a human review queue, not ML; the rollout shape is exported in the signed decision and delivery stays in your deployment tooling; Kubernetes is not yet a connector. See *Tenants and connector credentials* below for exactly what tenant isolation covers.

### Scale, live data, calibration and fleet handoff

**Storage.** The ontology store has two backends. The default is one JSON file, rewritten as a whole on each write, which is fine for a few thousand objects. `ZYNTRA_ONTOLOGY_STORE=sqlite` keeps objects, links, identity candidates and the change log in `ontology.db` (pure-Go SQLite, so `CGO_ENABLED=0` builds still work): writes touch only what changed, one transaction per ingest batch, and an object's history is read from disk. Lookups by alias, link and identity match are indexed. On the benchmark, ingesting 5,000 objects takes about 83 ms on SQLite and 23 ms batched on JSON, against 35 s before batching. Move an existing install with `zyntra ontology migrate -f PACK -from state/ontology.json -to state/ontology.db`; an existing `ontology.db` is used even when the variable is unset. Objects and links are held in memory, at about **1.4 KB per object** with a link (measured: 100,000 objects retain 143 MB; 1,000,000 would be about 1.4 GB, and the process held 114-150 MB with 12.6k objects loaded on a live host). SQLite removes the write cost and the file-size limit, not that footprint, so a cap stops a runaway source from exhausting the host: `ZYNTRA_ONTOLOGY_MAX_OBJECTS` (default 1,000,000; 0 for none). A batch that would take the store past the cap is refused whole, before anything is written, with an error that says how many objects it holds, how many the batch adds and what to change; updating objects that already exist always fits. `GET /api/v1/ontology/stats` (approver) and a line on the Objects page show objects, links and the cap, with a warning from 80%. Proposals remain a JSON file, but the audit chain is its own append-only file (`approvals.json.audit.jsonl`, one event per line, synced on write): a save costs one line instead of rewriting the history, and the bytes already written are never touched. An older state with the trail inline is moved over on first start with the chain and head hash unchanged (verified on a live host), and a crash that leaves a half-written last line is trimmed on start while damage anywhere else is refused as tampering. After upgrading, an older binary cannot read the new state: keep the `approvals.json` backup if you may need to roll back.

**Connectors and refresh.** Besides CSV mappings, a pack can declare connectors that the scheduler runs on an interval (default 5 minutes; file mappings every minute):

```yaml
connectors:
  - name: k8s-nodes                     # kubectl get nodes -o json, flattened by dotted path
    kind: kubernetes
    resource: nodes
    interval: 1m
    fields: {name: metadata.name, gpus: 'status.capacity.nvidia\.com/gpu'}
    mapping: {type: Node, namespace: k8s, key: name, props: {name: name, gpus: gpus}}
  - name: crm                           # a read-only SQL query; $1 is the time of the last good run
    kind: sql
    driver: postgres                    # or sqlite
    dsn_env: ZYNTRA_ERP_DSN             # the connection string comes from the environment, never the pack
    query: "SELECT id, name, updated_at FROM customers WHERE updated_at > $1::timestamptz"
    mapping: {type: Customer, namespace: crm, key: id, props: {name: name}, observed: updated_at}
```

A third kind, `rest`, reads any paged JSON API (OData, ServiceNow, Odoo and most ERP/MES/ticketing systems) with the same `fields` and `mapping`:

```yaml
  - name: erp-assets
    kind: rest
    url: https://erp.example/odata/Assets
    token_env: ZYNTRA_ERP_TOKEN          # or user_env + pass_env for basic auth
    items: value                          # dotted path to the list; empty if the body is the list
    next: '@odata\.nextLink'              # a dot inside a key is escaped; or page_param: page + page_size
    since_param: modified_after           # optional: receives the last good run's time
    fields: {id: AssetID, name: Name, status: State}
    mapping: {type: Asset, namespace: erp, key: id, props: {name: name, status: status}}
```

The token is sent only to the host in `url`: a next-page link or a redirect to any other host or scheme is refused, secrets and query strings are kept out of errors, and a pull is bounded (1,000 pages, 500k items, 64 MiB a page). Leave out `since_param` to make it a full listing that can `prune`. It was run against the public Northwind OData service (77 products over real `@odata.nextLink` pages); the OData and Odoo/ServiceNow shapes beyond that rest on the generic options above, not on tests against those products.

The SQL connector accepts one `SELECT` (or `WITH ... SELECT`), runs it in a read-only transaction (a data-modifying CTE is refused by the database, tested against a real Postgres) and keeps the connection string out of every error. Failures back off up to 8x the interval and the wait counts from the end of a run; a connector never blocks another, and each one's last success, error, counts and cursor survive a restart. The **Sources** card on the Objects page shows health (and flags a connector that spends more than half its interval running); `POST /api/v1/ontology/connectors/{name}/run` (admin) runs one now. A fact read from a file is dated by the file's modification time, not the read time, so re-reading an old file does not make it look fresh; a typed action's `evidence:` rule (`max_age: 10m`) then blocks a proposal on stale facts until a refresh brings new ones. [examples/ontology/gpu-live.yaml](examples/ontology/gpu-live.yaml) is a live ontology for the gpu pack.

Kubernetes specifics, from running it against a real k3s cluster of 12,622 pods: read an array element by key, not position (`status.conditions.[type=Ready].status`; a numeric index read the wrong condition), narrow the listing at the source with `k8s_namespace`, `k8s_selector` or `k8s_field_selector`, and set `prune: true` on a full listing so objects deleted in the cluster leave the ontology (only objects that this connector alone vouches for are removed, an empty or all-failed listing never prunes, and the removal is in the audit note). Output is streamed one item at a time (Zyntra held about 150 MB with 12.6k objects loaded) and capped at 512 MiB, but `kubectl get pods -A -o json` itself took 14 s and 1.5 GB of RAM on that cluster, which no connector can avoid: select what you need. A SQL query with no `$1`/`?` parameter is a full listing, takes no argument and may set `prune: true`; a query that filters on the last-run time returns changes only, so deleted rows are not handled there (let evidence rules age out stale facts).

**Calibration.** `zyntra calibrate -f PACK` (and the Insights page) backtests the model's edge weights against decisions that ran and finished. For each KPI it regresses the observed change on the contributions its incoming edges were predicted to make, anchored to the declared weight, validated leave-one-out, bounded to 0.25-4x and gated on two standard errors; it prints YAML corrections and **never applies them**. On 100 random datasets it suggested nothing for a correct model, found a 2x error every time and never changed a correct edge. It needs applied changes with an observed outcome, so a dry-run deployment has nothing to learn from, and outcomes are confounded by anything else that moved in the window.

**Fleet handoff.** Zyntra stays out of delivery. A decision with a rollout plan opens a rollout when it is finally approved. Your deployment tooling, holding `ZYNTRA_DEPLOY_TOKEN` (a role that can only read rollouts and report), posts per-site results to `POST /api/v1/rollouts/{id}/report`. The next stage may start only when every site of the previous one is healthy and its KPI health gates hold on fresh data; stale or unknown gate data fails closed. A failed site or a missed gate halts the rollout until a site is reported healthy again, the KPI recovers (`recheck`) or an approver aborts it. Every report is in the decision's audit trail. [examples/rollout/report.sh](examples/rollout/report.sh) shows the loop, and was run against a live server.

**Tenant service levels.** A KPI may carry `tenant: alpha`. A tenant account sees its own KPIs and gaps (`GET /api/v1/tenant/kpis`, no sources or owners), and any provider KPI that would appear in its views (object detail, risk, schema bindings, Ask) is replaced by the label "provider infrastructure". An edge may not join two tenants' KPIs. This removes the need for separate deployments when tenants only need their own service levels and objects; they are still needed when tenants must not share an operator or the provider's KPI graph.

### Tenants and connector credentials

**Tenant-bound accounts.** Give a local user (`tenant: alpha` in the policy file) or an OIDC user (`ZYNTRA_OIDC_TENANT_CLAIM`, required for everyone when set) a tenant and they work inside that tenant's workspace and nothing else. Only `viewer`, `proposer` and `approver` can be tenant-bound; admin and executor are deployment-wide. The tenant is part of the signed session cookie.

- **Can:** search and read their tenant's objects, history and exposure; ask about those objects; draft and create typed proposals on them; approve and reject their tenant's proposals; read their tenant's decisions and audit entries.
- **Cannot:** see the KPI model, gaps, plan, simulation, sources, inputs, policy, scenarios, other tenants' anything, the global audit chain, signed exports, Keep or the event stream. The route gate is deny-by-default, so a route added later stays closed to tenant accounts until it is listed in `internal/api/tenant.go`.
- **What they see of a proposal:** who, what, on which objects, status and approvals. KPI values, simulation, rendered payloads, execution output and alternatives are removed, and automatic audit notes (which can carry KPI detail) are blanked.
- **Objects:** an object belongs to one tenant, or to none. Tenant accounts see only their own; a policy `shared_types` rule can expose provider-owned types (a shared cluster, say) read-only. Identity-match candidates never pair objects of different tenants, and an alias held by another tenant never matches.
- **Ask:** object answers only; the model is never shown deployment-wide data.

**Connector credentials.** Replace the single shared ingest token with one credential per connector:

```bash
./bin/zyntra connector-token -name mes-alpha -tenant alpha -types Machine,Order
```

It prints the token once and a policy snippet that holds only the token's SHA-256, the tenants and object types it may write, and an expiry. Rotate by adding a second entry with the same name and setting `not_after` on the old one; revoke with `revoked: true`. A connector identity (`connector:mes-alpha`) can only call the ingest route, is checked against its own tenants and types, and appears by name in the audit chain. The old shared `ZYNTRA_INGEST_TOKEN` still works for webhook-in channels and, for ontology ingest, needs an `ingest_tenants` grant.

**Limits that remain.** An object id is global (`type:namespace:key`), so a connector gets a deliberately vague refusal if it picks an id another tenant already uses; give each tenant its own namespace. Tenant accounts that can approve a typed action cause the server to run it (dry-run by default) on shared infrastructure, so only define typed actions whose effect you are happy to delegate. The KPI graph is shared; tenants get their own KPIs (above) but not their own simulator. If tenants must not share an operator or the provider's graph at all, run separate deployments.

## Console

![Zyntra sign-in](docs/ux/login.png)

`zyntra serve` embeds a React console styled like Netra. Its pages are Overview, Gaps, Plan, Simulate, Approvals, Audit, Signals, Insights, Ask and Model, plus a timeline page for each decision, with light and dark themes. Every button, including Sign in, is apple.com blue (`#0071e3`, `#0077ed` on hover); a disabled button is grey, except the sign-in button, which stays a lighter blue.

- **Sign-in** asks for a username, then a password (or SSO, or the access key). Out of the box that is `admin` / `Admin@321`, flagged with a banner until changed.

- **Gaps and Plan** have an owner filter, remembered across pages. Plan shows pairs, ranges, actions waiting on a precondition (not proposable) and the list blocked by constraints or invariants.
- **Approvals** shows what will run: the Gravia CRD, the webhook request (method, URL, headers with `${...}` references, body), the file that would be written, or "done by people" for noop actions, plus the linked compensating action.
- **Signals** shows each source's state (healthy, stale, fallback, down), a form to enter manual KPI values with a reason, and when each webhook-in channel last received data.
- **Decision timeline** compares predicted and actual per KPI after an apply and marks each a hit or a miss.
- **Model** shows the pack, each action's kind, window and invariants, plus proposed edges (not in the model), the pack rules check and the pack-draft form.
- **Overview** has the digest with owner and close-of-window selectors; **Approvals** and the decision page show similar past decisions, the source rows behind each payload, and why a run landed or missed.

Sign in with **SSO** (OpenID Connect, authorization code with PKCE), a **local account** from the policy file (bcrypt), the **built-in admin** (`admin` / `Admin@321` until you set `ZYNTRA_ADMIN_PASSWORD`; only present when the policy file defines no users), or the **access key** (`ZYNTRA_API_KEY`, kept as break-glass admin access). Sign-in sets an HMAC session cookie carrying your name and roles (12 h, or 7 days with "remember me"). Scripts can use `Authorization: Bearer $ZYNTRA_API_KEY`.

| Role | Can |
|------|-----|
| `viewer` | Read models, plans, simulations, proposals, decisions and audit |
| `proposer` | Also create proposals |
| `approver` | Also propose, approve and reject |
| `executor` | Read, and run approved proposals through the exec endpoint |
| `admin` | Everything (the access key signs in as admin) |

With OIDC, `ZYNTRA_OIDC_ROLE_MAP` maps identity-provider groups to roles, for example `sre-leads=approver,platform=proposer,oncall=executor+viewer`. Users whose groups map to nothing are refused unless `ZYNTRA_OIDC_DEFAULT_ROLE` is set.

## AI (grounded, read-only)

The model drafts and explains. Deterministic code calculates every number, and only a named person approves a run. Every feature below returns YAML to review, a citation into the outcome store, or a payload that still waits in the inbox. With no model configured, each one returns its deterministic answer.

- **Anomalies:** z-score of each KPI against its own history.
- **Forecasts:** least-squares trend with time to breach. A forecast needs at least 6 samples spanning 10 minutes.
- **Digest, Ask and Explain:** built from gaps, plan, anomalies and source health, with the grounding facts listed. The model only rewrites the wording.
- **Shift digest:** `/ai/digest?owner=floor&window=evening` is the note for one owner at the close of a calendar window. It lists that owner's open gaps, the one action that closes the most of them, what the last approved run got wrong, and which inputs go stale before the window opens again.
- **Pack draft:** `zyntra pack draft -industry "kirana counter" -sample stock.csv -out packs/kirana` (or the form on the Model page) profiles the sample and writes `pack.yaml`, `kpis.yaml`, `sources.example.yaml`, a README and the fixture, then runs `pack validate`. The model proposes KPIs, edges and webhook or file actions as JSON; Go code checks each one and writes the YAML. An action is refused if an effect cites no column in the sample, if the URL variable is not an allowed `ZYNTRA_*_URL`, if the body references an unknown KPI, or if it decides credit, hiring, a diagnosis or anything else about a person. Drafted edges carry low confidence and a `draft:` reason until someone edits them. Without a model you get the KPIs only.
- **Payload fill:** a webhook body can use `rows:KPI` (the source rows behind a KPI) and `fill:NAME` (one column of those rows, such as `skus: "fill:sku"`). File templates get `{{range rows "stockout_rate"}}…{{end}}` and `{{fill "sku"}}`. The rows are captured when the proposal is made, shown under "Source rows behind the payload", covered by the payload hash, and reused at execution, so the approved payload is the one that runs. The dry-run prints `# rows used for stockout_rate: fixture/stock.csv rows 12, 48, 84`. When a placeholder has no column of the same name, the model may pick one from the row columns; the pick is validated, marked `model`, and the values still come from the rows.
- **Miss explainer:** once an approved run has a verdict, `/proposals/{id}/explanation` says why it landed or missed: which edge overshot (with the weight one run suggests, not applied), which effect went the wrong way, which input was stale or on a fallback source, which precondition was failing. Facts come from the outcome record only. The explanation hash is written into the audit event next to the verdict, so `audit/verify` covers it.
- **Similar past decisions:** `/proposals/{id}/similar` and `/similar?action=` find earlier proposals by KPI overlap, same action and keywords. The inbox and decision page show them as precedents ("last 2 times markdown_dead_stock regressed"). The ranking does not change.
- **Proposed edges:** `/ai/edges` looks for KPI pairs whose history moves together (at least 8 paired changes, correlation 0.7 or more) with no edge between them, and proposes one with a weight band and a YAML snippet. The edges are marked "proposed, not in the model" until you add them to `kpis.yaml`.
- **Pack rules check:** `/ai/contradictions` reads a `## Rules` section in the pack README ("never approve X", "only in window Y", "X needs two approvers", "X must be undone by Y") and checks the live plan, preconditions and policy against it. The model may read rules the parser cannot; its reading names a line and an action that both must exist.

### Air-gapped model

Set `ZYNTRA_AI_BASE_URL` to an OpenAI-compatible endpoint on your own network: the Fabric AI gateway, or Ollama (`http://ollama:11434/v1`). `ZYNTRA_AI_MODEL` defaults to `qwen2.5:7b-instruct`, and `ZYNTRA_AI_API_KEY` is only sent when set. Zyntra never picks a cloud endpoint by itself, and no plan, approval or execution needs a model. Compose has an `ai` profile with Ollama, and the Helm chart takes `ai.baseURL`, `ai.model` and `ai.apiKeySecret`.

The model does not pick, rank or run an action, does not adjust a score, cannot turn on auto-approve, and does not produce a forecast number. There is no chat box that acts.

## Approvals and execution

![Approval lane — propose, approve, execute in Keep, verify](docs/ux/readme-safety.jpg)

1. **Propose** an action (or a pair) from the plan. Zyntra captures a decision record: the input values with their freshness and source health, the model version, the full simulation with bands, the alternatives it considered, the effective policy, and the change it would make (a `GryviaPriority`, `GryviaGPUSharingPolicy` or Job suspend). Proposals that would breach a hard constraint are refused.
2. **Approve** or reject it. The policy decides how many distinct approvers are needed and whether the proposer may be one of them. Pending proposals expire (24 h by default) and approvals hold for a limited time (1 h by default).
3. **Revalidate.** Right before running, Zyntra takes a fresh snapshot and re-simulates. It blocks execution, and records why, if a required input is stale, a constraint would now be breached, the predicted improvement has fallen by more than `maxDrift` (50% by default), the model changed, the approval expired, or it is outside the action's maintenance window. Approvals outside their window wait and run when it opens.
4. **Execute:** `kubectl apply --dry-run=server` by default (`ZYNTRA_EXECUTE=apply` to apply for real).
5. **Observe.** After a real apply Zyntra watches the outcome: `verified` when the success criteria hold for enough consecutive fresh samples, `regressed` when a guardrail KPI worsens past its tolerance or a constraint breaks, `missed` when the window ends without success, `inconclusive` when the data was stale. A regression opens a linked **rollback proposal** that goes through the same approval policy (Gravia priority and MIG policies are deleted, suspended jobs resumed).

Every transition is appended to a hash-chained audit log (`GET /api/v1/audit/verify` checks it). `GET /api/v1/decisions/{id}/export` returns the decision and its audit events signed with Ed25519 (the key lives in `$ZYNTRA_STATE_DIR/decision-signing.key`); `zyntra verify-decision FILE` checks an export offline. State from v0.2 is migrated on first start.

### Policy

`zyntra serve -policy policy.yaml` (or `ZYNTRA_POLICY`) sets approval rules; see [examples/policy.yaml](examples/policy.yaml). Without a file, one approval is enough, as in v0.2.

```yaml
maintenanceWindows:
  weeknights: {days: [mon, tue, wed, thu, fri], start: "22:00", end: "06:00", timezone: Europe/Berlin}
rules:
  - name: high-risk-two-person
    match: {risk: [high]}          # also: actions, adapters
    approvals: 2
    distinctFromProposer: true
    maintenanceWindows: [weeknights]
    approvedExpiry: 12h
  - name: gravia-through-keep
    match: {adapters: [gravia]}
    keep: required                 # required | preferred (default) | off
    requireFresh: true
revalidation: {maxDrift: 0.5}
users:                             # optional local accounts
  - {name: ana, passwordHash: "$2a$12$...", roles: [approver]}
```

Later rules override earlier ones, an action's own `policy:` block overrides both, and for a pair the strictest setting of each kind wins. With `keep: required`, Zyntra blocks the proposal instead of falling back to local execution when Keep is unavailable.

With `ZYNTRA_APPROVAL_MODE=keep`, approved proposals run through **Fabric Keep**:

1. Keep starts a signed `zyntra-executor` agent in a FluxVM sandbox.
2. The agent calls Zyntra's loopback TLS exec endpoint using the brokered `zyntra-exec` credential. Keep holds the egress approval, and Zyntra decides it, so every execution has a Keep receipt and a hash-chained audit entry.
3. Rejected proposals are mirrored into Keep's audit as denied approvals.

If Keep can't start the session before an approval exists, the already-approved proposal runs locally (unless policy says `keep: required`), and the audit records the executor as `zyntra (keep unavailable)`.

```bash
zyntra keep pubkey                       # signer public key (add it to ZYVOR_AGENT_POLICY_TRUSTED_SIGNERS)
ZYNTRA_KEEP_TOKEN=... zyntra keep deploy -url http://127.0.0.1:9096   # sign + deploy; the seed never leaves this machine
zyntra keep credential                   # zyntra-exec descriptor for ZYVOR_AGENT_CREDENTIALS_FILE
```

## Configuration

| Variable | Purpose |
|----------|---------|
| `ZYNTRA_API_KEY` | Break-glass admin access key and Bearer token |
| `ZYNTRA_ADMIN_USER` / `ZYNTRA_ADMIN_PASSWORD` | Built-in local admin when the policy file has no users (default `admin` / `Admin@321`, flagged in the console until changed). `ZYNTRA_DEFAULT_ADMIN=off` removes it, and with no key, OIDC or users the console then runs open (dev only) |
| `ZYNTRA_POLICY` | Policy file (same as `serve -policy`) |
| `ZYNTRA_SESSION_SECRET` | Key for session cookies (defaults to the access key; set it so sign-ins survive key rotation and restarts) |
| `ZYNTRA_OIDC_ISSUER`, `_CLIENT_ID`, `_CLIENT_SECRET` | OpenID Connect sign-in |
| `ZYNTRA_OIDC_ROLE_MAP` | `group=role[+role],…` mapping from IdP groups to Zyntra roles |
| `ZYNTRA_OIDC_REDIRECT_URL`, `_GROUPS_CLAIM`, `_SCOPES`, `_DEFAULT_ROLE` | Optional: callback URL (default derived from the request), groups claim (`groups`), scopes, role for unmapped users |
| `ZYNTRA_LISTEN`, `ZYNTRA_STATE_DIR` | Listen address; directory for decisions, history, the decision signing key and exec TLS |
| `ZYNTRA_NETRA_URL` / `_TOKEN` | Netra API (eBPF metrics and health) |
| `ZYNTRA_GRAVIA_URL` / `_TOKEN` | Gravia API (GPU cluster, quota, costs) |
| `ZYNTRA_FABRIC_URL` / `_USER` / `_PASSWORD` or `_TOKEN` | Fabric host metrics (logs in for a token) |
| `ZYNTRA_ENDPOINT_INSECURE=1` | Accept self-signed certificates on the endpoints above (lab) |
| `ZYNTRA_EXECUTE` | `dry-run` (default) or `apply` |
| `ZYNTRA_OUTPUT_DIR` | Where `file` actions write (default `$ZYNTRA_STATE_DIR/out`) |
| `ZYNTRA_INGEST_TOKEN` | Token that may only POST to `/api/v1/ingest/<channel>` |
| `ZYNTRA_KUBECONFIG` | kubeconfig for execution and the `-kubectl` adapter |
| `ZYNTRA_APPROVAL_MODE` | `local` (default) or `keep` |
| `ZYNTRA_KEEP_URL` / `_TOKEN` | Keep agent runtime (status, sessions, approvals, audit) |
| `ZYNTRA_EXEC_TOKEN`, `ZYNTRA_EXEC_TLS_ADDR` | Token Keep injects, and the loopback TLS listener it calls |
| `ZYNTRA_AI_BASE_URL` / `_API_KEY` / `_MODEL` | Optional OpenAI-compatible model for answer rewriting |

## CLI

| Command | What it does |
|---------|--------------|
| `zyntra graph` | KPIs, targets, owners, sources, dependencies and actions |
| `zyntra gaps [-owner NAME]` | KPIs missing target, worst first |
| `zyntra simulate -action ID[+ID]` | Predicted KPI changes with ranges, constraint breaches and the propagation trace |
| `zyntra plan [-owner NAME]` | Actions and pairs ranked with confidence, then those waiting on a precondition and those blocked by constraints or invariants |
| `zyntra pack list\|validate [DIR]` | List packs under `packs/`, or check one against its fixture |
| `zyntra pack draft -industry TEXT -sample FILE [-out DIR]` | Draft a pack from CSV or JSON samples, then validate it |
| `zyntra serve [-policy FILE]` | Console, REST API and SSE pulse |
| `zyntra verify-decision FILE` | Check a signed decision export offline |
| `zyntra hash-password < pw` | bcrypt hash for a local account in the policy file |
| `zyntra keep deploy\|pubkey\|credential` | Fabric Keep executor agent |
| `zyntra fake-sources` | Fake Netra/Gravia/Fabric/Keep endpoints for development |
| `zyntra exec-token` | Random token for `ZYNTRA_EXEC_TOKEN` |

Common flags: `-f FILE|PACK_DIR`, `-o text|json`, `-prometheus URL`, `-kubectl`, `-kubeconfig FILE`. `serve` also takes `-addr`, `-interval` and `-policy`.

## API

All routes except `/healthz`, `/api/v1/meta`, sign-in and the OIDC redirects need a session cookie or a Bearer key. The role column is the minimum role.

| Method | Path | Role | Returns |
|--------|------|------|---------|
| `GET` | `/healthz` | — | `{"status":"ok"}` |
| `GET` | `/api/v1/meta` | — | Version, host, model, pack, source health, modes and sign-in methods |
| `POST`/`DELETE` | `/api/v1/session` | — | Sign in (`{operator, token}` or `{username, password}`) and out; `GET /api/v1/whoami` returns subject and roles |
| `GET` | `/api/v1/auth/oidc/login`, `/callback` | — | OIDC sign-in redirects (when configured) |
| `GET` | `/api/v1/graph`, `/gaps`, `/plan`, `/sources`, `/freshness`, `/policy` | viewer | Model with version and constraints, gaps, ranked and blocked actions, source health, per-KPI freshness, effective policy. `/gaps` and `/plan` take `?owner=` |
| `POST` | `/api/v1/simulate` | viewer | `{"action":"a"}`, `{"action":"a+b"}`, `{"actions":[…]}` or `{"custom":{...}}` |
| `GET` | `/api/v1/kpis/{id}/history` | viewer | Recorded values |
| `POST` | `/api/v1/kpis/{id}/value` | proposer | Enter a value for a `manual` KPI (`{"value":2,"reason":"..."}`), audited |
| `GET` | `/api/v1/inputs` | viewer | Manual KPIs and webhook-in channels with their last entry |
| `POST` | `/api/v1/ingest/{channel}` | ingest | JSON document for a `webhook-in` channel (ingest token or admin) |
| `GET`/`POST` | `/api/v1/ai/status`, `/digest`, `/insights`, `/ask`, `/explain` | viewer | Grounded AI (`/digest?owner=&window=` for one owner's shift note) |
| `GET` | `/api/v1/ai/edges`, `/ai/contradictions` | viewer | Proposed edges (not in the model) and pack README rules the plan breaks |
| `POST` | `/api/v1/ai/pack-draft` | proposer | `{"industry":"…","samples":[{"name":"stock.csv","content":"…"}]}` returns drafted files, refusals and validation; nothing is written |
| `GET` | `/api/v1/proposals/{id}/explanation`, `/proposals/{id}/similar`, `/similar?action=` | viewer | Why a run landed or missed (409 before a verdict), and precedents |
| `GET` | `/api/v1/proposals`, `/proposals/{id}` | viewer | Approval inbox |
| `POST` | `/api/v1/proposals` | proposer | Propose `{"action":"a"}` or `{"actions":["a","b"]}` |
| `POST` | `/api/v1/proposals/{id}/approve`, `/reject` | approver | Record an approval (202 until the quorum is met) or reject |
| `POST` | `/api/v1/exec/{id}` | executor | Revalidate and run an approved proposal (Keep's broker uses the exec token) |
| `GET` | `/api/v1/decisions`, `/decisions/{id}`, `/decisions/{id}/export` | viewer | Decision records, one with its audit events, signed export |
| `GET` | `/api/v1/audit`, `/audit/verify` | viewer | Hash-chained audit trail and its verification |
| `GET` | `/api/v1/keep/status`, `/approvals`, `/receipts`, `/audit` | viewer | Fabric Keep views (`POST /keep/approvals/{id}` needs approver) |
| `GET` | `/api/v1/events` | viewer | Server-sent `pulse` events every interval |

## Deploy

Three ways to run it, same binary inside each. Dry-run is the default everywhere; set `ZYNTRA_EXECUTE=apply` only when the targets are real and approvers are named.

### Binary on a host (systemd)

```bash
./scripts/deploy-remote.sh 212.8.248.187 sus                 # gpu pack, live Netra/Gravia/Fabric/Keep, smoke
./scripts/deploy-remote.sh 212.8.248.187 sus --pack shop     # any pack in packs/
./scripts/smoke-remote.sh                                     # re-run the smoke test against .deploy-last
./scripts/deploy-remote.sh 212.8.248.187 sus --uninstall
```

The script cross-compiles locally, validates the chosen pack, ships `packs/` and `examples/` to `/etc/zyntra`, and installs `zyntra.service` serving `/etc/zyntra/packs/<pack>`. It writes `/etc/zyntra/zyntra.env` (root:zyntra, 0640) from credentials already on the host (the Netra k8s secret, the Gravia API key, Fabric's admin password, the Keep token) and generates an ingest token; none are printed. It also adds the `zyntra-exec` credential and exec CA to Keep (after backing up Keep's env file) and signs the executor agent on your workstation. Options: `--pack`, `--port`, `--exec-port`, `--no-keep`, `--skip-web`, `--dry-run`, `--skip-smoke`. This is the install to use for kubectl actions and the Kubernetes adapters.

The smoke test logs in, posts any missing manual values, checks sources, gaps, plan and AI, then proposes and approves one action. It prefers an action without a maintenance window; if the action it picks is held for its window, that counts as a pass and it says so.

On the lab host, Keep sandboxes cannot reach the egress broker, so approved actions run locally and the audit records the executor as `zyntra (keep unavailable)`. The smoke test shows this line on purpose; set `keep: required` in the policy to block instead.

### Container (Docker or Podman)

Published images: `ghcr.io/zyvorai/zyntra` for linux/amd64 and linux/arm64. `:edge` and `:0.4.0-dev` track `main`, `:sha-<commit>` pins a build, and release tags add `:<version>`, `:<major>.<minor>` and `:latest`. Each image carries an SBOM, SLSA provenance and a keyless cosign signature:

```bash
docker pull ghcr.io/zyvorai/zyntra:edge
cosign verify ghcr.io/zyvorai/zyntra:edge \
  --certificate-identity-regexp 'https://github.com/zyvorai/zyntra/.github/workflows/image.yml@.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify oci://ghcr.io/zyvorai/zyntra:edge -R zyvorai/zyntra
```

For an air-gapped site, `docker save` the pinned image and load it on the inside; nothing in it calls out.

```bash
make docker                                          # zyntra:<version>, docker or podman
docker run --rm -p 8080:8080 -e ZYNTRA_API_KEY=dev ghcr.io/zyvorai/zyntra:edge                 # shop pack
docker run --rm -p 8080:8080 -e ZYNTRA_API_KEY=dev ghcr.io/zyvorai/zyntra:edge serve -f packs/gpu
ZYNTRA_API_KEY=$(openssl rand -hex 24) docker compose up --build                    # shop + test receiver
```

The image is distroless, runs as uid 65532, and keeps state (proposals, audit chain, inputs, file actions) in the `/var/lib/zyntra` volume, so a read-only root filesystem works. It contains `zyntra`, `zyntra-receiver`, `packs/` and `examples/`. It does **not** contain kubectl: kubectl actions and the Netra/Gravia Kubernetes adapters need the binary install. Packs with file, http, sheet, webhook-in and manual sources and webhook, file or noop actions work as is. To run your own pack, mount it and point `-f` at it (`-v $PWD/packs/mine:/app/packs/mine:ro ... serve -f packs/mine`).

[docker-compose.yml](docker-compose.yml) runs the shop pack with the receiver standing in for the POS and ERP (`http://127.0.0.1:9099`), both containers read-only with all capabilities dropped. Add `ZYNTRA_EXECUTE=apply` to watch an approved markdown arrive in the inbox.

### Kubernetes (Helm or plain manifest)

```bash
helm upgrade --install zyntra oci://ghcr.io/zyvorai/charts/zyntra --version 0.4.0 \
  -n zyntra --create-namespace --set pack=shop            # image ghcr.io/zyvorai/zyntra:0.4.0-dev
# or from a checkout: helm upgrade --install zyntra deploy/helm/zyntra ... --set image.repository=registry.internal/zyntra
kubectl -n zyntra port-forward svc/zyntra 8080:8080
kubectl -n zyntra get secret zyntra-auth -o jsonpath='{.data.ZYNTRA_API_KEY}' | base64 -d
```

The chart ([deploy/helm/zyntra](deploy/helm/zyntra/values.yaml)) runs one replica with a `Recreate` strategy, because proposals and the audit chain are file state with a single writer. It generates the API key, session secret, ingest token and exec token once and keeps them across upgrades, or uses `auth.existingSecret`. It also provides: a PVC for state (kept on uninstall), probes on `/healthz`, a non-root pod with a read-only root filesystem and no service account token, inline `policy`, extra `env`, optional Ingress and NetworkPolicy, and an optional test receiver (`receiver.enabled`). `modelPath` together with `extraVolumes` serves a pack from a ConfigMap or volume instead of the image.

Without Helm, use the rendered [deploy/kubernetes/zyntra.yaml](deploy/kubernetes/zyntra.yaml); its header shows the one `kubectl create secret` it needs. Regenerate it with `make k8s-manifest`; CI fails if it drifts from the chart.

For a k3s host with no registry, `deploy-k8s.sh` builds the image there with podman, imports it into containerd, installs the chart on a NodePort and runs the smoke test:

```bash
./scripts/deploy-k8s.sh 212.8.248.187 sus --pack shop     # http://212.8.248.187:30962
make deploy-k8s HOST=212.8.248.187 PACK=shop
```

Options: `--namespace`, `--node-port`, `--execute dry-run|apply`, `--no-receiver`, `--no-smoke`. The image tag is `<version>-<git sha>`, so each deploy rolls the pod.

## Where it fits in Zyvor

- **Netra:** eBPF network signals (retransmits, drops, latency, datapath health)
- **Gravia:** GPU utilisation, queue and cost; executes priority, MIG-sharing and job-suspend CRDs
- **Fabric:** host metrics, the AI gateway, and Keep for sandboxed, audited execution
- **Kairo / KubeFlight:** deploy blast radius, to be fed in as a risk input

Outside Zyvor, a pack needs nothing but files: generic sources read exports and APIs, and webhook or file actions hand the approved change to the system that already owns it (ERP, POS, ticketing).

## Develop

```bash
make check      # gofmt, vet, unit tests, build
make test-e2e   # CLI + API + console smoke test against fake sources
zyntra pack validate packs/shop   # check a pack against its fixture
cd web && ZYNTRA_DEV_API=http://127.0.0.1:8080 npm run dev   # console with hot reload
make docker     # container image (docker or podman)
make helm-lint  # lint the chart and render it with every option on
```

See [SECURITY.md](SECURITY.md) and [CONTRIBUTING.md](CONTRIBUTING.md). Social and README images are rebuilt from HTML; see [docs/social](docs/social/README.md).

## Editions and pricing

Every capability in this repository stays in **Community**, which is free for non-production use. Production use needs a commercial license. Subscriptions are priced by **managed clusters and KPI graphs**, with unlimited users and approvers.

| Edition | Annual price | Capacity | Support |
|---|---|---|---|
| Community | Free (non-production) | Unlimited non-production clusters | Community |
| Enterprise Essentials | INR 9 lakh / USD 11k | 2 clusters · 5 KPI graphs | 8×5, next business day |
| **Enterprise** | **INR 24 lakh / USD 29k** | **10 clusters · 25 KPI graphs** | **24×7, P1 in 1 hour** |
| Enterprise Scale | INR 48 lakh / USD 58k | 40 clusters · 100 KPI graphs | 24×7, P1 in 30 min + TAM |
| Sovereign / MSP | INR 1 crore / USD 120k+ | Custom | Mission-critical |

Enterprise adds multi-cluster decisions, OIDC/SAML and RBAC, approval policy (change windows, two-person rule, risk-bounded auto-approve), durable decision history, learned edge weights, managed connectors, and certified delivery. A 90-day paid pilot (INR 4–8 lakh) is credited to year one.

[Full pricing sheets](docs/sales/enterprise-pricing.md) · [Pricing PDF](docs/sales/Zyvor-Zyntra-Enterprise-Pricing.pdf) · [Buyer resources](docs/sales/README.md)

## License

Licensed under the **[Zyvor Production License v1.0](LICENSE)**.

- **Free** for development, testing, evaluation, research, education, and non-production labs
- **Paid commercial license required** for production, customer workloads, SaaS, managed services, OEM, redistribution, and other revenue-generating use

Commercial terms are issued separately: [https://zyvor.dev](https://zyvor.dev?utm_source=github&utm_medium=zyntra&utm_campaign=readme_footer).

**Next step:** [Book a demo](https://zyvor.dev/schedule?utm_source=github&utm_medium=zyntra&utm_campaign=readme_footer) · [30-day PoC](https://zyvor.dev/poc?utm_source=github&utm_medium=zyntra&utm_campaign=readme_footer) · [sales@zyvor.dev](mailto:sales@zyvor.dev)
