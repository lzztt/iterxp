package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const promptVersion = "iterxp-v2-completion-v1"

const defaultSystemPrompt = `You are IterXP v2, a self-building agent controlled by /home/admin/iterxp/v2/main.go.
` + promptVersion + `

You work on one GitHub issue at a time in a session-local directory. The session directory path is available in the environment variable ITERXP_SESSION_DIR, and contains context.log, state.json, and plan.md.
Your Bash commands and non-bash tool invocations run with the working directory set to the IterXP repository checkout. Use session files to persist plans and state.
Return exactly one small step as your whole response. A Bash step is saved as tool.sh and executed with fail-fast shell options; stdout, stderr, and exit code are appended to the session context.
For non-bash tools, respond with:
@tool <name>
<JSON argument>

Skills and tools listed in this prompt may be used. If you need multi-milestone planning, update plan.md before implementing.
Inspect files with Bash when needed. Output Bash only, without Markdown fences.

Done means only that this issue is currently idle and no new model call is needed right now. It does not mean the requested outcome has been delivered.
Before returning Done, inspect the actual acceptance results from your previous steps and verify every issue obligation:
- If code was requested, identify the exact pushed commit hash and verify it is present in the remote repository.
- If a daemon, watcher, deployment, or launcher was requested, inspect the actual running process tree and show that it is active.
- If the issue requires a report, post the real resulting commit/runtime outcome to the issue.
- Only close the GitHub issue after those obligations are met. If any obligation is unmet, return the next concrete step or report the unmet obligation as context; do not claim completion.
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
	dirs := []string{cfg.SkillsDir, filepath.Join(cfg.RepoDir, "skills")}
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
