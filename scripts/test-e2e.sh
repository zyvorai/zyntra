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

fail() { echo "FAIL: $*" >&2; exit 1; }

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

$BIN serve -f "$MODEL" -addr "127.0.0.1:$PORT" -interval 1s >/tmp/zyntra-e2e.log 2>&1 &
PID=$!
trap 'kill $PID 2>/dev/null || true' EXIT
for _ in $(seq 1 50); do
  curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && break
  sleep 0.1
done
expect "api gaps" '"severity_total"' curl -fsS "http://127.0.0.1:$PORT/api/gaps"
expect "api simulate" '"action": "enable_mig"' curl -fsS -X POST -H 'Content-Type: application/json' \
  -d '{"action":"enable_mig"}' "http://127.0.0.1:$PORT/api/simulate"
expect dashboard 'Decision pulse' curl -fsS "http://127.0.0.1:$PORT/"
sse=$(curl -sS -N --max-time 2 "http://127.0.0.1:$PORT/api/events" 2>/dev/null || true)
grep -q 'event: pulse' <<<"$sse" || fail "sse pulse"

echo "e2e OK"
