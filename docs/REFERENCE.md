# Zyntra reference

The technical reference for Zyntra: the model file, sources, packs, the business ontology, the console, approvals and execution, policy, configuration, the CLI and API, deployment and development. For what Zyntra is and why teams use it, start with the [README](../README.md).

Related: [Business ontology](ONTOLOGY.md) · [Product plan](PRODUCT_PLAN.md) · [Security](../SECURITY.md) · [Changelog](../CHANGELOG.md)

## Contents

- [What to know before you rely on it](#what-to-know-before-you-rely-on-it)
- [The model](#the-model)
- [Packs (any industry)](#packs-any-industry)
- [Business ontology](#business-ontology)
- [Console](#console)
- [AI (grounded, read-only)](#ai-grounded-read-only)
- [Approvals and execution](#approvals-and-execution)
- [Configuration](#configuration)
- [CLI](#cli)
- [API](#api)
- [Deploy](#deploy)
- [Develop](#develop)

## What to know before you rely on it

- **The simulator is a model, not a measurement.** It is a deterministic model over the edge weights and effects you supply. Its ranges come from the uncertainty you declare, not from data. `zyntra calibrate` backtests the weights against decisions that ran and suggests corrections, but never applies them.
- **The newer packs carry declared starting weights**, not measured ones. Calibrate them against your own outcomes before you lean on the ranking.
- **Changes execute only after human approval**, and in dry-run by default.
- **Sources only read.** The AI layer explains and forecasts; it never picks or runs an action.
- **Release state.** The changelog lists what is released and what is still unreleased; read it before you pin a version.

## The model

A model file ([examples/kpis.yaml](../examples/kpis.yaml)) has three parts:

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
- **Preconditions and invariants.** An action whose precondition does not hold (or whose input is stale) is ranked after the approvable ones with status `precondition-failed` and cannot be proposed. An action whose simulation worsens an invariant KPI by more than `max_worsen`, at the nominal or pessimistic end, is blocked like a constraint breach.

### Model reference (core)

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

### Model reference (packs and ontology additions)

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
    adapter: webhook                  # webhook | file | noop, or a core adapter with execute
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
| `kind` | `file`, `http`, `sheet`, `webhook-in`, `manual`, or a core kind (`prometheus`, `kubernetes`, `metrics`, `json`, `netra`, `gravia`, `fabric`, `keep`) |
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

### Live sources (Netra, Gryvia, Fabric, Keep)

`kind: metrics` scrapes Prometheus text (Netra's `/metrics`). `kind: json` reads a field from a JSON endpoint; `kind: netra`, `gravia`, `fabric` and `keep` (the `gravia` kind reads Gryvia) are shorthands that also pick the endpoint. Zyntra ships no eBPF code of its own; it consumes the Netra agent.

```yaml
- id: tcp_retransmits_per_s
  source: {kind: metrics, endpoint: netra, metric: netra_tcp_retransmissions, agg: sum, rate: true}
- id: ebpf_health_score
  source: {kind: netra, path: /api/v1/ebpf/health, field: summary.healthScore}
- id: root_disk_percent
  source: {kind: fabric, path: /api/v1/system/info, field: "filesystems.#(mountpoint=/).usage_percent"}
```

Fields support `a.b.0`, `list.#` (count), `list.#(k=v)` (count matches), `list.#(k=v).f` (field of the first match) and `list.*.f`, plus `scale`, `agg: sum|avg|max|min` and `rate`. See [packs/gpu](../packs/gpu) for a full lab model (20 KPIs) that uses every v0.3 field.

## Packs (any industry)

The engine knows nothing about GPUs or shops. A **pack** is a directory of files: `pack.yaml` (id, owners, timezone, calendars), `kpis.yaml`, `sources.example.yaml`, a README and a `fixture/` of sample exports. [packs/shop](../packs/shop) runs a shop from CSV exports with no Kubernetes in the loop:

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

**Test inbox.** `examples/receiver` (built as `bin/zyntra-receiver`) accepts webhook deliveries, stores one JSON file per delivery, answers a repeated `Idempotency-Key` with `200 {"duplicate":true}` instead of recording it twice, and redacts `Authorization`, `Cookie` and `X-Api-Key`. Point a pack's URLs at it during a pilot (`make run-shop` does) and open `http://127.0.0.1:9099` to see what landed. Shipped packs: [shop](../packs/shop), [gpu](../packs/gpu) (the lab model), [manufacturing](../packs/manufacturing), [logistics](../packs/logistics), [payments](../packs/payments) and [imaging-ops](../packs/imaging-ops) (capacity and flow only: no patient data, no clinical decisions). The newer ones are starting points: their weights are declared, not measured. See [docs/PRODUCT_PLAN.md](PRODUCT_PLAN.md) for the pack catalog and build order.

## Business ontology

The KPI graph says how numbers move. The ontology says which business things sit behind them: which orders depend on an inspection service, which customers on a cluster. A pack adds an `ontology.yaml` next to `kpis.yaml` (objects, links, how CSV or live sources map to them, typed actions, workflow views):

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
./bin/zyntra calibrate -f packs/gpu                                       # backtest edge weights; suggests, never applies
```

- **Separate from the simulator.** Links never carry numbers; an object points at the KPIs that measure it, and only declared KPI edges predict.
- **Provenance and permissions on every fact.** Source, observed time and transforms travel with each value; object types, tenants, properties and typed actions are enforced in retrieval, so an answer cannot cite what you could not open.
- **Typed actions** take objects as inputs and are re-checked right before execution; a changed fact or stale evidence blocks the run.
- **Live sources.** Files, Kubernetes (in a pod with no `kubectl`, or via `kubectl`), SQL, any paged REST API, and a stdlib Python connector, on a scheduler with backoff and health.
- **Scale.** JSON or SQLite store, paged listing, an indexed risk summary and an object cap; about 1.4 KB per object in memory.
- **Tenants.** Tenant-bound accounts see only their own objects, KPIs, proposals and audit entries, behind a deny-by-default route gate; connectors get one credential each.
- **Fleet handoff.** Rollouts with KPI health gates for your deployment tooling to report against; Zyntra never deploys.

The full reference (storage and capacity numbers, connectors, calibration, rollouts, tenant isolation and what it does not cover) is in **[docs/ONTOLOGY.md](ONTOLOGY.md)**.

## Console

![Zyntra sign-in](ux/login.png)

`zyntra serve` embeds a React console styled like Netra. Its pages are grouped as **Overview**; **Decide** (Gaps, Plan, Simulate); **Act** (Approvals, Audit); **Business** (Objects, Workflows, Scenarios, and Service levels for tenant accounts); **Signals**; **Intelligence** (Insights, Ask); and **Model**, plus a timeline page for each decision. There are light and dark themes. The Business group appears when the pack defines an ontology.

- **Sign-in** asks for a username, then a password (or SSO, or the access key). Out of the box that is `admin` / `Admin@321`, flagged with a banner until changed.

- **Gaps and Plan** have an owner filter, remembered across pages. Plan shows pairs, ranges, actions waiting on a precondition (not proposable) and the list blocked by constraints or invariants.
- **Approvals** shows what will run: the Gryvia CRD, the webhook request (method, URL, headers with `${...}` references, body), the file that would be written, or "done by people" for noop actions, plus the linked compensating action.
- **Signals** shows each source's state (healthy, stale, fallback, down), a form to enter manual KPI values with a reason, and when each webhook-in channel last received data.
- **Objects** lists the business objects, their links, the source and time behind every fact, and the objects that depend on the one you select. Uncertain same-thing matches wait for a person to merge or reject them.
- **Workflows** shows which objects are exposed to a failing KPI and the typed actions you can propose for them.
- **Scenarios** saves a plan with its assumptions, runs it against current data and compares plans side by side. Running one never changes anything.
- **Service levels** (tenant accounts) shows the tenant's own KPIs against their targets.
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

#### Service tokens

Agents and scripts should not hold the admin key. `zyntra service-token -name N -roles viewer[,proposer] [-tenant T] [-days N]` prints a bearer token once and a hash for the `service_tokens:` list in the policy file. A service token can carry the `viewer` and `proposer` roles, an optional tenant and an expiry. The `approver`, `executor` and `admin` roles are refused, so a service can propose a change but never approve it. With Helm, `policyExistingSecret` reads the policy from a Secret so a parent chart can generate the token and write its hash. See [ONTOLOGY.md](ONTOLOGY.md) for tenant scoping.

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

![Approval lane — propose, approve, execute in Keep, verify](ux/readme-safety.jpg)

1. **Propose** an action (or a pair) from the plan. Zyntra captures a decision record: the input values with their freshness and source health, the model version, the full simulation with bands, the alternatives it considered, the effective policy, and the change it would make (a `GryviaPriority`, `GryviaGPUSharingPolicy` or Job suspend). Proposals that would breach a hard constraint are refused.
2. **Approve** or reject it. The policy decides how many distinct approvers are needed and whether the proposer may be one of them. Pending proposals expire (24 h by default) and approvals hold for a limited time (1 h by default).
3. **Revalidate.** Right before running, Zyntra takes a fresh snapshot and re-simulates. It blocks execution, and records why, if a required input is stale, a constraint would now be breached, the predicted improvement has fallen by more than `maxDrift` (50% by default), the model changed, the approval expired, or it is outside the action's maintenance window. Approvals outside their window wait and run when it opens.
4. **Execute:** `kubectl apply --dry-run=server` by default (`ZYNTRA_EXECUTE=apply` to apply for real).
5. **Observe.** After a real apply Zyntra watches the outcome: `verified` when the success criteria hold for enough consecutive fresh samples, `regressed` when a guardrail KPI worsens past its tolerance or a constraint breaks, `missed` when the window ends without success, `inconclusive` when the data was stale. A regression opens a linked **rollback proposal** that goes through the same approval policy (Gravia priority and MIG policies are deleted, suspended jobs resumed).

Every transition is appended to a hash-chained audit log (`GET /api/v1/audit/verify` checks it). `GET /api/v1/decisions/{id}/export` returns the decision and its audit events signed with Ed25519 (the key lives in `$ZYNTRA_STATE_DIR/decision-signing.key`); `zyntra verify-decision FILE` checks an export offline. State from v0.2 is migrated on first start.

### Policy

`zyntra serve -policy policy.yaml` (or `ZYNTRA_POLICY`) sets approval rules; see [examples/policy.yaml](../examples/policy.yaml). Without a file, one approval is enough, as in v0.2.

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
| `ZYNTRA_GRAVIA_URL` / `_TOKEN` | Gryvia API (GPU cluster, quota, costs) |
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
| `zyntra fake-sources` | Fake Netra/Gryvia/Fabric/Keep endpoints for development |
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
./scripts/deploy-remote.sh 212.8.248.187 sus                 # gpu pack, live Netra/Gryvia/Fabric/Keep, smoke
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

The image is distroless, runs as uid 65532, and keeps state (proposals, audit chain, inputs, file actions) in the `/var/lib/zyntra` volume, so a read-only root filesystem works. It contains `zyntra`, `zyntra-receiver`, `packs/` and `examples/`. It does **not** contain kubectl. In a pod, Kubernetes connectors read through the service account, and approved `gravia.*` actions run through the Kubernetes API instead (server-side apply of Gryvia priorities and GPU sharing policies, a merge patch on Gryvia AI jobs, a delete only of objects labelled `app.kubernetes.io/managed-by=zyntra`); Helm `kubernetes.actions=true` grants exactly those writes. Outside a pod, kubectl actions and the Netra/Gravia Kubernetes adapters need the binary install. Packs with file, http, sheet, webhook-in and manual sources and webhook, file or noop actions work as is. To run your own pack, mount it and point `-f` at it (`-v $PWD/packs/mine:/app/packs/mine:ro ... serve -f packs/mine`).

[docker-compose.yml](../docker-compose.yml) runs the shop pack with the receiver standing in for the POS and ERP (`http://127.0.0.1:9099`), both containers read-only with all capabilities dropped. Add `ZYNTRA_EXECUTE=apply` to watch an approved markdown arrive in the inbox.

### Kubernetes (Helm or plain manifest)

```bash
helm upgrade --install zyntra oci://ghcr.io/zyvorai/charts/zyntra --version 0.4.0 \
  -n zyntra --create-namespace --set pack=shop            # image ghcr.io/zyvorai/zyntra:0.4.0-dev
# or from a checkout: helm upgrade --install zyntra deploy/helm/zyntra ... --set image.repository=registry.internal/zyntra
kubectl -n zyntra port-forward svc/zyntra 8080:8080
kubectl -n zyntra get secret zyntra-auth -o jsonpath='{.data.ZYNTRA_API_KEY}' | base64 -d
```

The chart ([deploy/helm/zyntra](../deploy/helm/zyntra/values.yaml)) runs one replica with a `Recreate` strategy, because proposals and the audit chain are file state with a single writer. It generates the API key, session secret, ingest token and exec token once and keeps them across upgrades, or uses `auth.existingSecret`. It also provides: a PVC for state (kept on uninstall), probes on `/healthz`, a non-root pod with a read-only root filesystem and no service account token, inline `policy` (or `policyExistingSecret`, a Secret's `policy.yaml`, for a policy with generated service-token hashes), extra `env`, optional Ingress and NetworkPolicy, and an optional test receiver (`receiver.enabled`). `modelPath` together with `extraVolumes` serves a pack from a ConfigMap or volume instead of the image.

Without Helm, use the rendered [deploy/kubernetes/zyntra.yaml](../deploy/kubernetes/zyntra.yaml); its header shows the one `kubectl create secret` it needs. Regenerate it with `make k8s-manifest`; CI fails if it drifts from the chart.

For a k3s host with no registry, `deploy-k8s.sh` builds the image there with podman, imports it into containerd, installs the chart on a NodePort and runs the smoke test:

```bash
./scripts/deploy-k8s.sh 212.8.248.187 sus --pack shop     # http://212.8.248.187:30962
make deploy-k8s HOST=212.8.248.187 PACK=shop
```

Options: `--namespace`, `--node-port`, `--execute dry-run|apply`, `--no-receiver`, `--no-smoke`. The image tag is `<version>-<git sha>`, so each deploy rolls the pod.


## Develop

```bash
make check      # gofmt, vet, unit tests, build
make test-e2e   # CLI + API + console smoke test against fake sources
zyntra pack validate packs/shop   # check a pack against its fixture
cd web && ZYNTRA_DEV_API=http://127.0.0.1:8080 npm run dev   # console with hot reload
make docker     # container image (docker or podman)
make helm-lint  # lint the chart and render it with every option on
```

See [SECURITY.md](../SECURITY.md) and [CONTRIBUTING.md](../CONTRIBUTING.md). Social and README images are rebuilt from HTML; see [docs/social](social/README.md).

