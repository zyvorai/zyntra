#!/usr/bin/env bash
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
#
# Builds nothing; expects bin/zyntra (run via `make test-e2e`).
set -euo pipefail
cd "$(dirname "$0")/.."

BIN=./bin/zyntra
MODEL=examples/kpis.yaml
PORT="${ZYNTRA_E2E_PORT:-18080}"
FAKE="${ZYNTRA_E2E_FAKE_PORT:-18090}"
KEY=e2e-key
STATE=$(mktemp -d)

fail() { echo "FAIL: $*" >&2; [ -f /tmp/zyntra-e2e.log ] && tail -20 /tmp/zyntra-e2e.log >&2; exit 1; }

# expect NAME PATTERN CMD...: run CMD fully, then match its output.
expect() {
  local name=$1 pattern=$2 out
  shift 2
  out=$("$@") || fail "$name: command exited $?"
  grep -q -- "$pattern" <<<"$out" || fail "$name: no match for $pattern"
}

expect version '^zyntra ' $BIN version
expect graph 'gpu-cluster-prod: 6 KPIs, 5 edges, 4 actions' $BIN graph -f "$MODEL"
expect gaps 'queue_wait_minutes' $BIN gaps -f "$MODEL"
expect simulate 'Closes: queue_wait_minutes, p99_inference_latency, slo_availability' \
  $BIN simulate -f "$MODEL" -action preempt_batch_to_spot
expect "plan json" '"status": "pending-approval"' $BIN plan -f "$MODEL" -o json
expect "plan ranking" '^1 *preempt_batch_to_spot' $BIN plan -f "$MODEL"
if $BIN simulate -f "$MODEL" -action does_not_exist 2>/dev/null; then fail "unknown action should error"; fi
expect "keep credential" '"requires_approval": \["POST"\]' $BIN keep credential

# Live lab model against fake Netra/Gravia/Fabric/Keep endpoints.
$BIN fake-sources -addr "127.0.0.1:$FAKE" >/tmp/zyntra-e2e-fake.log 2>&1 &
FPID=$!
export ZYNTRA_NETRA_URL=http://127.0.0.1:$FAKE ZYNTRA_GRAVIA_URL=http://127.0.0.1:$FAKE \
  ZYNTRA_FABRIC_URL=http://127.0.0.1:$FAKE ZYNTRA_FABRIC_PASSWORD=fake ZYNTRA_KEEP_URL=http://127.0.0.1:$FAKE \
  ZYNTRA_API_KEY=$KEY ZYNTRA_EXEC_TOKEN=e2e-exec ZYNTRA_STATE_DIR=$STATE ZYNTRA_EXECUTE=dry-run
ZYNTRA_KEEP_URL='' $BIN serve -f examples/lab-kpis.yaml -addr "127.0.0.1:$PORT" -interval 1s >/tmp/zyntra-e2e.log 2>&1 &
PID=$!
trap 'kill $PID $FPID 2>/dev/null || true; rm -rf "$STATE"' EXIT
for _ in $(seq 1 50); do
  curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && break
  sleep 0.1
done
sleep 2.5 # two refreshes so counter rates exist

auth=(-H "Authorization: Bearer $KEY")
api() { curl -fsS "${auth[@]}" "$@"; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

expect meta '"auth_required": true' curl -fsS "http://127.0.0.1:$PORT/api/v1/meta"
[ "$(code "http://127.0.0.1:$PORT/api/v1/gaps")" = 401 ] || fail "gaps without auth should be 401"
[ "$(code -H 'Authorization: Bearer wrong' "http://127.0.0.1:$PORT/api/v1/gaps")" = 401 ] || fail "wrong key should be 401"
expect "session login" '"ok": *true' curl -fsS -c "$STATE/jar" -H 'Content-Type: application/json' \
  -d "{\"token\":\"$KEY\",\"operator\":\"e2e\"}" "http://127.0.0.1:$PORT/api/v1/session"
expect "cookie whoami" '"subject":"e2e"' curl -fsS -b "$STATE/jar" "http://127.0.0.1:$PORT/api/v1/whoami"
expect "api gaps" 'ebpf_health_score' api "http://127.0.0.1:$PORT/api/v1/gaps"
expect "sources" '"name": "netra"' api "http://127.0.0.1:$PORT/api/v1/sources"
srcs=$(api "http://127.0.0.1:$PORT/api/v1/sources")
grep -q '"ok": false' <<<"$srcs" && fail "a fake source is unhealthy: $srcs"
expect "live gravia value" '"v": 3' api "http://127.0.0.1:$PORT/api/v1/kpis/gravia_pending_jobs/history"
expect "ai digest" '"intent": "digest"' api "http://127.0.0.1:$PORT/api/v1/ai/digest"
expect "ai ask" '"intent": "plan"' api -X POST -H 'Content-Type: application/json' \
  -d '{"question":"what should we do first?"}' "http://127.0.0.1:$PORT/api/v1/ai/ask"

prop=$(api -X POST -H 'Content-Type: application/json' -d '{"action":"raise-inference-priority"}' \
  "http://127.0.0.1:$PORT/api/v1/proposals")
grep -q 'kind: GryviaPriority' <<<"$prop" || fail "proposal render: $prop"
id=$(sed -n 's/^  "id": "\([^"]*\)".*/\1/p' <<<"$prop" | head -1)
[ -n "$id" ] || fail "no proposal id"
[ "$(code -X POST -H 'Authorization: Bearer e2e-exec' "http://127.0.0.1:$PORT/api/v1/exec/$id")" = 409 ] \
  || fail "exec before approval should be 409"
# kubectl may be absent here; either way the decision must be recorded.
out=$(api -X POST -H 'Content-Type: application/json' -d '{"reason":"e2e"}' \
  "http://127.0.0.1:$PORT/api/v1/proposals/$id/approve")
grep -Eq '"status": "(executed|failed)"' <<<"$out" || fail "approve: $out"
grep -q -- '--dry-run=server' <<<"$out" || fail "execution must be dry-run: $out"
expect audit '"to": "approved"' api "http://127.0.0.1:$PORT/api/v1/audit"

expect console '<div id="root">' curl -fsS "http://127.0.0.1:$PORT/"
expect "spa fallback" '<div id="root">' curl -fsS "http://127.0.0.1:$PORT/approvals"
sse=$(curl -sS -N --max-time 2 "${auth[@]}" "http://127.0.0.1:$PORT/api/v1/events" 2>/dev/null || true)
grep -q 'event: pulse' <<<"$sse" || fail "sse pulse"

echo "e2e OK"
