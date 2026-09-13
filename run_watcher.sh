#!/usr/bin/env bash
set -euo pipefail
cd /home/admin/iterxp

while true; do
  needs_build=0
  for src in watcher/*.go go.mod; do
    if [[ "$src" -nt ./iterxp-watcher ]]; then
      needs_build=1
      break
    fi
  done
  if [[ $needs_build -eq 1 ]]; then
    echo "[run_watcher $(date +%FT%T)] source newer than binary, rebuilding watcher"
    go build -o ./iterxp-watcher.tmp ./watcher
    chmod 0700 ./iterxp-watcher.tmp
    mv ./iterxp-watcher.tmp ./iterxp-watcher
  fi
  echo "[run_watcher $(date +%FT%T)] starting ./iterxp-watcher"
  set +e
  ./iterxp-watcher
  code=$?
  set -e
  echo "[run_watcher $(date +%FT%T)] watcher exited with code=$code; restarting in 5s"
  sleep 5
done
