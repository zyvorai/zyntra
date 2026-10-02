#!/usr/bin/env bash
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0
#
# Reference for how deployment tooling talks to Zyntra. Zyntra never deploys;
# your tool does, and reports each site here. Zyntra answers which stage may
# run next, and stops the rollout on a failure or a missed KPI health gate.
#
#   ZYNTRA_URL=https://zyntra.example ZYNTRA_DEPLOY_TOKEN=... \
#     ./report.sh <decision-id> <deploy-command...>
#
# The deploy command is yours (helm, ansible, a Zyvor deployment tool): it gets
# the site name in $SITE and must exit 0 only when the site is healthy.
set -euo pipefail
: "${ZYNTRA_URL:?set ZYNTRA_URL}" "${ZYNTRA_DEPLOY_TOKEN:?set ZYNTRA_DEPLOY_TOKEN}"
id="${1:?decision id}"; shift
api() { curl -fsS -H "Authorization: Bearer $ZYNTRA_DEPLOY_TOKEN" -H 'Content-Type: application/json' "$@"; }
report() { # stage site state note
  api -X POST "$ZYNTRA_URL/api/v1/rollouts/$id/report" \
    -d "$(printf '{"stage":"%s","site":"%s","state":"%s","note":"%s"}' "$1" "$2" "$3" "$4")"
}
while true; do
  ro="$(api "$ZYNTRA_URL/api/v1/rollouts/$id")"
  state="$(jq -r .rollout.state <<<"$ro")"
  case "$state" in
    complete) echo "rollout complete"; exit 0 ;;
    aborted)  echo "rollout aborted: $(jq -r .rollout.reason <<<"$ro")"; exit 1 ;;
    halted)   echo "halted: $(jq -r .rollout.reason <<<"$ro"); waiting for a fix, a recheck or an abort" >&2; sleep 30; continue ;;
  esac
  stage="$(jq -r .rollout.current <<<"$ro")"
  for site in $(jq -r --arg s "$stage" '.rollout.stages[] | select(.name==$s) | .sites[]' <<<"$ro"); do
    [ "$(jq -r --arg s "$stage" --arg x "$site" '.rollout.stages[] | select(.name==$s) | .reports[$x].state // ""' <<<"$ro")" = healthy ] && continue
    report "$stage" "$site" started "" >/dev/null
    if SITE="$site" "$@"; then report "$stage" "$site" healthy "" >/dev/null
    else report "$stage" "$site" failed "deploy command failed" >/dev/null; fi
  done
done
