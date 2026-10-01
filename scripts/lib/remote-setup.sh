#!/usr/bin/env bash
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0
# remote-setup.sh — runs as root on the target host (copied by deploy-remote.sh).
#
#   remote-setup.sh install <stage-dir> <port> <exec-port> <owner>
#   remote-setup.sh keep    <stage-dir>
#   remote-setup.sh uninstall
#
# Secrets are read from files on the host (k8s secret, ~/.gryvia, Fabric admin
# password, Keep env) and written only to root-owned env files; never printed.
set -euo pipefail

ETC=/etc/zyntra
ENV_FILE=$ETC/zyntra.env
STATE=/var/lib/zyntra
BIN=/usr/local/bin/zyntra
UNIT=/etc/systemd/system/zyntra.service
KEEP_ENV=${ZYNTRA_DEPLOY_KEEP_ENV:-/etc/zyvor-fabricd/zyvor-fabric-agent.env}
KEEP_UNIT=zyvor-fabric-agent-runtime.service
EXEC_CA=$STATE/tls/exec-ca.pem

say() { printf '  • %s\n' "$*"; }

# envtool get FILE KEY | envtool set FILE KEY (value in $ENVTOOL_VALUE, kept out of argv)
# Values are written single-quoted (double-quoted and escaped if they contain '),
# which both systemd EnvironmentFile and sh read verbatim.
ENVTOOL_PY=$(cat <<'PY'
import os, re, sys
op, path, key = sys.argv[1:4]
def parse(line):
    m = re.match(r'\s*([A-Za-z_][A-Za-z0-9_]*)=(.*)$', line)
    if not m:
        return None, None
    k, v = m.group(1), m.group(2).strip()
    if len(v) >= 2 and v[0] == v[-1] and v[0] in "\"'":
        v = v[1:-1]
        if line.split("=", 1)[1].strip()[0] == '"':
            v = re.sub(r'\\(.)', r'\1', v)
    return k, v
lines = open(path).read().splitlines() if os.path.exists(path) else []
if op == "get":
    val = ""
    for l in lines:
        k, v = parse(l)
        if k == key:
            val = v
    sys.stdout.write(val)
    sys.exit(0)
val = os.environ.get("ENVTOOL_VALUE", "")
if "'" not in val:
    quoted = "'" + val + "'"
else:
    quoted = '"' + val.replace("\\", "\\\\").replace('"', '\\"').replace("$", "\\$") + '"'
out, done = [], False
for l in lines:
    k, _ = parse(l)
    if k == key:
        if not done:
            out.append(f"{key}={quoted}")
            done = True
        continue
    out.append(l)
if not done:
    out.append(f"{key}={quoted}")
tmp = path + ".tmp"
fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o640)
with os.fdopen(fd, "w") as f:
    f.write("\n".join(out) + "\n")
if os.path.exists(path):
    st = os.stat(path)
    os.chown(tmp, st.st_uid, st.st_gid)
    os.chmod(tmp, st.st_mode & 0o777)
os.replace(tmp, path)
PY
)
setenv() { ENVTOOL_VALUE="$3" python3 -c "$ENVTOOL_PY" set "$1" "$2"; }
getenv() { python3 -c "$ENVTOOL_PY" get "$1" "$2"; }

reachable() { [ "$(curl -ks -m 5 -o /dev/null -w '%{http_code}' "$1" || true)" != "000" ]; }

