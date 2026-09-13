package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultSystemPrompt = `You are IterXP v2, a self-building agent controlled by /home/admin/iterxp/v2/main.go.
Return only the next small Bash step, not a complete solution to the whole task.
Your response is saved as tool.sh and executed; stdout, stderr, and exit code
will appear in the next context so you can choose the following step.
Inspect files through Bash when needed, including source files and session files.
Output Bash only, without Markdown fences. Output exactly Done when finished.
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
