<!-- Copyright 2026 Zyvor AI Labs · https://zyvor.dev · SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0 -->

# Business ontology

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
- **Limits.** Objects and links are held in memory (about 1.4 KB each, capped by `ZYNTRA_ONTOLOGY_MAX_OBJECTS`). Entity resolution is deterministic aliases plus a human review queue, not ML. The rollout shape is exported in the signed decision and delivery stays in your deployment tooling. There is one KPI graph per deployment (tenants get their own KPIs, not their own simulator). See *Tenants and connector credentials* below for exactly what tenant isolation covers.

## Storage, connectors, calibration and fleet handoff

**Storage.** The ontology store has two backends. The default is one JSON file, rewritten as a whole on each write, which is fine for a few thousand objects.

`ZYNTRA_ONTOLOGY_STORE=sqlite` keeps objects, links, identity candidates and the change log in `ontology.db` (pure-Go SQLite, so `CGO_ENABLED=0` builds still work): writes touch only what changed, one transaction per ingest batch, and an object's history is read from disk. Lookups by alias, link and identity match are indexed. On the benchmark, ingesting 5,000 objects takes about 83 ms on SQLite and 23 ms batched on JSON, against 35 s before batching.

Helm: `ontology.store`, `ontology.maxObjects`, `auth.deployToken` and `connectors.exec` set the matching variables (size `resources.limits.memory` for the object cap: about 150 MB per 100,000 objects). 

Move an existing install with `zyntra ontology migrate -f PACK -from state/ontology.json -to state/ontology.db`; an existing `ontology.db` is used even when the variable is unset.

Objects and links are held in memory, at about **1.4 KB per object** with a link (measured: 100,000 objects retain 143 MB; 1,000,000 would be about 1.4 GB, and the process held 114-150 MB with 12.6k objects loaded on a live host). SQLite removes the write cost and the file-size limit, not that footprint, so a cap stops a runaway source from exhausting the host: `ZYNTRA_ONTOLOGY_MAX_OBJECTS` (default 1,000,000; 0 for none). A batch that would take the store past the cap is refused whole, before anything is written, with an error that says how many objects it holds, how many the batch adds and what to change; updating objects that already exist always fits.

`GET /api/v1/ontology/stats` (approver) and a line on the Objects page show objects, links and the cap, with a warning from 80%.

Listing is paged by cursor so it stays fast at that size: `GET /api/v1/ontology/objects?limit=200&after=<next>&type=&q=` returns up to `limit` (at most 1,000) visible objects in id order plus a `next` cursor (an id, not an offset, so objects added or removed between requests never repeat or skip one); a page of 200 over a 200,000-object store takes about 4 microseconds. Workflow views return at most 500 rows, those needing attention first, with the true total. The console pages with a Load more button.

The risk summary and the assistant's exposure answers read only objects that a KPI measures (an index of type- and per-object bindings), so their cost follows the number of bound objects, not the store size: a summary over 150,000 objects with 20 bound takes about 20 microseconds. The one remaining whole-store read is the assistant looking for an object named in a question, which pages through visible objects and stops at 50 matches; a full pass over 150,000 objects is about 35 ms.

Proposals remain a JSON file, but the audit chain is its own append-only file (`approvals.json.audit.jsonl`, one event per line, synced on write): a save costs one line instead of rewriting the history, and the bytes already written are never touched. An older state with the trail inline is moved over on first start with the chain and head hash unchanged (verified on a live host), and a crash that leaves a half-written last line is trimmed on start while damage anywhere else is refused as tampering. After upgrading, an older binary cannot read the new state: keep the `approvals.json` backup if you may need to roll back.

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

The SQL connector accepts one `SELECT` (or `WITH ... SELECT`), runs it in a read-only transaction (a data-modifying CTE is refused by the database, tested against a real Postgres) and keeps the connection string out of every error. Failures back off up to 8x the interval and the wait counts from the end of a run; a connector never blocks another, and each one's last success, error, counts and cursor survive a restart. The **Sources** card on the Objects page shows health (and flags a connector that spends more than half its interval running); `POST /api/v1/ontology/connectors/{name}/run` (admin) runs one now. A fact read from a file is dated by the file's modification time, not the read time, so re-reading an old file does not make it look fresh; a typed action's `evidence:` rule (`max_age: 10m`) then blocks a proposal on stale facts until a refresh brings new ones. [examples/ontology/gpu-live.yaml](../examples/ontology/gpu-live.yaml) is a live ontology for the gpu pack.

