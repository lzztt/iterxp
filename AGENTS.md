# AGENTS.md

This repository contains IterXP, a self-building agent.

## Agent context

- The active agent implementation lives in `v2/`.
- The agent polls open GitHub issues in `lzztt/iterxp` and works on one issue at a time.
- For each issue, the agent keeps session-local state under `~/.iterxp_v2/sessions/issue-<number>/`:
  `context.log`, `state.json`, and `plan.md`.
- Agent Bash and non-bash tool invocations run with the working directory set to this repository checkout.
- `ITERXP_SESSION_DIR` is set to the active issue session directory.
- Credentials live in `~/token/` and must never be revealed.

## Repository layout

- `skills/` — agent skills.
- `tools/` — source code for self-developed tools. Run `tools/install.sh` to compile tools into `~/.iterxp_v2/tools/`.
- `docs/` — design docs.
- `v2/` — current agent implementation.
- `watcher/` — long-running watcher daemon for safe agent version switching.

## Watcher daemon

- `watcher/` is the source for the long-running watcher daemon.
- `run_watcher.sh` builds and starts `./iterxp-watcher`; the watcher manages the agent binary.
- The watcher promotes stable agent versions to `./iterxp-agent.last_good`, rolls failed
  versions back, and reports failures by creating GitHub issues.
