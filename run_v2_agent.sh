#!/usr/bin/env bash
set -euo pipefail
cd /home/admin/iterxp

while true; do
  needs_build=0
  for src in v2/*.go go.mod; do
    if [[ "$src" -nt ./iterxp-agent-v2 ]]; then
      needs_build=1
      break
    fi
  done
  if [[ $needs_build -eq 1 ]]; then
    echo "[run_v2_agent $(date +%FT%T)] source newer than binary, rebuilding"
    go build -o ./iterxp-agent-v2.tmp ./v2
    chmod 0700 ./iterxp-agent-v2.tmp
    mv ./iterxp-agent-v2.tmp ./iterxp-agent-v2
  fi
  echo "[run_v2_agent $(date +%FT%T)] starting ./iterxp-agent-v2"
  set +e
  ./iterxp-agent-v2
  code=$?
  set -e
  echo "[run_v2_agent $(date +%FT%T)] agent exited with code=$code; restarting in 5s"
  sleep 5
done
