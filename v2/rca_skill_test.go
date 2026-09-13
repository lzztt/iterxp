package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRCASkillAutoLoadsFromRepoSkills(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	sharedSkills := filepath.Join(root, "config", "skills")
	rcaDir := filepath.Join(repoDir, "skills", "rca")
	if err := os.MkdirAll(rcaDir, 0700); err != nil {
		t.Fatal(err)
	}
	content := "Perform root cause analysis for IterXP agent issues."
	if err := os.WriteFile(filepath.Join(rcaDir, "SKILL.md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	skills, block := loadSkills(Config{
		RepoDir:   repoDir,
		SkillsDir: sharedSkills,
	})
	if len(skills) != 1 {
		t.Fatalf("skills count = %d, want 1", len(skills))
	}
	if skills[0].Name != "rca" {
		t.Fatalf("skill name = %q, want rca", skills[0].Name)
	}
	if !strings.Contains(block, "## Skill: rca") {
		t.Fatalf("skill block missing rca skill header:\n%s", block)
	}
	if !strings.Contains(block, content) {
		t.Fatalf("skill block missing rca skill content:\n%s", block)
	}
}
