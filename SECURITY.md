# Security policy

Zyntra reads infrastructure metrics and cluster inventory, and with approval it changes Gravia scheduling objects. KPI models, decision records and snapshots describe the capacity, cost and weak points of your environment, so treat them as confidential infrastructure data.

## Safe deployment defaults

- **Nothing runs without approval.** Every proposal needs at least one human approval. Execution is `kubectl --dry-run=server` unless `ZYNTRA_EXECUTE=apply` is set.
- **Configure sign-in.** With no `ZYNTRA_API_KEY`, OIDC or local users, the API and console are open; this is for development only and Zyntra logs a warning.
  - Prefer OIDC (`ZYNTRA_OIDC_*`). It uses the authorization code flow with PKCE, a nonce, and a signed, short-lived state cookie. Map groups to the narrowest role that works (`viewer`, `proposer`, `approver`, `executor`, `admin`).
  - The access key signs in as `admin`. Keep it as break-glass access, store it like a root credential and rotate it. Set `ZYNTRA_SESSION_SECRET` so rotating the key does not invalidate sessions, and so sessions survive restarts when you run without a key.
  - Local accounts in the policy file store bcrypt hashes only (`zyntra hash-password`). Unknown users take as long to reject as wrong passwords.
- **Sessions** are HMAC-signed HttpOnly cookies carrying the subject, roles and expiry (12 h, or 7 days with "remember me"). They are `SameSite=Lax` so the OIDC redirect can complete; every state-changing route is a JSON `POST`, which browsers do not send cross-site with Lax cookies. Cookies are marked `Secure` behind TLS (directly or via `X-Forwarded-Proto: https`).
- **Separation of duties.** Use the policy file to require several approvers for risky actions (`approvals: 2`, `distinctFromProposer: true`), maintenance windows, fresh inputs, and `keep: required` so changes never bypass Fabric Keep.
- **Re-checks before execution.** An approved proposal is re-simulated on fresh data right before it runs. Stale required inputs, constraint breaches, drift past `maxDrift`, a changed model, an expired approval or a closed maintenance window block it, and the reason is recorded.
- **Tamper evidence.** The audit log in `$ZYNTRA_STATE_DIR/approvals.json` is hash-chained; `GET /api/v1/audit/verify` reports the first altered or missing entry. Exports are signed with an Ed25519 key in `$ZYNTRA_STATE_DIR/decision-signing.key` (0600). Back up the state directory and protect it with file permissions; the chain detects edits but does not prevent someone with write access from deleting the whole file.
- **Least privilege for adapters and execution.** Give the Prometheus and Kubernetes adapters read-only credentials; the Kubernetes adapter only runs `kubectl get nodes`. The execution kubeconfig needs only the Gravia CRDs Zyntra manages (`gryviapriorities`, `gryviagpusharingpolicies`, and `patch` on `gryviaaijobs`). Rollbacks delete only objects labelled `app.kubernetes.io/managed-by=zyntra`.
- Terminate TLS at a trusted ingress or reverse proxy. The Keep exec listener uses its own loopback TLS certificate and a separate exec token that can only call the execution endpoint.
- The server limits request bodies to 1 MiB.

## Reporting vulnerabilities

Please report suspected vulnerabilities privately to the project maintainers rather than opening a public issue with exploit details.
