#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "$0")/.." && pwd)"
config_dir="${ITERXP_CONFIG_DIR:-$HOME/.iterxp_v2}"
dest_dir="$config_dir/tools"
mkdir -p "$dest_dir"

for source_dir in "$repo_dir"/tools/*/; do
  [ -d "$source_dir" ] || continue
  name="$(basename "$source_dir")"
  if [ -f "$source_dir/main.go" ]; then
    echo "building $name -> $dest_dir/$name"
    (cd "$repo_dir" && go build -o "$dest_dir/$name" "./tools/$name")
    chmod 0700 "$dest_dir/$name"
  fi
done
