#!/usr/bin/env bash
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
# smoke-remote.sh — Verify a running Zyntra instance (local or remote)
# ============================================================================
# Checks health, console, auth, live sources, decisions, AI, and a full
# propose → approve → execute round trip (through Fabric Keep when the
# instance runs keep approvals). Execution is whatever the instance is set to
# (dry-run by default: kubectl --dry-run=server).
#
# Usage:
#   ZYNTRA_URL=http://212.8.248.187:19620 ./scripts/smoke-remote.sh
#   ./scripts/smoke-remote.sh            # HOST/PORT from .deploy-last
#
# The access key comes from ZYNTRA_API_KEY, else it is read over SSH from
# /etc/zyntra/zyntra.env on the deploy host (never printed).
#
# Options:
#   --action ID   action to propose (default raise-inference-priority)
#   --no-exec     skip the propose/approve round trip
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ACTION="raise-inference-priority"
DO_EXEC=1
while [ $# -gt 0 ]; do
  case "$1" in
    --action) [ $# -ge 2 ] || { echo "--action requires a value" >&2; exit 2; }; ACTION="$2"; shift 2 ;;
    --no-exec) DO_EXEC=0; shift ;;
    --help|-h) sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done

last() { awk -F= -v k="$1" '$1 == k {print $2; exit}' "$ROOT/.deploy-last" 2>/dev/null || true; }
LAST_HOST="$(last HOST)"
LAST_PORT="$(last PORT)"
SSH_HOST="${DEPLOY_HOST:-$LAST_HOST}"
SSH_USER="${DEPLOY_USER:-$(last USER)}"
BASE="${ZYNTRA_URL:-}"
if [ -z "$BASE" ] && [ -n "$LAST_HOST" ] && [ -n "$LAST_PORT" ]; then
  BASE="http://${LAST_HOST}:${LAST_PORT}"
fi
[ -n "$BASE" ] || { echo "Set ZYNTRA_URL=http://host:port (or deploy first for .deploy-last)" >&2; exit 2; }
BASE="${BASE%/}"

if [ -z "${ZYNTRA_API_KEY:-}" ] && [ -n "$SSH_HOST" ]; then
  ZYNTRA_API_KEY="$(ssh -o BatchMode=yes -o ConnectTimeout=10 "${SSH_USER:+$SSH_USER@}$SSH_HOST" \
    "sudo sh -c '. /etc/zyntra/zyntra.env && printf %s \"\$ZYNTRA_API_KEY\"'" 2>/dev/null | tr -d '\r' || true)"
fi
[ -n "${ZYNTRA_API_KEY:-}" ] || { echo "Set ZYNTRA_API_KEY (could not read it from the host)" >&2; exit 2; }

echo "Zyntra smoke → ${BASE}"
ZYNTRA_API_KEY="$ZYNTRA_API_KEY" python3 - "$BASE" "$ACTION" "$DO_EXEC" <<'PY'
import http.cookiejar, json, os, sys, time, urllib.error, urllib.request

base, action, do_exec = sys.argv[1], sys.argv[2], sys.argv[3] == "1"
jar = http.cookiejar.CookieJar()
opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))

def ok(msg): print(f"  ✅ {msg}")
def fail(msg):
    print(f"  ❌ {msg}", file=sys.stderr)
    sys.exit(1)

def call(method, path, body=None, auth=True, raw=False):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data, method=method,
                                 headers={"Content-Type": "application/json"} if data else {})
    o = opener if auth else urllib.request.build_opener()
    try:
        with o.open(req, timeout=30) as r:
            b = r.read()
            return r.status, (b.decode() if raw else json.loads(b or b"null"))
    except urllib.error.HTTPError as e:
        b = e.read()
        try:
            return e.code, json.loads(b)
        except ValueError:
            return e.code, b.decode(errors="replace")

c, h = call("GET", "/healthz", auth=False)
if c != 200 or h.get("status") != "ok":
    fail(f"healthz HTTP {c}: {h}")
ok("healthz")

c, html = call("GET", "/", auth=False, raw=True)
if c != 200 or '<div id="root">' not in html:
    fail(f"console HTTP {c}")
ok("console served")

c, meta = call("GET", "/api/v1/meta", auth=False)
if c != 200 or not meta.get("auth_required"):
    fail(f"meta HTTP {c} or auth not required: {meta}")
ok(f"meta: v{meta['version']} · {meta['model']} · approvals {meta['approval_mode']} · execute {meta['execute_mode']} · AI {meta['ai_mode']}")

c, _ = call("GET", "/api/v1/gaps", auth=False)
if c != 401:
    fail(f"unauthenticated gaps returned {c}, want 401")
c, _ = call("POST", "/api/v1/session", {"operator": "smoke", "token": "wrong-key"})
if c != 401:
    fail(f"wrong key returned {c}, want 401")
