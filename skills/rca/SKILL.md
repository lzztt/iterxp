# RCA Skill

Perform root cause analysis (RCA) for IterXP agent issues and propose design or source-code fixes.

## Inputs

Use these inputs. Never print or commit token values.

- Active session: `$ITERXP_SESSION_DIR/context.log`, `state.json`, `plan.md`, `llm_calls.jsonl`, and `tool.sh`
- All sessions: `$HOME/.iterxp_v2/sessions/issue-*/`
- Repo state: `git status --short`, `git log --oneline --decorate -n 20`, `git remote -v`
- Deployment state: process tree, binary hashes, `$HOME/.iterxp_v2/watcher_state.json`
- GitHub issue state: issue body, comments, labels, `state`, and `closed_at`

## RCA procedure

1. Fetch the issue and all comments with the GitHub API using the token from `~/token/github`.
2. For each local session, compare `state.json` fields (`done`, `context_hash`, `pending_context_hash`, `updated_at`, `llm_last_call_at`) against the remote GitHub issue `state`, `closed_at`, labels, and comments.
3. Replay the session `context.log` and `tool.sh` outputs. Locate the last executable action, test result, error, timeout, or incomplete model response.
4. Check delivery obligations:
   - Does the local `done=true` state match a real delivered outcome?
   - Is there an exact pushed commit hash, and does the remote contain it?
   - Are the requested daemon/watcher/agent processes actually running?
5. Classify the root cause. Typical IterXP root-cause categories:
   - state-machine/ack bug: context was consumed before the tool result or completion report was written
   - unbounded tool execution: a command started another long-running agent and blocked the loop
   - missing CLI/dispatch guard: `-h` or unknown flags started normal polling instead of help/error
   - deployment gap: a required binary, last-good copy, watcher, or restart was missing
   - completion-discipline gap: local task was marked done but the GitHub issue was never reported on or closed
   - skill/tool loading gap: a new skill exists on disk but the running agent was not restarted or the loader did not scan it
6. Propose a minimal design or source-code fix with the relevant file/function, and an acceptance probe that fails loudly without `|| true`.
7. If the issue is systemic and should be tracked as follow-up work, create a GitHub issue.

## Creating a GitHub issue with the API

Use JSON payloads and the GitHub token from `~/token/github`:

```bash
python3 - <<'PY'
import json, os, subprocess
token = open(os.path.expanduser('~/token/github')).read().strip()
issue = {
    "title": "RCA: <short title>",
    "body": "\n".join([
        "## Evidence",
        "- <evidence bullets>",
        "## Root cause",
        "- <root cause bullets>",
        "## Proposed fix",
        "- <file/function and acceptance probe>",
    ]),
}
subprocess.run([
    "curl", "-fsSL", "-X", "POST",
    "-H", f"Authorization: Bearer {token}",
    "-H", "Accept: application/vnd.github+json",
    "https://api.github.com/repos/lzztt/iterxp/issues",
    "-d", json.dumps(issue),
], check=True)
PY
```

Do not reveal token values in logs, issues, or commits.

## Report format

Use this issue body shape:

```markdown
## Evidence
<observed local state, remote state, commit, process tree>

## Root cause
<specific file/function or design decision and why it caused the failure>

## Fix
<minimal code/design change and how it was verified>

## Verification
<exact pushed commit hash, remote check, and live process/binary check>
```

## Known findings to reuse

- #3 and #4 were left open because the agent locally marked `done=true` but never reported the outcome on GitHub or closed the issue.
- #5 found that v2 ignored command-line arguments, ran sessions synchronously with unbounded tool waits, and committed context hashes too early on failed requests.
- New skills are loaded only when `NewAgent` starts. If the watcher only hashes `v2/*.go` and `go.mod`, adding a skill alone may not trigger a restart; add/update a v2 source test or otherwise force a rebuild and restart.
