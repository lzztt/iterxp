#!/usr/bin/env bash
set -euo pipefail

packages=(ripgrep jq tree)

echo "[setup_cli_basics] refreshing apt metadata"
sudo apt-get update -y

echo "[setup_cli_basics] installing: ${packages[*]}"
sudo apt-get install -y --no-install-recommends "${packages[@]}"

if gh_candidate="$(apt-cache policy gh 2>/dev/null | awk '$1=="Candidate:"{print $2; exit}')" && [ -n "$gh_candidate" ] && [ "$gh_candidate" != "(none)" ]; then
  echo "[setup_cli_basics] gh candidate available: ${gh_candidate}; installing via apt"
  sudo apt-get install -y --no-install-recommends gh
  echo "[setup_cli_basics] gh installed via apt"
else
  echo "[setup_cli_basics] gh unavailable: no installable candidate in configured apt repositories" >&2
fi

echo "[setup_cli_basics] verification"
for cmd in rg jq tree; do
  if command -v "$cmd" >/dev/null 2>&1; then
    printf 'available %s -> %s\n' "$cmd" "$(command -v "$cmd")"
  else
    printf 'MISSING %s\n' "$cmd" >&2
    exit 1
  fi
done
if command -v gh >/dev/null 2>&1; then
  printf 'available gh -> %s\n' "$(command -v gh)"
else
  printf 'unavailable gh\n'
fi
