#!/usr/bin/env bash
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0
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
expect "plan ranking" '^1 *add_gpu_nodes+preempt_batch_to_spot' $BIN plan -f "$MODEL"
if $BIN simulate -f "$MODEL" -action does_not_exist 2>/dev/null; then fail "unknown action should error"; fi
expect "keep credential" '"requires_approval": \["POST"\]' $BIN keep credential

# Live lab model against fake Netra/Gravia/Fabric/Keep endpoints.
$BIN fake-sources -addr "127.0.0.1:$FAKE" >/tmp/zyntra-e2e-fake.log 2>&1 &
FPID=$!
export ZYNTRA_NETRA_URL=http://127.0.0.1:$FAKE ZYNTRA_GRAVIA_URL=http://127.0.0.1:$FAKE \
  ZYNTRA_FABRIC_URL=http://127.0.0.1:$FAKE ZYNTRA_FABRIC_PASSWORD=fake ZYNTRA_KEEP_URL=http://127.0.0.1:$FAKE \
  ZYNTRA_API_KEY=$KEY ZYNTRA_EXEC_TOKEN=e2e-exec ZYNTRA_STATE_DIR=$STATE ZYNTRA_EXECUTE=dry-run
$BIN serve -f packs/gpu -addr "127.0.0.1:$PORT" -interval 1s >/tmp/zyntra-e2e.log 2>&1 &
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
expect "audit chain" '"ok": true' api "http://127.0.0.1:$PORT/api/v1/audit/verify"
api "http://127.0.0.1:$PORT/api/v1/decisions/$id/export" >"$STATE/decision.json" || fail "decision export"
expect "verify decision" 'audit chain at export: intact' $BIN verify-decision "$STATE/decision.json"

expect console '<div id="root">' curl -fsS "http://127.0.0.1:$PORT/"
expect "spa fallback" '<div id="root">' curl -fsS "http://127.0.0.1:$PORT/approvals"
sse=$(curl -sS -N --max-time 2 "${auth[@]}" "http://127.0.0.1:$PORT/api/v1/events" 2>/dev/null || true)
grep -q 'event: pulse' <<<"$sse" || fail "sse pulse"

# Shop pack: CSV fixtures, no Kubernetes. Webhooks go to the test receiver and
# file actions are written for real (apply) into a scratch directory.
expect "pack list" 'shop' $BIN pack list
expect "pack validate shop" 'shop (packs/shop): ok' $BIN pack validate packs/shop
expect "pack validate gpu" 'gpu (packs/gpu): ok' $BIN pack validate packs/gpu
expect "shop plan closes stockout" 'reorder_fast_movers' $BIN plan -f packs/shop
expect "shop invariant blocks markdown" 'markdown_dead_stock' $BIN plan -f packs/shop -o json
$BIN plan -f packs/shop -o json 2>/dev/null | python3 -c '
import json, sys
d = json.load(sys.stdin)
assert any(b["action"] == "markdown_dead_stock" for b in d["blocked"]), "not blocked"
assert not any(r["action"] == "markdown_dead_stock" for r in d["recommendations"]), "still ranked"
' || fail "markdown_dead_stock must be blocked by the margin invariant"
expect "shop dry-run PO" 'Purchase order: fast movers below cover' $BIN simulate -f packs/shop -action reorder_fast_movers
expect "shop owner filter" 'queue_wait' $BIN gaps -f packs/shop -owner floor

SPORT=$((PORT + 2))
RPORT=$((PORT + 3))
SSTATE=$(mktemp -d)
./bin/zyntra-receiver -addr "127.0.0.1:$RPORT" -dir "$SSTATE/inbox" >/tmp/zyntra-e2e-receiver.log 2>&1 &
RPID=$!
env -u ZYNTRA_NETRA_URL -u ZYNTRA_GRAVIA_URL -u ZYNTRA_FABRIC_URL -u ZYNTRA_KEEP_URL \
  ZYNTRA_STATE_DIR="$SSTATE" ZYNTRA_OUTPUT_DIR="$SSTATE/out" ZYNTRA_EXECUTE=apply \
  ZYNTRA_INGEST_TOKEN=e2e-ingest ZYNTRA_POS_URL="http://127.0.0.1:$RPORT/pos" ZYNTRA_ERP_URL="http://127.0.0.1:$RPORT/erp" \
  $BIN serve -f packs/shop -addr "127.0.0.1:$SPORT" -interval 1s >/tmp/zyntra-e2e-shop.log 2>&1 &
