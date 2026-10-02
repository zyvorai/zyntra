# GPU cluster pack (pack zero)

The Zyvor lab model: Netra eBPF datapath signals, Gravia GPU scheduling, Fabric host metrics and Keep readiness, joined to service KPIs. This is the pack the lab host runs.

## The three gaps this pack is judged on

| Gap | Owner | KPI | Target |
| --- | --- | --- | --- |
| Inference waits for GPUs | ml-platform | `gpu_queue_wait_min` | at most 10 min |
| The service is slow | ml-serving | `service_latency_p99_ms` | at most 200 ms |
| The datapath is unhealthy | network | `ebpf_health_score` | at least 80 |

Availability (`app_availability`, critical) and host memory (ceiling 90%) are hard constraints: no action that would breach them is ranked.

## Try it

Against the declared values (no live sources):

```sh
zyntra pack validate packs/gpu
zyntra plan -f packs/gpu
zyntra simulate -f packs/gpu -action raise-inference-priority
```

Against look-alike Netra, Gravia, Fabric and Keep endpoints:

```sh
make run-lab        # fake sources + console on http://127.0.0.1:8080, key "dev"
```

One simulate trace:

```text
$ zyntra simulate -f packs/gpu -action raise-inference-priority

KPI                     BEFORE  AFTER   RANGE           CHANGE  TARGET
service_latency_p99_ms  185     172.51  164.39..179.47  -6.8%   met
app_availability        99.95   99.96   99.96..99.97    +0.01%  met
gpu_queue_wait_min      12      7.8     6.54..9.06      -35.0%  met (closed)

Why:
  raise-inference-priority changes gpu_queue_wait_min by -35.0% (direct effect) ±30%, after 5m0s
  raise-inference-priority changes service_latency_p99_ms by -5.0% (direct effect) ±50%
  gpu_queue_wait_min -35.0% -> service_latency_p99_ms -1.7% (weight +0.05 ±60%) after 5m0s [learned, assumed relationship]: inference replicas wait for GPUs
  service_latency_p99_ms -6.8% -> app_availability +0.01% (weight -0.002): timeouts past the client deadline

Settles after: 10m0s
Closes: gpu_queue_wait_min
```

The dry-run then prints the `GryviaPriority` object it would apply.

## Actions

| Action | Runs as | Notes |
| --- | --- | --- |
| `raise-inference-priority` | kubectl: `GryviaPriority` | Rollback deletes the object; outcome judged over 20 minutes |
| `enable-mig-sharing` | kubectl: `GryviaGPUSharingPolicy` | Rollback deletes the policy |
| `tune-tcp-buffers` | advisory (Netra) | Recorded decision; people apply it |
| `investigate-connect-failures` | advisory (Netra) | Recorded decision |
| `rebalance-host` | advisory (Fabric), high risk | Two approvers through policy |

## Binding

The netra, gravia, fabric and keep source kinds read their endpoints from `ZYNTRA_NETRA_URL`, `ZYNTRA_GRAVIA_URL`, `ZYNTRA_FABRIC_URL` and `ZYNTRA_KEEP_URL` (with their tokens); `scripts/deploy-remote.sh` finds them on a Zyvor host. Execution needs `kubectl` and a kubeconfig allowed to manage the Gravia CRDs, so this pack's actions run from the binary install, not from the distroless container image.
