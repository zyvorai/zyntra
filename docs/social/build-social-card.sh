#!/usr/bin/env bash
# SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0
# Render the share card (1200x630 PNG) and the LinkedIn/X card (1600x900 JPEG).
# Needs Google Chrome and macOS `sips` (both already on a Mac); nothing is installed.
#   ./docs/social/build-social-card.sh
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
CHROME="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
[[ -x "$CHROME" ]] || { echo "Google Chrome not found (set CHROME=...)" >&2; exit 1; }
TMP="$(mktemp -d "${TMPDIR:-/tmp}/zyntra-card.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
shot() { # <html> <w> <h> <png>
  "$CHROME" --headless=new --disable-gpu --hide-scrollbars --force-device-scale-factor=1 \
    --window-size="$2,$3" --screenshot="$4" "file://$HERE/$1" >/dev/null 2>&1
}
shot zyntra-share-card.html 1200 630 "$HERE/zyntra-share-card.png"
echo "wrote docs/social/zyntra-share-card.png ($(du -k "$HERE/zyntra-share-card.png" | cut -f1) KB)"
shot zyntra-social-card.html 1600 900 "$TMP/social.png"
sips -s format jpeg -s formatOptions 92 "$TMP/social.png" --out "$HERE/zyntra-social-card.jpg" >/dev/null
echo "wrote docs/social/zyntra-social-card.jpg ($(du -k "$HERE/zyntra-social-card.jpg" | cut -f1) KB)"
