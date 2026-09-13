#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(readlink -f "${ITERXP_REPO_DIR:-/home/admin/iterxp}")"
config_dir="$(readlink -f "${ITERXP_WATCHER_CONFIG_DIR:-$HOME/.iterxp_v2}")"
agent_bin="$(readlink -f "${ITERXP_AGENT_BIN:-$repo_dir/iterxp-agent-v2}")"
watcher_bin="$(readlink -f "${ITERXP_WATCHER_BIN:-$repo_dir/iterxp-watcher}")"
state_path="${ITERXP_WATCHER_STATE:-$config_dir/watcher_state.json}"

fail() { echo "verify_deployment: $*" >&2; exit 1; }

[ -x "$agent_bin" ] || fail "agent binary not executable: $agent_bin"
[ -x "$watcher_bin" ] || fail "watcher binary not executable: $watcher_bin"
[ -f "$state_path" ] || fail "watcher state not found: $state_path"

agent_pids="$(python3 - "$agent_bin" <<'PY'
import os, sys
target = os.path.realpath(sys.argv[1])
pids = []
for entry in os.listdir('/proc'):
    if not entry.isdigit():
        continue
    try:
        exe = os.path.realpath(f'/proc/{entry}/exe')
    except OSError:
        continue
    if exe == target:
        pids.append(entry)
print(' '.join(pids))
PY
)"
[ -n "$agent_pids" ] || fail "no running agent process for $agent_bin"
set -- $agent_pids
[ "$#" -eq 1 ] || fail "expected exactly one agent process, found $#: $agent_pids"
agent_pid="$1"

watcher_pids="$(python3 - "$watcher_bin" <<'PY'
import os, sys
target = os.path.realpath(sys.argv[1])
pids = []
for entry in os.listdir('/proc'):
    if not entry.isdigit():
        continue
    try:
        exe = os.path.realpath(f'/proc/{entry}/exe')
    except OSError:
        continue
    if exe == target:
        pids.append(entry)
print(' '.join(pids))
PY
)"
[ -n "$watcher_pids" ] || fail "no running watcher process for $watcher_bin"
set -- $watcher_pids
[ "$#" -eq 1 ] || fail "expected exactly one watcher process, found $#: $watcher_pids"
watcher_pid="$1"

agent_ppid="$(ps -o ppid= -p "$agent_pid" | tr -d ' ')"
[ "$agent_ppid" = "$watcher_pid" ] || fail "agent pid $agent_pid ppid=$agent_ppid is not watcher pid $watcher_pid"

agent_hash="$(sha256sum "$agent_bin" | awk '{print $1}')"
active_hash="$(python3 - "$state_path" <<'PY'
import json, sys
print(json.load(open(sys.argv[1])).get('active_hash', ''))
PY
)"
[ -n "$active_hash" ] || fail "watcher state active_hash is empty"
[ "$agent_hash" = "$active_hash" ] || fail "running agent hash $agent_hash != active_hash $active_hash"

smoke_dir="$(mktemp -d)"
trap 'rm -rf "$smoke_dir"' EXIT
help_out="$smoke_dir/help.out"
help_err="$smoke_dir/help.err"
"$agent_bin" -h >"$help_out" 2>"$help_err"
grep -q "iterxp-agent-v2 \[flags\]" "$help_out" || fail "agent -h probe did not print usage: $(cat "$help_err")"

smoke_out="$smoke_dir/smoke.out"
smoke_err="$smoke_dir/smoke.err"
marker="verify-deployment-$$-$RANDOM"
set +e
ITERXP_CONFIG_DIR="$smoke_dir/config" \
ITERXP_REPO_DIR="$smoke_dir/repo" \
ITERXP_SMOKE_TEST=1 \
ITERXP_SMOKE_MARKER="$marker" \
timeout 20s "$agent_bin" >"$smoke_out" 2>"$smoke_err"
smoke_code=$?
set -e
[ "$smoke_code" -eq 0 ] || fail "agent smoke probe failed with exit $smoke_code: $(cat "$smoke_err")"
grep -q "smoke ok" "$smoke_out" || fail "agent smoke probe missing success marker: $(cat "$smoke_out")"

echo "verify_deployment: OK agent=$agent_bin pid=$agent_pid watcher=$watcher_bin pid=$watcher_pid hash=$agent_hash"
ps -o pid=,ppid=,pgid=,stat=,cmd= -p "$watcher_pid" -p "$agent_pid"
