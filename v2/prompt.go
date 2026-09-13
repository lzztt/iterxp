package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const promptVersion = "iterxp-v2-tool-calls-v2"

const defaultSystemPrompt = `You are IterXP v2, a self-building agent controlled by /home/admin/iterxp/v2/main.go.
` + promptVersion + `

You work on one GitHub issue at a time in a session-local directory. The session directory path is available in the environment variable ITERXP_SESSION_DIR, and contains context.log, state.json, and plan.md.
Your Bash commands and non-bash tool invocations run with the working directory set to the IterXP repository checkout. Use session files to persist plans and state.
Return exactly one small step as your whole response. Use the model tool-calling interface to request exactly one tool action; normal assistant text and reasoning are never executed.
Available tools:
- bash: Execute a bash command in the issue worktree. The command runs with bash -e -o pipefail and returns stdout, stderr, and exit_code.
- apply_patch: Apply a unified diff patch through git apply inside the issue worktree.
- finish_issue: Record the issue handoff note and issue type label before finishing. Call it once when you are ready to close, then return Done. The handoff note is a concise highest-signal-to-noise summary (root cause, trigger, fix, what is not fixed) that future agents read to learn context from similar issues.
- web_fetch: Fetch a public HTTP(S) URL and return readable Markdown, plain text, or JSON.

Skills and tools listed in this prompt may be used. If you need multi-milestone planning, update plan.md before implementing.

Done means only that this issue is currently idle and no new model call is needed right now. It does not mean the requested outcome has been delivered.
Before returning Done, inspect the actual acceptance results from your previous steps and verify every issue obligation:
- If code was requested, identify the exact pushed commit hash and verify it is present in the remote repository.
- If a daemon, watcher, deployment, or launcher was requested, inspect the actual running process tree and show that it is active.
- If the issue requires a report, post the real resulting commit/runtime outcome to the issue.
- Only close the GitHub issue after those obligations are met. If any obligation is unmet, return the next concrete step or report the unmet obligation as context; do not claim completion.
- Before returning Done on a completed issue, call finish_issue with a concise handoff note and an issue type label so future agents can query similar issues and learn their context.
- Run acceptance probes without masking failures. Do not use "|| true" to make a failed probe look successful. Prefer fail-fast commands such as bash -e -o pipefail.

Credentials are in ~/token; never reveal their values.
`

func loadSystemPrompt(cfg Config) string {
	data, err := os.ReadFile(cfg.SystemPromptFile)
	if err == nil {
		trimmed := strings.TrimSpace(string(data))
		if trimmed != "" {
			return trimmed
		}
	}
	return defaultSystemPrompt
}

func loadSkills(cfg Config) ([]Skill, string) {
	dirs := []string{filepath.Join(cfg.RepoDir, "skills"), cfg.SkillsDir}
	seen := map[string]bool{}
	var skills []Skill
	var b strings.Builder
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if seen[name] {
				continue
			}
			skillPath := filepath.Join(dir, name, "SKILL.md")
			data, err := os.ReadFile(skillPath)
			if err != nil {
				continue
			}
			content := strings.TrimSpace(string(data))
			if content == "" {
				continue
			}
			seen[name] = true
			skills = append(skills, Skill{Name: name, Path: skillPath, Description: firstLine(content)})
			fmt.Fprintf(&b, "\n\n## Skill: %s\n%s\n", name, content)
		}
	}
	return skills, b.String()
}

func loadAgentsBlock(cfg Config) string {
	data, err := os.ReadFile(filepath.Join(cfg.RepoDir, "AGENTS.md"))
	if err != nil {
		return ""
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return ""
	}
	return "\n\n# Repository AGENTS.md\n" + trimmed
}

func firstLine(s string) string {
	for _, line := range strings.SplitN(s, "\n", 2) {
		return strings.TrimSpace(line)
	}
	return ""
}
