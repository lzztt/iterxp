package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunBashFailFastStopsAtFailedCommand(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSession(Issue{ID: 1, Number: 78}, dir)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{}
	stdout, stderr, code := a.runBash("false\necho should-not-run\n", s)
	if code == 0 {
		t.Fatalf("runBash exit code = 0, want non-zero; stdout=%q stderr=%q", stdout, stderr)
	}
	if strings.Contains(stdout, "should-not-run") {
		t.Fatalf("later command ran after a failed step; stdout=%q", stdout)
	}
}

func TestRunBashTimesOutWithOwnedChild(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSession(Issue{ID: 1, Number: 79}, dir)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{cfg: &Config{ToolTimeout: 250 * time.Millisecond, KillTimeout: 1 * time.Second}}
	start := time.Now()
	stdout, stderr, code := a.runBash("sleep 10 &\nwait\n", s)
	elapsed := time.Since(start)
	if code != 124 {
		t.Fatalf("runBash timeout exit code = %d, want 124; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("runBash timeout took too long: %s", elapsed)
	}
	if !strings.Contains(stderr, "timed out") {
		t.Fatalf("timeout stderr missing timed out message: %q", stderr)
	}
}

func TestPromptCompletionDiscipline(t *testing.T) {
	prompt := defaultSystemPrompt
	for _, want := range []string{
		promptVersion,
		"Done means only that this issue is currently idle",
		"exact pushed commit hash",
		"inspect the actual running process tree",
		"Only close the GitHub issue after those obligations are met",
		"Do not use \"|| true\"",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("default prompt missing %q", want)
		}
	}
}

func TestRunBashTimeoutWritesToolFile(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSession(Issue{ID: 1, Number: 80}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	a := &Agent{cfg: &Config{ToolTimeout: 150 * time.Millisecond, KillTimeout: 1 * time.Second}}
	_, _, code := a.runBash("sleep 10\n", s)
	if code != 124 {
		t.Fatalf("exit code = %d, want 124", code)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "tool.sh")); err != nil {
		t.Fatalf("tool.sh not written: %v", err)
	}
}
