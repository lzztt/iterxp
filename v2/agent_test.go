package main

import (
	"os"
	"path/filepath"
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
