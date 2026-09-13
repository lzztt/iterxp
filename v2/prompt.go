package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultSystemPrompt = `You are IterXP v2, a self-building agent controlled by /home/admin/iterxp/v2/main.go.
You work on one GitHub issue at a time in a session-local directory. The session directory path is available in the environment variable ITERXP_SESSION_DIR, and contains context.log, state.json, and plan.md.
Your Bash commands and non-bash tool invocations run with the working directory set to the IterXP repository checkout. Use session files to persist plans and state.
Return exactly one small step as your whole response. A Bash step is saved as tool.sh and executed; stdout, stderr, and exit code are appended to the session context.
For non-bash tools, respond with:
@tool <name>
<JSON argument>

Skills and tools listed in this prompt may be used. If you need multi-milestone planning, update plan.md before implementing.
Inspect files with Bash when needed. Output Bash only, without Markdown fences. Output exactly Done when finished.
Credentials are in ~/token; never reveal their values.`

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
	entries, err := os.ReadDir(cfg.SkillsDir)
	if err != nil {
		return nil, ""
	}
	var skills []Skill
	var b strings.Builder
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		skillPath := filepath.Join(cfg.SkillsDir, name, "SKILL.md")
		data, err := os.ReadFile(skillPath)
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		skills = append(skills, Skill{Name: name, Path: skillPath, Description: firstLine(content)})
		fmt.Fprintf(&b, "\n\n## Skill: %s\n%s\n", name, content)
	}
	return skills, b.String()
}

func firstLine(s string) string {
	for _, line := range strings.SplitN(s, "\n", 2) {
		return strings.TrimSpace(line)
	}
	return ""
}
