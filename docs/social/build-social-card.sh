#!/usr/bin/env bash
# SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0
# Render the social card from zyntra-social-hires.html at four sizes:
#   zyntra-social-3200x1680.png / .jpg   master for LinkedIn, X and print
#   zyntra-share-2400x1260.png           README hero and Open Graph
#   zyntra-github-preview-1280x640.png   GitHub social preview (Settings -> General)
# Needs Google Chrome and macOS `sips`; nothing is installed.
#   ./docs/social/build-social-card.sh
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
CHROME="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
[[ -x "$CHROME" ]] || { echo "Google Chrome not found (set CHROME=...)" >&2; exit 1; }
shot() { # <width> <height> <scale> <png>
  "$CHROME" --headless=new --disable-gpu --hide-scrollbars --force-device-scale-factor="$3" \
    --window-size="$1,$2" --screenshot="$4" "file://$HERE/zyntra-social-hires.html" >/dev/null 2>&1
}
report() { echo "wrote docs/social/$(basename "$1") ($(sips -g pixelWidth -g pixelHeight "$1" | awk '/pixel/{printf "%s ", $2}')$(( $(wc -c < "$1") / 1024 )) KB)"; }
shot 1600 840 2   "$HERE/zyntra-social-3200x1680.png"
sips -s format jpeg -s formatOptions 92 "$HERE/zyntra-social-3200x1680.png" --out "$HERE/zyntra-social-3200x1680.jpg" >/dev/null
shot 1600 840 1.5 "$HERE/zyntra-share-2400x1260.png"
shot 1600 800 0.8 "$HERE/zyntra-github-preview-1280x640.png"
for f in zyntra-social-3200x1680.png zyntra-social-3200x1680.jpg zyntra-share-2400x1260.png zyntra-github-preview-1280x640.png; do report "$HERE/$f"; done
