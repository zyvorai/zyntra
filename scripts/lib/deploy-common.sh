# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0
# shellcheck shell=bash disable=SC2034
# Zyntra deploy library (self-contained under scripts/lib/).

_DEPLOY_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

DEPLOY_UI_PROJECT="zyntra"
DEPLOY_UI_ICON="◆"
DEPLOY_UI_ICON_UNINSTALL="🗑️"
DEPLOY_UI_ICON_MAGIC="✨"
DEPLOY_UI_PORT="${ZYNTRA_PORT:-0}"
DEPLOY_UI_SCHEME="http"
DEPLOY_UI_DASH_PATH="/"
DEPLOY_UI_HEALTH_PATH="/healthz"

# shellcheck source=deploy-ui.sh
source "$_DEPLOY_LIB_DIR/deploy-ui.sh"

zyntra_build_metadata() {
    local repo_dir="$1"
    ZYNTRA_GIT_VERSION=$(git -C "$repo_dir" describe --tags --always --dirty 2>/dev/null || echo 'dev')
    ZYNTRA_GIT_COMMIT=$(git -C "$repo_dir" rev-parse --short HEAD 2>/dev/null || echo 'unknown')
    export ZYNTRA_GIT_VERSION ZYNTRA_GIT_COMMIT
}

zyntra_parse_target() { deploy_ui_parse_target "$@"; }
zyntra_save_deploy_last() {
    deploy_ui_save_deploy_last "$1" "$2" "$3" "$4" "${ZYNTRA_GIT_VERSION:-}" "${ZYNTRA_GIT_COMMIT:-}"
}
zyntra_load_deploy_last() { deploy_ui_load_deploy_last "$1"; }
zyntra_print_success() {
    deploy_ui_success "$1" "$2" "./scripts/deploy-remote.sh $1 --uninstall"
}

zyntra_info()  { deploy_ui_info "$@"; }
zyntra_warn()  { deploy_ui_warn "$@"; }
zyntra_error() { deploy_ui_error "$@"; }
