#!/usr/bin/env bash
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0
# deploy-remote.sh — Deploy Zyntra to a remote host as a systemd service
# ============================================================================
# Zyntra builds as a single static binary with the console embedded, so the
# host needs no toolchain:
#   1. Detect the remote arch over SSH
#   2. Build the console (web/dist) and cross-compile ./cmd/zyntra locally
#   3. Copy binary + examples + unit; write /etc/zyntra/zyntra.env from the
#      host's own Netra / Gravia / Fabric / Keep credentials (never printed)
#   4. Start zyntra.service (it creates the exec listener's private CA)
#   5. Configure Fabric Keep: zyntra-exec credential, exec token, extra CA
#   6. Sign + deploy the zyntra-executor agent to Keep through an SSH tunnel
#      (the signer seed stays on this workstation; only signatures travel)
#   7. Verify with scripts/smoke-remote.sh
#
# Usage:
#   ./scripts/deploy-remote.sh <host> [user] [password] [options]
#   ./scripts/deploy-remote.sh 212.8.248.187 sus
#   ./scripts/deploy-remote.sh 212.8.248.187 sus --port 19620
#   ./scripts/deploy-remote.sh 212.8.248.187 sus --uninstall
#
# Options:
#   --port N      Console/API port (default: .deploy-last, else 19620)
#   --exec-port N Loopback TLS port Keep calls to execute (default 19621)
#   --no-keep     Local approvals only; leave Keep untouched
#   --skip-web    Reuse the existing web/dist build
#   --uninstall   Remove zyntra and restore the original Keep env file
#   --dry-run     Print what would happen; make no changes
#   --skip-smoke  Skip scripts/smoke-remote.sh at the end
#
# Environment:
#   DEPLOY_HOST, DEPLOY_USER, DEPLOY_PASS   same as the positional args
#   ZYNTRA_PORT                             console port
#   ZYNTRA_API_KEY                          console key (default: keep existing, else Admin@321)
#   ZYNTRA_AI_BASE_URL / _API_KEY / _MODEL  optional OpenAI-compatible model (e.g. Fabric AI gateway)
#   ZYNTRA_KEEP_SEED                        signer seed (default ~/.config/zyvor/keep-signer.seed)
#   ZYNTRA_DEPLOY_EXECUTE                   dry-run (default) | apply
# ============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
# shellcheck source=lib/deploy-common.sh
source "$SCRIPT_DIR/lib/deploy-common.sh"

info()  { zyntra_info "$@"; }
warn()  { zyntra_warn "$@"; }
error() { zyntra_error "$@"; }
step()  { deploy_ui_step_start "$*"; }

UNINSTALL_MODE=false
DRY_RUN=false
SKIP_SMOKE=false
SKIP_WEB=false
USE_KEEP=true
PORT_FROM_CLI=""
EXEC_PORT=19621
POSITIONAL=()
while [ $# -gt 0 ]; do
    case "$1" in
        --port)       [ $# -ge 2 ] || error "--port requires a value"; PORT_FROM_CLI="$2"; shift 2 ;;
        --port=*)     PORT_FROM_CLI="${1#*=}"; shift ;;
        --exec-port)  [ $# -ge 2 ] || error "--exec-port requires a value"; EXEC_PORT="$2"; shift 2 ;;
        --no-keep)    USE_KEEP=false; shift ;;
        --skip-web)   SKIP_WEB=true; shift ;;
        --uninstall)  UNINSTALL_MODE=true; shift ;;
        --dry-run)    DRY_RUN=true; shift ;;
        --skip-smoke) SKIP_SMOKE=true; shift ;;
        --help|-h)    sed -n '2,42p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        --)           shift; POSITIONAL+=("$@"); break ;;
        -*)           error "Unknown option: $1 (see --help)" ;;
        *)            POSITIONAL+=("$1"); shift ;;
    esac
done

HOST="${POSITIONAL[0]:-${DEPLOY_HOST:-}}"
USER="${POSITIONAL[1]:-${DEPLOY_USER:-root}}"
PASS="${POSITIONAL[2]:-${DEPLOY_PASS:-}}"

zyntra_parse_target HOST USER
LAST_PORT=""
if [ -z "$HOST" ] && zyntra_load_deploy_last "$REPO_DIR"; then
    info "Using .deploy-last → ${USER}@${HOST}"
    LAST_PORT="${PORT:-}"
elif [ -f "$REPO_DIR/.deploy-last" ]; then
    LAST_PORT="$(awk -F= '/^PORT=/ {print $2; exit}' "$REPO_DIR/.deploy-last")"
fi
[ -z "$HOST" ] && error "Usage: $0 <host> [user] [password] [options]  (see --help)"

ZYNTRA_PORT="${PORT_FROM_CLI:-${ZYNTRA_PORT:-${LAST_PORT:-19620}}}"
for p in "$ZYNTRA_PORT" "$EXEC_PORT"; do
    case "$p" in ''|*[!0-9]*) error "Invalid port: $p" ;; esac
    { [ "$p" -ge 1 ] && [ "$p" -le 65535 ]; } || error "Port out of range: $p"