install_zyntra() {
    local stage=$1 port=$2 exec_port=$3 owner=$4
    local owner_home
    owner_home=$(getent passwd "$owner" | cut -d: -f6)

    id zyntra &>/dev/null || useradd --system --home-dir $STATE --no-create-home --shell /usr/sbin/nologin zyntra
    install -d -m 755 $ETC
    install -d -m 750 -o zyntra -g zyntra $STATE
    install -m 755 "$stage/zyntra" $BIN
    rm -rf $ETC/examples
    tar -C $ETC -xzf "$stage/examples.tgz"
    install -m 644 "$stage/zyntra.service" $UNIT
    say "binary, examples and unit installed"

    local kcfg=${ZYNTRA_DEPLOY_KUBECONFIG:-$owner_home/.kube/gryvia-k3s.yaml}
    [ -f "$kcfg" ] || kcfg=/etc/rancher/k3s/k3s.yaml
    if [ -f "$kcfg" ]; then
        install -m 640 -o root -g zyntra "$kcfg" $ETC/kubeconfig
        say "kubeconfig from $kcfg"
    fi

    touch $ENV_FILE
    chown root:zyntra $ENV_FILE
    chmod 640 $ENV_FILE

    local v
    v=$(getenv "$stage/overrides.env" ZYNTRA_API_KEY)
    [ -n "$v" ] || v=$(getenv $ENV_FILE ZYNTRA_API_KEY)
    setenv $ENV_FILE ZYNTRA_API_KEY "${v:-Admin@321}"
    v=$(getenv $ENV_FILE ZYNTRA_EXEC_TOKEN)
    setenv $ENV_FILE ZYNTRA_EXEC_TOKEN "${v:-$($BIN exec-token)}"
    for k in ZYNTRA_AI_BASE_URL ZYNTRA_AI_API_KEY ZYNTRA_AI_MODEL ZYNTRA_AI_LABEL ZYNTRA_AI_INSECURE; do
        v=$(getenv "$stage/overrides.env" $k)
        [ -z "$v" ] || setenv $ENV_FILE $k "$v"
    done

    setenv $ENV_FILE ZYNTRA_LISTEN "0.0.0.0:$port"
    setenv $ENV_FILE ZYNTRA_MODEL "${ZYNTRA_DEPLOY_MODEL:-$ETC/examples/lab-kpis.yaml}"
    setenv $ENV_FILE ZYNTRA_STATE_DIR $STATE
    setenv $ENV_FILE ZYNTRA_HOST "$(hostname)"
    setenv $ENV_FILE ZYNTRA_EXECUTE "${ZYNTRA_DEPLOY_EXECUTE:-dry-run}"
    setenv $ENV_FILE ZYNTRA_EXEC_TLS_ADDR "127.0.0.1:$exec_port"
    setenv $ENV_FILE ZYNTRA_ENDPOINT_INSECURE 1
    [ -f $ETC/kubeconfig ] && setenv $ENV_FILE ZYNTRA_KUBECONFIG $ETC/kubeconfig

    local url
    url=${ZYNTRA_DEPLOY_NETRA_URL:-https://127.0.0.1:30870}
    if reachable "$url/healthz"; then
        setenv $ENV_FILE ZYNTRA_NETRA_URL "$url"
        v=$(kubectl --kubeconfig $ETC/kubeconfig -n netra-system get secret netra-auth \
            -o jsonpath='{.data.api-key}' 2>/dev/null | base64 -d 2>/dev/null || true)
        [ -z "$v" ] || setenv $ENV_FILE ZYNTRA_NETRA_TOKEN "$v"
        say "netra: $url (token $([ -n "$v" ] && echo found || echo missing))"
    else
        say "netra: not reachable at $url"
    fi

    url=${ZYNTRA_DEPLOY_GRAVIA_URL:-https://127.0.0.1:32443}
    if reachable "$url/"; then
        setenv $ENV_FILE ZYNTRA_GRAVIA_URL "$url"
        v=$(tr -d '\r\n' < "$owner_home/.gryvia/api-key" 2>/dev/null || true)
        [ -z "$v" ] || setenv $ENV_FILE ZYNTRA_GRAVIA_TOKEN "$v"
        say "gravia: $url (token $([ -n "$v" ] && echo found || echo missing))"
    else
        say "gravia: not reachable at $url"
    fi

    url=${ZYNTRA_DEPLOY_FABRIC_URL:-https://127.0.0.1:9095}
    if reachable "$url/"; then
        setenv $ENV_FILE ZYNTRA_FABRIC_URL "$url"
        v=$(tr -d '\r\n' < /var/lib/zyvor-fabricd/.admin_password 2>/dev/null || true)
        [ -z "$v" ] || setenv $ENV_FILE ZYNTRA_FABRIC_PASSWORD "$v"
        say "fabric: $url (password $([ -n "$v" ] && echo found || echo missing))"
    else
        say "fabric: not reachable at $url"
    fi

    local keep_token="" listen
    if [ -f "$KEEP_ENV" ]; then
        keep_token=$(getenv "$KEEP_ENV" ZYVOR_AGENT_API_TOKEN)
        listen=$(getenv "$KEEP_ENV" ZYVOR_AGENT_LISTEN)
        url="http://${listen:-127.0.0.1:9096}"
    fi
    if [ -n "$keep_token" ] && [ "${ZYNTRA_DEPLOY_KEEP:-1}" = 1 ]; then
        setenv $ENV_FILE ZYNTRA_KEEP_URL "$url"
        setenv $ENV_FILE ZYNTRA_KEEP_TOKEN "$keep_token"
        setenv $ENV_FILE ZYNTRA_APPROVAL_MODE keep
        say "keep: $url (approvals via Keep)"
    else
        setenv $ENV_FILE ZYNTRA_APPROVAL_MODE local
        say "keep: not configured (local approvals)"
    fi

    systemctl daemon-reload
    systemctl enable zyntra.service >/dev/null 2>&1
    systemctl restart zyntra.service
    for _ in $(seq 1 20); do
        curl -fs -m 2 "http://127.0.0.1:$port/healthz" >/dev/null 2>&1 && break
        sleep 0.5
    done
    if ! systemctl is-active --quiet zyntra.service; then
        journalctl -u zyntra.service --no-pager -n 30
        exit 1
    fi
    if command -v ufw &>/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
        ufw allow "$port/tcp" >/dev/null || true
    elif command -v firewall-cmd &>/dev/null; then
        firewall-cmd --permanent --add-port="$port/tcp" >/dev/null 2>&1 || true
        firewall-cmd --reload >/dev/null 2>&1 || true
    fi
    say "zyntra.service active on :$port"
}

# Teach Keep about Zyntra's exec endpoint: the zyntra-exec credential, the
# token it injects, and the private CA of the loopback TLS listener.
configure_keep() {
    local stage=$1
    [ -f "$KEEP_ENV" ] || { say "keep: $KEEP_ENV missing, skipped"; return 0; }
    for _ in $(seq 1 20); do [ -f $EXEC_CA ] && break; sleep 0.5; done
    [ -f $EXEC_CA ] || { echo "exec CA $EXEC_CA was not created" >&2; exit 1; }

    [ -f "$KEEP_ENV.zyntra-orig" ] || cp -p "$KEEP_ENV" "$KEEP_ENV.zyntra-orig"
    local creds before after
    creds=$(getenv "$KEEP_ENV" ZYVOR_AGENT_CREDENTIALS_FILE)
    [ -n "$creds" ] || creds=$(dirname "$KEEP_ENV")/agent-credentials.json
    before=$({ cat "$KEEP_ENV"; cat "$creds" 2>/dev/null || true; } | sha256sum)

    python3 - "$creds" "$stage/credential.json" <<'PY'
import json, os, sys
path, add = sys.argv[1], sys.argv[2]
cur = json.load(open(path)) if os.path.exists(path) else {}
cur.update(json.load(open(add)))
tmp = path + ".tmp"
fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, "w") as f:
    json.dump(cur, f, indent=2)
    f.write("\n")
os.replace(tmp, path)
PY
    setenv "$KEEP_ENV" ZYVOR_AGENT_CREDENTIALS_FILE "$creds"
    setenv "$KEEP_ENV" ZYNTRA_EXEC_TOKEN "$(getenv $ENV_FILE ZYNTRA_EXEC_TOKEN)"
    local cas
    cas=$(getenv "$KEEP_ENV" ZYVOR_AGENT_EXTRA_CA_FILE)
    case ",$cas," in
        *",$EXEC_CA,"*) ;;
        *) setenv "$KEEP_ENV" ZYVOR_AGENT_EXTRA_CA_FILE "${cas:+$cas,}$EXEC_CA" ;;
    esac

    after=$({ cat "$KEEP_ENV"; cat "$creds" 2>/dev/null || true; } | sha256sum)
    if [ "$before" != "$after" ]; then
        systemctl restart $KEEP_UNIT
        say "keep: credential zyntra-exec + exec CA configured, $KEEP_UNIT restarted"
    else
        say "keep: already configured"
    fi
    local url
    url=$(getenv $ENV_FILE ZYNTRA_KEEP_URL)
    for _ in $(seq 1 30); do
        [ "$(curl -s -m 2 -o /dev/null -w '%{http_code}' "$url/v1/keep/status" || true)" != "000" ] && return 0
        sleep 1
    done
    systemctl --no-pager status $KEEP_UNIT | tail -20
    exit 1
}

uninstall() {
    systemctl disable --now zyntra.service 2>/dev/null || true
    rm -f $BIN $UNIT
    rm -rf $ETC $STATE
    systemctl daemon-reload
    if [ -f "$KEEP_ENV.zyntra-orig" ]; then
        mv -f "$KEEP_ENV.zyntra-orig" "$KEEP_ENV"
        systemctl restart $KEEP_UNIT || true
        say "keep env restored"
    fi
    say "zyntra removed"
}

case "${1:-}" in
    install)   shift; install_zyntra "$@" ;;
    keep)      shift; configure_keep "$@" ;;
    uninstall) uninstall ;;
    *) echo "usage: $0 install|keep|uninstall" >&2; exit 2 ;;
esac
