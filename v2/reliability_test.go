package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigUsesEnv(t *testing.T) {
	t.Setenv("ITERXP_REPO", "acme/widgets")
	t.Setenv("ITERXP_MODEL", "model-x")
	t.Setenv("ITERXP_REASONING_EFFORT", "medium")
	t.Setenv("ITERXP_WANDB_API", "https://example.invalid/v1")
	t.Setenv("ITERXP_PROJECT_HEADER", "OpenAI-Project: test/proj")
	t.Setenv("ITERXP_REPO_DIR", "/tmp/iterxp-repo")

	cfg := loadConfig()
	if cfg.Repo != "acme/widgets" {
		t.Fatalf("Repo = %q, want acme/widgets", cfg.Repo)
	}
	if cfg.Model != "model-x" {
		t.Fatalf("Model = %q, want model-x", cfg.Model)
	}
	if cfg.Reasoning != "medium" {
		t.Fatalf("Reasoning = %q, want medium", cfg.Reasoning)
	}
	if cfg.APIBase != "https://example.invalid/v1" {
		t.Fatalf("APIBase = %q", cfg.APIBase)
	}
	if cfg.ProjectHeader != "OpenAI-Project: test/proj" {
		t.Fatalf("ProjectHeader = %q", cfg.ProjectHeader)
	}
	if cfg.RepoDir != "/tmp/iterxp-repo" {
		t.Fatalf("RepoDir = %q", cfg.RepoDir)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	wantBase := filepath.Join(home, ".iterxp_v2")
	if cfg.ConfigDir != wantBase {
		t.Fatalf("ConfigDir = %q, want %q", cfg.ConfigDir, wantBase)
	}
	if cfg.SystemPromptFile != filepath.Join(wantBase, "system_prompt.md") {
		t.Fatalf("SystemPromptFile = %q", cfg.SystemPromptFile)
	}
}

func TestGetenvFallback(t *testing.T) {
	t.Setenv("ITERXP_TEST_VALUE", "")
	if got := getenv("ITERXP_TEST_VALUE", "fallback"); got != "fallback" {
		t.Fatalf("getenv empty = %q, want fallback", got)
	}
	t.Setenv("ITERXP_TEST_VALUE", "value")
	if got := getenv("ITERXP_TEST_VALUE", "fallback"); got != "value" {
		t.Fatalf("getenv set = %q, want value", got)
	}
}

func TestToolBlockListsExecutableTool(t *testing.T) {
	dir := t.TempDir()
	toolsDir := filepath.Join(dir, "tools")
	skillsDir := filepath.Join(dir, "skills")
	sessionsDir := filepath.Join(dir, "sessions")
	for _, d := range []string{toolsDir, skillsDir, sessionsDir} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	toolPath := filepath.Join(toolsDir, "hello-tool")
	if err := os.WriteFile(toolPath, []byte("#!/bin/sh\necho hello\n"), 0700); err != nil {
		t.Fatal(err)
	}

	a := NewAgent(&Config{ToolsDir: toolsDir, SkillsDir: skillsDir, SessionDir: sessionsDir}, nil)
	block := a.toolBlock()
	if !strings.Contains(block, "hello-tool") {
		t.Fatalf("toolBlock does not list executable tool: %q", block)
	}
	if !strings.Contains(block, "@tool") {
		t.Fatalf("toolBlock does not include @tool syntax: %q", block)
	}
}

func TestSessionDoneStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSession(Issue{ID: 7, Number: 123}, dir)
	if err != nil {
		t.Fatal(err)
	}
	st := SessionState{IssueID: 7, IssueNumber: 123, ContextHash: "done-hash", Done: true}
	if err := s.SaveState(st); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Done {
		t.Fatalf("Done = false, want true")
	}
	if loaded.ContextHash != "done-hash" {
		t.Fatalf("ContextHash = %q, want done-hash", loaded.ContextHash)
	}
}

func TestSharedConfigDirFromEnv(t *testing.T) {
	t.Setenv("ITERXP_CONFIG_DIR", "/tmp/iterxp-shared-config")
	cfg := loadConfig()
	if cfg.ConfigDir != "/tmp/iterxp-shared-config" {
		t.Fatalf("ConfigDir = %q, want /tmp/iterxp-shared-config", cfg.ConfigDir)
	}
	if cfg.ToolsDir != filepath.Join("/tmp/iterxp-shared-config", "tools") {
		t.Fatalf("ToolsDir = %q, want shared config tools", cfg.ToolsDir)
	}
	if cfg.SkillsDir != filepath.Join("/tmp/iterxp-shared-config", "skills") {
		t.Fatalf("SkillsDir = %q, want shared config skills", cfg.SkillsDir)
	}
}

func TestLoadAgentsBlockReadsRepo(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, "AGENTS.md"), []byte("Repo agent context."), 0600); err != nil {
		t.Fatal(err)
	}
	block := loadAgentsBlock(Config{RepoDir: repoDir})
	if !strings.Contains(block, "# Repository AGENTS.md") || !strings.Contains(block, "Repo agent context.") {
		t.Fatalf("AGENTS block = %q", block)
	}
}

func TestLoadSkillsIncludesRepoAndConfig(t *testing.T) {
	repoDir := t.TempDir()
	configDir := t.TempDir()
	for _, base := range []string{repoDir, filepath.Join(configDir, "skills")} {
		if err := os.MkdirAll(filepath.Join(base, "git-workflow"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, "git-workflow", "SKILL.md"), []byte("Use git carefully."), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{RepoDir: repoDir, SkillsDir: filepath.Join(configDir, "skills")}
	skills, block := loadSkills(cfg)
	if len(skills) != 1 {
		t.Fatalf("skills count = %d, want 1", len(skills))
	}
	if !strings.Contains(block, "## Skill: git-workflow") || !strings.Contains(block, "Use git carefully.") {
		t.Fatalf("skills block missing repo skill: %q", block)
	}
}

func TestBuildPromptIncludesAgentsBlock(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, "AGENTS.md"), []byte("Build prompt agent context."), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{RepoDir: repoDir, ToolsDir: filepath.Join(repoDir, "tools")}
	a := &Agent{cfg: &cfg}
	prompt := a.buildPrompt()
	if !strings.Contains(prompt, "# Repository AGENTS.md") || !strings.Contains(prompt, "Build prompt agent context.") {
		t.Fatalf("buildPrompt missing AGENTS.md block: %q", prompt)
	}
}