ok("auth rejects anonymous and wrong key")

c, s = call("POST", "/api/v1/session", {"operator": "smoke", "token": os.environ["ZYNTRA_API_KEY"]})
if c != 200:
    fail(f"login HTTP {c}: {s}")
c, who = call("GET", "/api/v1/whoami")
if c != 200 or who.get("identity", {}).get("subject") != "smoke":
    fail(f"whoami {c}: {who}")
ok("session cookie login as smoke")

deadline = time.time() + 60
while True:
    c, src = call("GET", "/api/v1/sources")
    sources = src.get("sources") or []
    if sources or time.time() > deadline:
        break
    time.sleep(3)
bad = [x for x in sources if not x["ok"]]
for x in sources:
    print(f"     {'●' if x['ok'] else '○'} {x['name']:<8} {x['kind']:<8} {x['latency_ms']:>4} ms  {len(x['kpis'])} KPIs" + (f"  {x.get('error','')}" if not x["ok"] else ""))
if not sources or bad:
    fail(f"{len(bad)}/{len(sources)} sources unhealthy")
ok(f"{len(sources)}/{len(sources)} live sources healthy")

c, g = call("GET", "/api/v1/gaps")
if c != 200:
    fail(f"gaps HTTP {c}")
ok(f"gaps: {len(g['gaps'])} KPIs off target (severity {g['severity_total']:.2f})")
c, plan = call("GET", "/api/v1/plan")
if c != 200:
    fail(f"plan HTTP {c}")
ok(f"plan: {len(plan['recommendations'])} ranked actions")

c, d = call("GET", "/api/v1/ai/digest")
if c != 200 or not d.get("text"):
    fail(f"AI digest {c}: {d}")
ok(f"AI digest ({d.get('mode')}): {d['text'][:90]}…")
c, a = call("POST", "/api/v1/ai/ask", {"question": "what should I fix first?"})
if c != 200 or not a.get("text"):
    fail(f"AI ask {c}: {a}")
ok("AI ask answered")

c, ks = call("GET", "/api/v1/keep/status")
if c == 200 and ks.get("configured"):
    st = ks.get("status") or {}
    if ks.get("error"):
        fail(f"Keep status error: {ks['error']}")
    k = st.get("keep") or {}
    signers = k.get("trusted_signers")
    ok(f"Keep: mode {k.get('keep_mode')} · signers {len(signers) if isinstance(signers, list) else signers} · sandbox ready {(k.get('fluxvm') or {}).get('ready')} · agent {st.get('agent_version') if st.get('agent_deployed') else 'not deployed'}")

if not do_exec:
    print("  ✨ smoke OK (no exec)")
    sys.exit(0)

c, p = call("POST", "/api/v1/proposals", {"action": action})
if c not in (200, 201):
    fail(f"propose {action} HTTP {c}: {p}")
if p.get("render_error"):
    fail(f"render error: {p['render_error']}")
pid = p["id"]
ok(f"proposed {action} → {pid} ({p.get('template')})")
c, p = call("POST", f"/api/v1/proposals/{pid}/approve", {"reason": "smoke test"})
if c not in (200, 202):
    fail(f"approve HTTP {c}: {p}")
ok(f"approved (HTTP {c}, status {p.get('status')})")

deadline = time.time() + 240
while p.get("status") not in ("executed", "failed") and time.time() < deadline:
    time.sleep(3)
    c, p = call("GET", f"/api/v1/proposals/{pid}")
ex = p.get("execution") or {}
keep = p.get("keep") or {}
if keep:
    print(f"     keep: mode {keep.get('mode')} session {keep.get('session_id','-')} approval {keep.get('approval_id','-')}" + (f" error {keep['error']}" if keep.get("error") else ""))
if ex.get("output"):
    print("     " + ex["output"].strip().replace("\n", "\n     "))
if p.get("status") != "executed":
    fail(f"proposal ended {p.get('status')}: {ex.get('error') or keep.get('error') or 'timeout'}")
ok(f"executed via kubectl ({ex.get('mode')})")

c, au = call("GET", "/api/v1/audit")
trail = [e for e in au.get("events", []) if e.get("proposal") == pid]
print("     audit: " + " → ".join(f"{e['to']}({e['by']})" for e in trail))
if not any(e["to"] == "executed" for e in trail):
    fail("audit trail missing the executed event")
ok("audit trail recorded")
if keep.get("mode") == "keep":
    c, ka = call("GET", f"/api/v1/keep/audit?session_id={keep.get('session_id','')}")
    n = len((ka or {}).get("items") or (ka or {}).get("entries") or []) if c == 200 else 0
    ok(f"Keep audit reachable ({n} entries)") if c == 200 else fail(f"Keep audit HTTP {c}: {ka}")
print("  ✨ smoke OK")
PY