done
[ "$ZYNTRA_PORT" != "$EXEC_PORT" ] || error "--port and --exec-port must differ"
[ -f "$REPO_DIR/go.mod" ] || error "Not in the zyntra repo: $REPO_DIR"
zyntra_build_metadata "$REPO_DIR"
# shellcheck disable=SC2034
DEPLOY_UI_PORT="$ZYNTRA_PORT"
SEED="${ZYNTRA_KEEP_SEED:-$HOME/.config/zyvor/keep-signer.seed}"

SUDO=""
[ "$USER" != "root" ] && SUDO="sudo"
SSH_OPTS=(-o StrictHostKeyChecking=accept-new -o ConnectTimeout=15 -o ServerAliveInterval=15 -o ServerAliveCountMax=8)
if [ -n "$PASS" ] && ! command -v sshpass &>/dev/null; then
    error "sshpass required for password auth (brew install sshpass / dnf install sshpass)"
fi
_ssh() {
    if [ -n "$PASS" ]; then SSHPASS="$PASS" sshpass -e ssh "${SSH_OPTS[@]}" "${USER}@${HOST}" "$@"
    else ssh "${SSH_OPTS[@]}" "${USER}@${HOST}" "$@"; fi
}
# remote_env KEY prints one value from /etc/zyntra/zyntra.env (captured, never echoed).
remote_env() {
    _ssh "$SUDO sh -c '. /etc/zyntra/zyntra.env && printf %s \"\$$1\"'" | tr -d '\r'
}
_scp() {
    if [ -n "$PASS" ]; then SSHPASS="$PASS" sshpass -e scp "${SSH_OPTS[@]}" "$@"
    else scp "${SSH_OPTS[@]}" "$@"; fi
}

if $DRY_RUN; then
    deploy_ui_banner "${DEPLOY_UI_ICON_MAGIC} Dry run" "no changes will be made"
    deploy_ui_kv "🎯" "Target" "${USER}@${HOST}"
    deploy_ui_kv "🌐" "Console" "http://${HOST}:${ZYNTRA_PORT}"
    deploy_ui_kv "🔒" "Exec" "https://127.0.0.1:${EXEC_PORT} (host loopback, Keep only)"
    deploy_ui_kv "📄" "Env file" "/etc/zyntra/zyntra.env"
    deploy_ui_kv "🛡️" "Keep" "$($USE_KEEP && echo "credential + CA + signed agent" || echo "untouched")"
    echo ""
    deploy_ui_note "Would: build web + linux binary → install → write env → start → configure Keep → deploy agent → smoke"
    exit 0
fi

deploy_ui_banner "Remote Deploy" "${ZYNTRA_GIT_VERSION} (${ZYNTRA_GIT_COMMIT}) → ${USER}@${HOST}"
deploy_ui_kv "🎯" "Target" "${USER}@${HOST}"
deploy_ui_kv "🔐" "Auth" "$([ -n "$PASS" ] && echo 'password' || echo 'SSH key')"
deploy_ui_kv "🌐" "Port" "$ZYNTRA_PORT"
echo ""

STAGE="/tmp/zyntra-deploy.$$"
BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT

if $UNINSTALL_MODE; then
    deploy_ui_uninstall_banner
    step "Uninstalling zyntra from ${HOST}"
    _ssh "mkdir -p $STAGE"
    _scp "$SCRIPT_DIR/lib/remote-setup.sh" "${USER}@${HOST}:$STAGE/remote-setup.sh"
    _ssh "$SUDO bash $STAGE/remote-setup.sh uninstall; rm -rf $STAGE"
    rm -f "$REPO_DIR/.deploy-last"
    info "zyntra removed from ${HOST}"
    exit 0
fi

# ── 1. remote arch ──
step "Detecting remote architecture"
case "$(_ssh uname -m | tr -d '\r')" in
    x86_64)        GOARCH=amd64 ;;
    aarch64|arm64) GOARCH=arm64 ;;
    *) error "Unsupported remote architecture" ;;
esac
info "Remote: linux/${GOARCH}"

# ── 2. build ──
if $SKIP_WEB && [ -f "$REPO_DIR/web/dist/index.html" ]; then
    info "Reusing web/dist"
else
    step "Building console (web/dist)"
    make -C "$REPO_DIR" web >/dev/null
    info "Console built"
