#!/usr/bin/env bash
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0
#
# Runs Zyntra as a pod and checks that its Kubernetes connectors read the
# cluster it runs in through the service account, with no kubectl in the
# container, using the read-only role the Helm chart renders.
#
#   make test-incluster                     # against the current kubectl context
#
# It needs a cluster that can pull busybox, kubectl, helm and Go. Everything it
# creates lives in one throwaway namespace plus a ClusterRole and binding, and is
# removed on exit (including on failure). It reads nodes and the running pods of
# kube-system, and checks that a resource the chart does not grant is refused.
#
# Overrides, for hosts without helm or a repo checkout:
#   KUBECTL     kubectl command (default "kubectl")
#   ZYNTRA_BIN  a linux zyntra binary for the cluster's architecture
#   RBAC_FILE   the chart's rendered extras.yaml (default: helm template)
#   PACK_DIR    a pack directory holding pack.yaml, kpis.yaml and ontology.yaml
set -euo pipefail
cd "$(dirname "$0")/.."

KUBECTL=${KUBECTL:-kubectl}
NS=${NS:-zyntra-e2e-$$}
KEY=incluster-test-key-not-secret
WORK=$(mktemp -d)
K() { $KUBECTL "$@"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok   $*"; }

cleanup() {
  K delete namespace "$NS" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  K delete clusterrolebinding "$NS-zyntra-reader" --ignore-not-found >/dev/null 2>&1 || true
  K delete clusterrole "$NS-zyntra-reader" --ignore-not-found >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

arch=$(K get nodes -o jsonpath='{.items[0].status.nodeInfo.architecture}')
BIN=${ZYNTRA_BIN:-}
if [ -z "$BIN" ]; then
  BIN=$WORK/zyntra
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -o "$BIN" ./cmd/zyntra
fi

PACK=${PACK_DIR:-$WORK/pack}
if [ -z "${PACK_DIR:-}" ]; then
  mkdir -p "$PACK"
  cp packs/gpu/pack.yaml packs/gpu/kpis.yaml packs/gpu/sources.example.yaml "$PACK"/
  sed -i.bak 's/^id: gpu$/id: incluster/' "$PACK/pack.yaml" && rm -f "$PACK/pack.yaml.bak"
  cat >"$PACK/ontology.yaml" <<'YAML'
name: incluster
objects:
  - name: Node
    properties: [{name: name, type: string, required: true}, {name: ready, type: string}]
  - name: Workload
    properties: [{name: name, type: string, required: true}, {name: phase, type: string}]
  - name: Config
    properties: [{name: name, type: string}]
links:
  - {name: runs_on, from: Workload, to: Node}
connectors:
  - name: nodes
    kind: kubernetes
    resource: nodes
    prune: true
    interval: 30s
    fields: {name: metadata.name, ready: 'status.conditions.[type=Ready].status'}
    mapping: {type: Node, namespace: k, key: name, props: {name: name, ready: ready}}
  - name: pods
    kind: kubernetes
    resource: pods
    k8s_namespace: kube-system
    k8s_field_selector: status.phase=Running
    prune: true
    interval: 30s
    fields: {uid: metadata.uid, name: metadata.name, phase: status.phase, node: spec.nodeName}
    mapping:
      type: Workload
      namespace: k
      key: uid
      props: {name: name, phase: phase}
      links: [{type: runs_on, column: node, to: Node}]
  - name: configmaps          # not in the chart's default role: must be refused
    kind: kubernetes
    resource: configmaps
    k8s_namespace: kube-system
    interval: 30s
    fields: {name: metadata.name}
    mapping: {type: Config, namespace: k, key: name, props: {name: name}}
YAML
fi

RBAC=${RBAC_FILE:-$WORK/rbac.yaml}
if [ -z "${RBAC_FILE:-}" ]; then
  helm template "$NS" deploy/helm/zyntra --namespace "$NS" \
    --set kubernetes.inCluster=true,persistence.enabled=false -s templates/extras.yaml >"$RBAC"
fi
SA=$NS-zyntra

K create namespace "$NS" >/dev/null
# Every apply names the namespace: the chart's templates carry none, so without
# -n a rendered ServiceAccount would land in the current one.
K apply -n "$NS" -f "$RBAC" >/dev/null
[ "$(K get sa -n default --no-headers 2>/dev/null | grep -c "^$SA ")" = 0 ] || fail "the service account leaked into the default namespace"

cat >"$WORK/pod.yaml" <<YAML
apiVersion: v1
kind: Pod
metadata: {name: zyntra, namespace: $NS}
spec:
  serviceAccountName: $SA
  automountServiceAccountToken: true
  restartPolicy: Never
  securityContext: {runAsUser: 65532, runAsGroup: 65532, runAsNonRoot: true}
  containers:
    - name: zyntra
      image: busybox:1.36
      command: ["sh", "-c", "sleep 3600"]
      env:
        - {name: ZYNTRA_STATE_DIR, value: /work/state}
        - {name: ZYNTRA_API_KEY, value: $KEY}
        - {name: ZYNTRA_ONTOLOGY_STORE, value: sqlite}
      volumeMounts: [{name: work, mountPath: /work}]
  volumes: [{name: work, emptyDir: {}}]
YAML
for i in 1 2 3 4 5 6; do K apply -n "$NS" -f "$WORK/pod.yaml" >/dev/null 2>&1 && break; sleep 3; done
K -n "$NS" wait --for=condition=Ready pod/zyntra --timeout=180s >/dev/null || fail "the pod did not become ready"

K -n "$NS" exec zyntra -- mkdir -p /work/state
K -n "$NS" cp "$BIN" zyntra:/work/zyntra
K -n "$NS" exec zyntra -- chmod +x /work/zyntra
K -n "$NS" cp "$PACK" zyntra:/work/pack
K -n "$NS" exec zyntra -- sh -c '/work/zyntra serve -addr :8080 -f /work/pack -interval 30s >/work/log 2>&1 &'

K -n "$NS" exec zyntra -- sh -c 'command -v kubectl' >/dev/null 2>&1 && fail "kubectl exists in the pod, so this would not prove the built-in client"
ok "the pod has no kubectl"

api() { K -n "$NS" exec zyntra -- wget -qO- --header "Authorization: Bearer $KEY" "http://127.0.0.1:8080$1"; }
out=""
for _ in $(seq 1 40); do
  out=$(api /api/v1/ontology/connectors 2>/dev/null || true)
  n=$(python3 -c 'import sys,json; print(sum(1 for c in json.load(sys.stdin)["connectors"] if c["runs"]>0))' <<<"$out" 2>/dev/null || echo 0)
  [ "$n" = 3 ] && break
  sleep 3
done
[ "${n:-0}" = 3 ] || { K -n "$NS" exec zyntra -- cat /work/log >&2 || true; fail "the connectors did not all run: $out"; }

python3 - "$out" <<'PY' || exit 1
import json, sys
c = {x["name"]: x for x in json.loads(sys.argv[1])["connectors"]}
def need(cond, msg):
    if not cond:
        print("FAIL: " + msg, file=sys.stderr); sys.exit(1)
need(c["nodes"]["healthy"] and c["nodes"]["last_objects"] >= 1, "nodes: %r" % c["nodes"])
need(c["pods"]["healthy"] and c["pods"]["last_objects"] >= 1, "pods: %r" % c["pods"])
err = c["configmaps"].get("last_error", "")
need(not c["configmaps"]["healthy"] and "forbidden" in err and "kubernetes.inCluster=true" in err, "configmaps should be refused with guidance, got %r" % err)
print("  ok   nodes and pods read through the service account (%d node(s), %d pod(s))" % (c["nodes"]["last_objects"], c["pods"]["last_objects"]))
print("  ok   configmaps, not granted by the chart's role, was refused with guidance")
PY
stats=$(api /api/v1/ontology/stats)
python3 -c 'import sys,json; s=json.load(sys.stdin); assert s["objects"]>=2 and s["links"]>=1, s; print("  ok   %d objects and %d links built from the live cluster" % (s["objects"], s["links"]))' <<<"$stats" || fail "stats: $stats"
echo "in-cluster OK"
