package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunBash(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSession(Issue{ID: 1, Number: 77}, dir)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{}
	stdout, stderr, code := a.runBash("echo hello", s)
	if code != 0 || stdout != "hello\n" || stderr != "" {
		t.Fatalf("runBash = (%q,%q,%d)", stdout, stderr, code)
	}
}

func TestRunTool(t *testing.T) {
	dir := t.TempDir()
	toolDir := filepath.Join(dir, "tools")
	if err := os.MkdirAll(toolDir, 0700); err != nil {
		t.Fatal(err)
	}
	toolPath := filepath.Join(toolDir, "dummy")
	script := "#!/bin/sh\necho \"TOOL:$1\"\n"
	if err := os.WriteFile(toolPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a := &Agent{
		cfg:   &Config{ToolsDir: toolDir},
		tools: map[string]Tool{"dummy": {Name: "dummy", Path: toolPath}},
	}
	s, err := NewSession(Issue{ID: 1, Number: 77}, dir)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := a.runTool("@tool dummy\n{\"x\":1}", s)
	if code != 0 || stdout != "TOOL:{\"x\":1}\n" || stderr != "" {
		t.Fatalf("runTool = (%q,%q,%d)", stdout, stderr, code)
	}
}

func TestLoadSessionsResumesExisting(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewSession(Issue{ID: 11, Number: 22, Title: "existing"}, dir); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		SessionDir: dir,
		SkillsDir:  filepath.Join(dir, "skills"),
		ToolsDir:   filepath.Join(dir, "tools"),
	}
	for _, d := range []string{cfg.SessionDir, cfg.SkillsDir, cfg.ToolsDir} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	a := NewAgent(cfg, nil)
	s, ok := a.sessions[22]
	if !ok {
		t.Fatal("expected session issue-22 to be loaded")
	}
	if s.IssueID != 11 {
		t.Fatalf("resumed IssueID = %d, want 11", s.IssueID)
	}
}

func TestTailByTokens(t *testing.T) {
	text := strings.Repeat("a", 1000)
	if got := estimateTokens(tailByTokens(text, 10)); got > 10 {
		t.Fatalf("tailByTokens kept too many tokens: %d", got)
	}
}

func TestCompactContextArchivesAndShrinks(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSession(Issue{ID: 8, Number: 124}, dir)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("x", 12000)
	cfg := &Config{ContextCompactThreshold: 1000, ContextKeepTokens: 1}
	a := &Agent{cfg: cfg}
	compacted, err := a.compactContext(s, text)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compacted, "Context compacted at") {
		t.Fatalf("compacted output missing marker: %q", compacted)
	}
	if estimateTokens(compacted) >= estimateTokens(text) {
		t.Fatalf("compacted text is not smaller: before=%d after=%d", estimateTokens(text), estimateTokens(compacted))
	}
	entries, err := os.ReadDir(filepath.Join(s.Dir, "context_archive"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("expected context archive file: %v", err)
	}
}
