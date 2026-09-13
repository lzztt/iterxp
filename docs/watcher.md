# IterXP watcher daemon

The watcher is a long-running daemon that runs separately from the IterXP agent binary.

Launch it with:

```sh
./run_watcher.sh
```

The watcher:

- Watches `v2/*.go` and `go.mod`.
- Rebuilds `./iterxp-agent` from `./v2` when sources are newer.
- Starts the agent and keeps it running.
- Promotes a new agent to `./iterxp-agent.last_good` after a stable window.
- Counts early failures and, after the configured maximum, rolls `./iterxp-agent`
  back to `./iterxp-agent.last_good`.
- Creates a GitHub issue in the configured repository with stdout, stderr, exit
  code, and agent hash when a new version fails or cannot start.

Configuration is read from environment variables:

- `ITERXP_REPO`, `ITERXP_REPO_DIR`
- `ITERXP_GITHUB_TOKEN`
- `ITERXP_AGENT_BIN`, `ITERXP_LAST_GOOD_BIN`
- `ITERXP_WATCHER_STATE`
- `ITERXP_WATCHER_POLL_INTERVAL`
- `ITERXP_WATCHER_STABLE_WINDOW`
- `ITERXP_WATCHER_START_TIMEOUT`
- `ITERXP_WATCHER_MAX_FAILURES`

State is stored in JSON by default at `~/.iterxp_v2/watcher_state.json`.
