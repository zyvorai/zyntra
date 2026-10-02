# Changelog

## v0.3.0 — 2026-10-02

Zyntra becomes a decision engine: it ranks safe changes, gets them approved under policy, re-checks them right before they run, verifies the outcome, and keeps a signed record of every decision.

### Added

- **Richer model.** KPI `criticality`, `min`/`max` and `freshness` (`maxAge`, `required`). Edge `confidence`, `provenance` and `delay`. Action effect `uncertainty`, `delay` and `saturation`. Hard `constraints` (`ceiling`, `floor`, `mustNotWorsen`), plus per-action `outcome`, `rollback` and `policy`. Existing v0.2 models load unchanged.
- **Planner.** Criticality-weighted scoring, prediction ranges, a confidence score per candidate, combined actions (pairs), and a "Blocked (hard constraints)" list. `zyntra simulate a+b` runs several actions together.
- **Per-KPI freshness.** Every KPI shows its source, age and whether it is usable; stale required inputs block execution.
- **Approval policy** (`-policy` / `ZYNTRA_POLICY`, see `examples/policy.yaml`): approval quorum, approvers distinct from the proposer, approver roles, expiry, maintenance windows, required fresh inputs, Fabric Keep `required`/`preferred`/`off`, and the revalidation drift limit.
- **Revalidation before execution.** Approved proposals are re-simulated on fresh data. A closed window waits; drift, constraint breaches, stale inputs, a changed model or expiry block the proposal with a recorded reason.
- **Outcome verification.** After an apply, Zyntra samples the target and guardrail KPIs, records a verdict (`improved`, `no-effect`, `regressed`) and opens a linked rollback proposal on regression.
- **Decision records.** Each proposal stores the inputs, model version, simulation, alternatives, policy, approvals, execution and outcome. The audit log is hash-chained (`GET /api/v1/audit/verify`), and `GET /api/v1/decisions/{id}/export` returns an Ed25519-signed record; `zyntra verify-decision FILE` checks it offline.
- **Roles and SSO.** Roles `viewer`, `proposer`, `approver`, `executor` and `admin` gate every route. Sign in with OIDC (PKCE, group-to-role mapping), local users with bcrypt hashes (`zyntra hash-password`), or the access key as break-glass admin. `ZYNTRA_SESSION_SECRET` keeps sessions stable across key rotations and restarts.
- **Console.** Decision timeline page with audit hashes and signed export, quorum and expiry in Approvals, confidence and ranges in Plan, chain status in Audit, and SSO and password sign-in.
- Rollback templates for Gravia priority and GPU sharing objects. They delete only objects labelled `app.kubernetes.io/managed-by=zyntra`.

### Changed

- Proposals that would breach a hard constraint are refused (HTTP 422).
- Approval state moves to schema version 2; v0.2 state files migrate automatically.
- Session cookies now carry the subject and roles and use `SameSite=Lax` so OIDC redirects complete.
- New dependencies for authentication only: `github.com/coreos/go-oidc/v3`, `golang.org/x/oauth2` and `golang.org/x/crypto`.

## v0.2.0 — 2026-10-02

### License

- **Zyntra is now licensed under the [Zyvor Production License v1.0](LICENSE)** (SPDX `LicenseRef-Zyvor-Production-1.0`), replacing Apache-2.0. It is free for development, testing, evaluation, research, education and non-production labs. Production and other revenue-generating use needs a commercial license. Releases tagged before v0.2.0 stay available under Apache-2.0.
- Editions, pricing, and buyer resources: [docs/sales](docs/sales/README.md). Every capability in this repository stays in Community.

### Added

- Web console (React 19, embedded in the binary) styled like Netra, with Fabric's two-step sign-in, HMAC session cookies, and light and dark themes. Pages: Overview, Gaps, Plan, Simulate, Approvals, Audit, Signals, Insights, Ask and Model.
- Live sources: Netra (eBPF metrics and health), Gravia (GPU queue, quota and cost), Fabric (host metrics) and Fabric Keep, through `kind: metrics`, `json`, `netra`, `gravia`, `fabric` and `keep`.
- Grounded, read-only AI: anomalies, forecasts with time to breach, digest, Ask and Explain, with an optional OpenAI-compatible model to rewrite answers.
- Approval inbox. Approved actions render Gravia CRDs and run with `kubectl --dry-run=server` by default, then show predicted vs actual.
- Fabric Keep execution (`ZYNTRA_APPROVAL_MODE=keep`): a signed `zyntra-executor` agent in a FluxVM sandbox, brokered credentials, receipts and a hash-chained audit.
- `scripts/deploy-remote.sh` and `scripts/smoke-remote.sh` for systemd installs. Credentials are read on the host and never printed.
- Share card, social card, README cards and console screenshots (`docs/social`, `docs/ux`).

### Fixed

- The Ask page went blank in current Chromium after a question was asked: its scroll effect returned the Promise from `scrollIntoView` as a React cleanup function.

## v0.1.0

- KPI graph model, gaps, explainable what-if simulation and a ranked plan. CLI with Prometheus and Kubernetes adapters.