**In a pod, no `kubectl` needed.** The container image has no `kubectl`, so Kubernetes connectors use a built-in client when Zyntra runs in a pod with no kubeconfig set: it reads the cluster through the pod's service account (token re-read on every request, so rotation works), over TLS with the cluster CA. It reads a fixed set of resources (nodes, pods, namespaces, services, endpoints, configmaps, persistent volumes and claims, service accounts, events, deployments, statefulsets, daemonsets, replicasets, jobs, cronjobs, ingresses, network policies) and **never secrets**: a pack that names `secrets` is refused in every mode. With Helm, `kubernetes.inCluster=true` creates a read-only ClusterRole (`get` and `list` on only the resources in `kubernetes.resources`) and binds it to the service account; the default install can read nothing from the cluster. A missing grant produces the API's own `forbidden` message plus what to set. Outside a pod, or with `ZYNTRA_KUBECONFIG` set, `kubectl` is used as before. Verified against a real k3s cluster twice: with a throwaway service account allowed nodes only (nodes read, pods forbidden with guidance, secrets refused), and with Zyntra running as a pod under the chart's own rendered role (no `kubectl` in the container; nodes and pods read, 12 objects and 11 links built, `configmaps`, which the role does not grant, refused with guidance). `make test-incluster` repeats that second check on whatever cluster your kubectl points at (it needs kubectl, helm and Go, and removes its one throwaway namespace, ClusterRole and binding on exit). Approved `gravia.*` actions work the same way: with no `kubectl` on the path in a pod, the executor sends the change to the API with the service account (server-side apply with field manager `zyntra`, or a merge patch, or a delete only of objects labelled `app.kubernetes.io/managed-by=zyntra`; `dryRun=All` while `ZYNTRA_EXECUTE` is `dry-run`) and refuses any other kind; `kubernetes.actions=true` grants those writes and nothing else. When you apply the chart's templates by hand, pass `-n <namespace>`: they carry no namespace of their own, so a rendered ServiceAccount otherwise lands in the current one.

Kubernetes specifics, from running it against a real k3s cluster of 12,622 pods: read an array element by key, not position (`status.conditions.[type=Ready].status`; a numeric index read the wrong condition), narrow the listing at the source with `k8s_namespace`, `k8s_selector` or `k8s_field_selector`, and set `prune: true` on a full listing so objects deleted in the cluster leave the ontology (only objects that this connector alone vouches for are removed, an empty or all-failed listing never prunes, and the removal is in the audit note). Output is streamed one item at a time (Zyntra held about 150 MB with 12.6k objects loaded) and capped at 512 MiB, but `kubectl get pods -A -o json` itself took 14 s and 1.5 GB of RAM on that cluster, which no connector can avoid: select what you need. A SQL query with no `$1`/`?` parameter is a full listing, takes no argument and may set `prune: true`; a query that filters on the last-run time returns changes only and cannot see deleted rows by itself, so add `reconcile_query`: a parameterless read-only listing of the rows that still exist (at least the identifying columns), run every `reconcile_every` (default 24h, at least 1m, and once after each restart). Objects that only this connector vouches for and that the listing no longer names are removed and the removal is audited; an empty listing never prunes.

**Calibration.** `zyntra calibrate -f PACK` (and the Insights page) backtests the model's edge weights against decisions that ran and finished. For each KPI it regresses the observed change on the contributions its incoming edges were predicted to make, anchored to the declared weight, validated leave-one-out, bounded to 0.25-4x and gated on two standard errors; it prints YAML corrections and **never applies them**. On 100 random datasets it suggested nothing for a correct model, found a 2x error every time and never changed a correct edge. It needs applied changes with an observed outcome, so a dry-run deployment has nothing to learn from, and outcomes are confounded by anything else that moved in the window.

**Fleet handoff.** Zyntra stays out of delivery. A decision with a rollout plan opens a rollout when it is finally approved. Your deployment tooling, holding `ZYNTRA_DEPLOY_TOKEN` (a role that can only read rollouts and report), posts per-site results to `POST /api/v1/rollouts/{id}/report`. The next stage may start only when every site of the previous one is healthy and its KPI health gates hold on fresh data; stale or unknown gate data fails closed. A failed site or a missed gate halts the rollout until a site is reported healthy again, the KPI recovers (`recheck`) or an approver aborts it. Every report is in the decision's audit trail. [examples/rollout/report.sh](../examples/rollout/report.sh) shows the loop, and was run against a live server.

**Tenant service levels.** A KPI may carry `tenant: alpha`. A tenant account sees its own KPIs and gaps (`GET /api/v1/tenant/kpis`, no sources or owners), and any provider KPI that would appear in its views (object detail, risk, schema bindings, Ask) is replaced by the label "provider infrastructure". An edge may not join two tenants' KPIs. This removes the need for separate deployments when tenants only need their own service levels and objects; they are still needed when tenants must not share an operator or the provider's KPI graph.

## Tenants and connector credentials

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

**Service tokens.** An agent or script that reads objects and drafts proposals gets its own token instead of the admin key:

```bash
./bin/zyntra service-token -name gryvia-agent-helper -roles viewer,proposer -tenant alpha
```

It prints the token once and a `service_tokens:` policy snippet with the token's SHA-256, its roles, an optional tenant and an expiry; rotate and revoke as for connector credentials. Only `viewer` and `proposer` are allowed, so a service can never approve, reject, execute or administer: its proposals wait for a named person like anyone else's. The identity is `service:<name>` in proposals and the audit chain, and a `tenant` confines it exactly as it does a tenant-bound user. Typed actions whose `permissions` name a higher role stay out of its reach.

**Limits that remain.** An object id is global (`type:namespace:key`), so a connector gets a deliberately vague refusal if it picks an id another tenant already uses; give each tenant its own namespace. Tenant accounts that can approve a typed action cause the server to run it (dry-run by default) on shared infrastructure, so only define typed actions whose effect you are happy to delegate. The KPI graph is shared; tenants get their own KPIs (above) but not their own simulator. If tenants must not share an operator or the provider's graph at all, run separate deployments.
