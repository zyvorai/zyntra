#!/usr/bin/env bash
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0
# deploy-k8s.sh — Build the image on a k3s host and install the Helm chart
# ============================================================================
# No registry needed: the image is built with podman (or docker) on the host,
# imported into k3s containerd, and installed with Helm. Then the smoke test
# runs against the NodePort.
#
# Usage:
#   ./scripts/deploy-k8s.sh HOST [USER] [options]
#
# Options:
#   --pack NAME        pack to serve (default: shop)
#   --namespace NS     namespace (default: zyntra)
#   --node-port N      NodePort for the console (default: 30962)
#   --execute MODE     dry-run|apply (default: dry-run)
#   --no-receiver      do not run the test webhook receiver
#   --no-smoke         skip the smoke test
#
# Requires on the host: podman or docker, k3s, helm, passwordless sudo.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
HOST="" USER_="sus" PACK="shop" NS="zyntra" NODE_PORT=30962 EXECUTE="dry-run" RECEIVER=true SMOKE=true
pos=()
while [ $# -gt 0 ]; do
  case "$1" in
    --pack) PACK="$2"; shift 2 ;;
    --namespace) NS="$2"; shift 2 ;;
    --node-port) NODE_PORT="$2"; shift 2 ;;
    --execute) EXECUTE="$2"; shift 2 ;;
    --no-receiver) RECEIVER=false; shift ;;
    --no-smoke) SMOKE=false; shift ;;
    -h|--help) sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*) echo "unknown option: $1" >&2; exit 2 ;;
    *) pos+=("$1"); shift ;;
  esac
done
[ ${#pos[@]} -ge 1 ] || { sed -n '10,12p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }
HOST="${pos[0]}"; [ ${#pos[@]} -ge 2 ] && USER_="${pos[1]}"
[[ "$PACK" =~ ^[a-z0-9-]+$ && -f "$ROOT/packs/$PACK/pack.yaml" ]] || { echo "no such pack: $PACK" >&2; exit 2; }
[[ "$NS" =~ ^[a-z0-9-]+$ ]] || { echo "bad namespace: $NS" >&2; exit 2; }
[[ "$NODE_PORT" =~ ^[0-9]+$ ]] || { echo "bad node port: $NODE_PORT" >&2; exit 2; }
case "$EXECUTE" in dry-run|apply) ;; *) echo "--execute must be dry-run or apply" >&2; exit 2 ;; esac

VERSION="$(sed -n 's/^var version = "\(.*\)"/\1/p' "$ROOT/cmd/zyntra/main.go")"
SHA="$(git -C "$ROOT" rev-parse --short HEAD)"
git -C "$ROOT" diff --quiet HEAD -- . 2>/dev/null || SHA="${SHA}-dirty"
TAG="${VERSION}-${SHA}"
SSH=(ssh -o BatchMode=yes -o ConnectTimeout=15 "${USER_}@${HOST}")
STAGE="/tmp/zyntra-k8s-build"
KCFG="/etc/rancher/k3s/k3s.yaml"

step() { printf '\n▶ %s\n' "$*"; }

step "Checking pack and chart locally"
(cd "$ROOT" && go run ./cmd/zyntra pack validate "packs/$PACK" >/dev/null)
helm lint "$ROOT/deploy/helm/zyntra" >/dev/null

step "Shipping build context to ${USER_}@${HOST}"
COPYFILE_DISABLE=1 tar -C "$ROOT" --no-xattrs --exclude web/node_modules --exclude web/dist --exclude '._*' -czf - \
  Dockerfile .dockerignore go.mod go.sum cmd internal keep examples packs web deploy/helm \
  | "${SSH[@]}" "rm -rf $STAGE && mkdir -p $STAGE && tar -xzf - -C $STAGE"

step "Building zyntra:${TAG} on the host and importing it into k3s"
"${SSH[@]}" bash -s -- "$STAGE" "$TAG" "$VERSION" <<'REMOTE'
set -euo pipefail
stage="$1" tag="$2" version="$3"
cli=podman; command -v podman >/dev/null || cli=docker
cd "$stage"
$cli build -q --build-arg VERSION="$version" -t "localhost/zyntra:$tag" . >/dev/null
$cli save "localhost/zyntra:$tag" | sudo k3s ctr images import - >/dev/null
echo "imported localhost/zyntra:$tag"
REMOTE

step "Installing the chart in namespace ${NS}"
set_args="image.repository=localhost/zyntra,image.tag=${TAG},pack=${PACK},execute=${EXECUTE}"
set_args+=",service.type=NodePort,service.nodePort=${NODE_PORT},receiver.enabled=${RECEIVER}"
if $RECEIVER; then
  set_args+=",env.ZYNTRA_POS_URL=http://zyntra-receiver:9099/pos,env.ZYNTRA_ERP_URL=http://zyntra-receiver:9099/erp"
fi
"${SSH[@]}" "sudo helm --kubeconfig $KCFG upgrade --install zyntra $STAGE/deploy/helm/zyntra \
  --namespace $NS --create-namespace --wait --timeout 3m --set '$set_args'" | sed -n '1,6p'

KEY="$("${SSH[@]}" "sudo k3s kubectl -n $NS get secret zyntra-auth -o jsonpath='{.data.ZYNTRA_API_KEY}' | base64 -d")"
URL="http://${HOST}:${NODE_PORT}"
printf '\n  Console  %s\n  Pack     packs/%s\n  Image    localhost/zyntra:%s\n  Execute  %s\n' "$URL" "$PACK" "$TAG" "$EXECUTE"
printf '  API key  ssh %s@%s sudo k3s kubectl -n %s get secret zyntra-auth -o jsonpath={.data.ZYNTRA_API_KEY} | base64 -d\n' "$USER_" "$HOST" "$NS"

if $SMOKE; then
  step "Smoke test"
  if curl -fsS -m 5 "$URL/healthz" >/dev/null 2>&1; then
    ZYNTRA_URL="$URL" ZYNTRA_API_KEY="$KEY" "$ROOT/scripts/smoke-remote.sh"
  else
    echo "NodePort not reachable from here; checking from the host"
    printf 'Authorization: Bearer %s' "$KEY" | "${SSH[@]}" \
      "curl -fsS -m 5 http://127.0.0.1:${NODE_PORT}/healthz && curl -fsS -m 5 -H @- http://127.0.0.1:${NODE_PORT}/api/v1/meta"
    echo
  fi
fi
