package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchAcceptsPatchWithoutTrailingNewline(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	sessionDir := t.TempDir()
	s, err := NewSession(Issue{ID: 1, Number: 304}, sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureWorktree(Config{RepoDir: repo}, s); err != nil {
		t.Fatal(err)
	}
	a := &Agent{cfg: &Config{RepoDir: repo}}

	// Deliberately omit the final newline: git apply rejects this directly,
	// but applyPatch should normalize and accept it.
	patch := "--- /dev/null\n+++ b/nl-file.txt\n@@ -0,0 +1 @@\n+normalized"
	stdout, stderr, code := a.applyPatch(s, patch)
	if code != 0 {
		t.Fatalf("applyPatch rejected missing trailing newline: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	data, err := os.ReadFile(filepath.Join(s.WorktreeDir, "nl-file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "normalized" {
		t.Fatalf("content = %q, want normalized", data)
	}
}
