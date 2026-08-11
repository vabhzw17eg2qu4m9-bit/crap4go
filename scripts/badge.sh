#!/usr/bin/env bash
# scripts/badge.sh — emit a shields.io-style flat badge SVG.
# Usage: bash scripts/badge.sh <label> <value> <color> <out.svg>
# Pure bash + awk/printf: no python/node dependency. See badges-hooks-spec §2.
set -euo pipefail

if [ "$#" -ne 4 ]; then
  echo "usage: $0 <label> <value> <color> <out.svg>" >&2
  exit 1
fi

label="$1" value="$2" color="$3" out="$4"

# Widths: LW/VW = ceil(len*6.5)+10, W = LW+VW (Verdana 11px). Text x = LW/2 and LW+VW/2.
read -r lw vw w lxc vxc < <(awk -v l="$label" -v v="$value" '
function ceil(x) { return int(x) + (x > int(x) ? 1 : 0) }
BEGIN { lw = ceil(length(l) * 6.5) + 10; vw = ceil(length(v) * 6.5) + 10;
  print lw, vw, lw + vw, lw / 2, lw + vw / 2 }')

mkdir -p "$(dirname "$out")"
{
  printf '<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" viewBox="0 0 %d 20" role="img" aria-label="%s: %s">\n' "$w" "$w" "$label" "$value"
  printf '  <title>%s: %s</title>\n' "$label" "$value"
  printf '  <rect width="%d" height="20" rx="3" fill="#555"/>\n' "$lw"
  printf '  <rect x="%d" width="%d" height="20" rx="3" fill="%s"/>\n' "$lw" "$vw" "$color"
  printf '  <rect width="%d" height="20" rx="3" fill="none"/>\n' "$w"
  printf '  <g fill="#fff" font-family="Verdana,DejaVu Sans,sans-serif" font-size="11" text-anchor="middle">\n'
  printf '    <text x="%s" y="14">%s</text>\n' "$lxc" "$label"
  printf '    <text x="%s" y="14">%s</text>\n' "$vxc" "$value"
  printf '  </g>\n'
  printf '</svg>\n'
} > "$out"
