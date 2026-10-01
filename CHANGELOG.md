# Changelog

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
