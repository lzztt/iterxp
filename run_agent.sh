#!/usr/bin/env bash
set -euo pipefail
cd /home/admin/iterxp

while true; do
  if [[ main.go -nt ./iterxp-agent ]] || [[ go.mod -nt ./iterxp-agent ]] || [[ main_test.go -nt ./iterxp-agent ]]; then
    echo "[run_agent $(date +%FT%T)] source newer than binary, rebuilding"
    go build -o ./iterxp-agent.tmp .
    chmod 0700 ./iterxp-agent.tmp
    mv ./iterxp-agent.tmp ./iterxp-agent
  fi
  echo "[run_agent $(date +%FT%T)] starting ./iterxp-agent"
  set +e
  ./iterxp-agent
  code=$?
  set -e
  echo "[run_agent $(date +%FT%T)] agent exited with code=$code; restarting in 5s"
  sleep 5
done