fi
step "Cross-compiling zyntra for linux/${GOARCH}"
( cd "$REPO_DIR" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" \
    go build -trimpath -ldflags="-s -w" -o "$BUILD_DIR/zyntra" ./cmd/zyntra )
( cd "$REPO_DIR" && go build -o "$BUILD_DIR/zyntra-local" ./cmd/zyntra )
COPYFILE_DISABLE=1 tar --no-xattrs -C "$REPO_DIR" -czf "$BUILD_DIR/examples.tgz" examples
"$BUILD_DIR/zyntra-local" keep credential > "$BUILD_DIR/credential.json"
python3 - "$BUILD_DIR/credential.json" "$EXEC_PORT" <<'PY'
import json, sys
p, port = sys.argv[1], int(sys.argv[2])
d = json.load(open(p))
d["zyntra-exec"]["allowed_ports"] = [port]
json.dump(d, open(p, "w"), indent=2)
PY
(
    umask 077
    : > "$BUILD_DIR/overrides.env"
    for k in ZYNTRA_API_KEY ZYNTRA_AI_BASE_URL ZYNTRA_AI_API_KEY ZYNTRA_AI_MODEL ZYNTRA_AI_LABEL ZYNTRA_AI_INSECURE; do
        [ -z "${!k:-}" ] || printf '%s=%s\n' "$k" "${!k}" >> "$BUILD_DIR/overrides.env"
    done
)
info "Built $(du -h "$BUILD_DIR/zyntra" | cut -f1) binary"

# ── 3+4. install, env, start ──
step "Installing zyntra on ${HOST}"
_ssh "umask 077 && mkdir -p $STAGE"
_scp -q "$BUILD_DIR/zyntra" "$BUILD_DIR/examples.tgz" "$BUILD_DIR/credential.json" "$BUILD_DIR/overrides.env" \
    "$REPO_DIR/systemd/zyntra.service" "$SCRIPT_DIR/lib/remote-setup.sh" "${USER}@${HOST}:$STAGE/"
KEEP_FLAG=""
$USE_KEEP || KEEP_FLAG="ZYNTRA_DEPLOY_KEEP=0"
_ssh "$SUDO env $KEEP_FLAG ZYNTRA_DEPLOY_EXECUTE=${ZYNTRA_DEPLOY_EXECUTE:-dry-run} bash $STAGE/remote-setup.sh install $STAGE $ZYNTRA_PORT $EXEC_PORT $USER" \
    || { _ssh "rm -rf $STAGE"; error "remote install failed"; }
info "zyntra.service running"

# ── 5+6. Keep ──
if $USE_KEEP; then
    step "Configuring Fabric Keep"
    if _ssh "$SUDO bash $STAGE/remote-setup.sh keep $STAGE"; then
        info "Keep trusts the exec listener"
    else
        warn "Keep configuration failed; approvals fall back to local execution"
        USE_KEEP=false
    fi
fi
_ssh "rm -rf $STAGE"

if $USE_KEEP; then
    step "Signing + deploying zyntra-executor to Keep"
    if [ ! -f "$SEED" ]; then
        warn "No signer seed at $SEED; skipping agent deploy (Keep will refuse unsigned agents)"
    else
        KEEP_LISTEN=$(remote_env ZYNTRA_KEEP_URL)
        KEEP_LISTEN=${KEEP_LISTEN#http://}
        TUNNEL_PORT=$((29000 + RANDOM % 900))
        ssh "${SSH_OPTS[@]}" -o ExitOnForwardFailure=yes -f -N -L "127.0.0.1:${TUNNEL_PORT}:${KEEP_LISTEN:-127.0.0.1:9096}" "${USER}@${HOST}"
        TUNNEL_PID=$(pgrep -f "127.0.0.1:${TUNNEL_PORT}:" | head -1 || true)
        ZYNTRA_KEEP_TOKEN=$(remote_env ZYNTRA_KEEP_TOKEN)
        if ZYNTRA_KEEP_TOKEN="$ZYNTRA_KEEP_TOKEN" "$BUILD_DIR/zyntra-local" keep deploy -seed "$SEED" -url "http://127.0.0.1:${TUNNEL_PORT}"; then
            info "Agent deployed (signed locally; seed never left this machine)"
        else
            warn "Agent deploy failed; proposals fall back to local execution after approval"
        fi
        unset ZYNTRA_KEEP_TOKEN
        [ -n "$TUNNEL_PID" ] && kill "$TUNNEL_PID" 2>/dev/null || true
    fi
fi

# ── 7. verify ──
step "Verifying deployment"
BASE_URL="http://${HOST}:${ZYNTRA_PORT}"
_ssh "curl -fsS http://127.0.0.1:${ZYNTRA_PORT}/healthz >/dev/null" && info "Health check OK (on-host)"
zyntra_save_deploy_last "$REPO_DIR" "$HOST" "$USER" "full"

deploy_ui_highlight "📋 Final checklist"
deploy_ui_checklist "service" "$(_ssh "systemctl is-active zyntra.service" | tr -d '\r')"
deploy_ui_checklist "health"  "$(_ssh "curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:${ZYNTRA_PORT}/healthz" | tr -d '\r')"
zyntra_print_success "$HOST" 0
deploy_ui_note "Console key: sudo grep ZYNTRA_API_KEY /etc/zyntra/zyntra.env (on ${HOST})"

if $SKIP_SMOKE; then
    info "Skipped smoke-remote.sh (--skip-smoke)"
else
    step "Running scripts/smoke-remote.sh against ${BASE_URL}"
    ( cd "$REPO_DIR" && ZYNTRA_URL="$BASE_URL" DEPLOY_HOST="$HOST" DEPLOY_USER="$USER" ./scripts/smoke-remote.sh )
fi
