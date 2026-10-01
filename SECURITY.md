# Security policy

Zyntra reads infrastructure metrics and cluster inventory. KPI models and snapshots describe capacity, cost and weak points of your environment — treat them as confidential infrastructure data.

## Safe deployment defaults

- Zyntra v0.1 is read-only. It never mutates clusters; every recommended action is returned with `status: pending-approval` and nothing executes it.
- Give the Prometheus and Kubernetes adapters read-only credentials. The Kubernetes adapter only runs `kubectl get nodes`.
- Run the API on a private network until authentication and authorization are configured for your environment.
- Terminate TLS at a trusted ingress or reverse proxy.
- The server limits request bodies to 1 MiB.

## Reporting vulnerabilities

Please report suspected vulnerabilities privately to the project maintainers rather than opening a public issue with exploit details.