SPID=$!
trap 'kill $PID $FPID $SPID $RPID 2>/dev/null || true; rm -rf "$STATE" "$SSTATE"' EXIT
for _ in $(seq 1 50); do
  curl -fsS "http://127.0.0.1:$SPORT/healthz" >/dev/null 2>&1 && curl -fsS "http://127.0.0.1:$RPORT/healthz" >/dev/null 2>&1 && break
  sleep 0.1
done
S="http://127.0.0.1:$SPORT/api/v1"
json=(-H 'Content-Type: application/json')
expect "shop meta pack" '"id": "shop"' curl -fsS "$S/meta"
expect "shop file sources" '"state": "ok"' api "$S/sources"
expect "manual value" '"kpi": "cashiers_open"' api -X POST "${json[@]}" -d '{"value":1,"reason":"e2e"}' "$S/kpis/cashiers_open/value"
expect "manual audited" 'manual value cashiers_open' api "$S/audit"
[ "$(code -X POST "${json[@]}" -d '{"value":1}' "${auth[@]}" "$S/kpis/stockout_rate/value")" = 400 ] || fail "non-manual KPI must refuse a value"
[ "$(code -H 'Authorization: Bearer e2e-ingest' "$S/gaps")" = 403 ] || fail "ingest token must not read gaps"
[ "$(code -X POST "${json[@]}" -d '{}' -H 'Authorization: Bearer e2e-ingest' "$S/ingest/nope")" = 404 ] || fail "unknown ingest channel must be 404"
expect "owner filter api" '"owner": "floor"' api "$S/gaps?owner=floor"

wh=$(api -X POST "${json[@]}" -d '{"action":"markdown_capped"}' "$S/proposals")
grep -q '"webhook"' <<<"$wh" || fail "webhook proposal kinds: $wh"
grep -q '${ZYNTRA_POS_URL}' <<<"$wh" || fail "rendered webhook must keep the variable reference: $wh"
wid=$(sed -n 's/^  "id": "\([^"]*\)".*/\1/p' <<<"$wh" | head -1)
out=$(api -X POST "${json[@]}" -d '{"reason":"e2e"}' "$S/proposals/$wid/approve")
grep -q '"status": "executed"' <<<"$out" || fail "webhook approve: $out"
grep -q '"response_hash"' <<<"$out" || fail "webhook response hash: $out"
expect "receiver got the markdown" "\"idempotency_key\":\"$wid\"" curl -fsS "http://127.0.0.1:$RPORT/"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Idempotency-Key: $wid" -d '{}' "http://127.0.0.1:$RPORT/pos/markdowns")
[ "$code" = 200 ] || fail "receiver must acknowledge a repeated key with 200, got $code"

fp=$(api -X POST "${json[@]}" -d '{"action":"drop_slow_supplier"}' "$S/proposals")
fid=$(sed -n 's/^  "id": "\([^"]*\)".*/\1/p' <<<"$fp" | head -1)
out=$(api -X POST "${json[@]}" -d '{"reason":"e2e"}' "$S/proposals/$fid/approve")
grep -q '"status": "executed"' <<<"$out" || fail "file approve: $out"
ls "$SSTATE"/out/buy-list/*-suppliers.md >/dev/null 2>&1 || fail "file action did not write the buy list"
grep -q 'Next buy list' "$SSTATE"/out/buy-list/*-suppliers.md || fail "buy list content"
expect "shop audit chain" '"ok": true' api "$S/audit/verify"

echo "e2e OK"
