package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func initWorktreeTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if _, err := runGit(repo, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(repo, "checkout", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	readme := filepath.Join(repo, "README.md")
	if err := os.WriteFile(readme, []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(repo, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(repo, "-c", "user.name=IterXP Test", "-c", "user.email=iterxp@example.com", "commit", "-m", "init"); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestEnsureWorktreeCreatesBranchAndState(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	sessionDir := t.TempDir()
	s, err := NewSession(Issue{ID: 7, Number: 123, Title: "worktree issue"}, sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureWorktree(Config{RepoDir: repo}, s); err != nil {
		t.Fatal(err)
	}
	if !isGitWorktree(s.WorktreeDir) {
		t.Fatalf("worktree is not a git worktree: %s", s.WorktreeDir)
	}
	branch, err := runGit(s.WorktreeDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if branch != "issue/123" {
		t.Fatalf("worktree branch = %q, want issue/123", branch)
	}
	base, err := runGit(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.WorktreePath != s.WorktreeDir {
		t.Fatalf("state WorktreePath = %q, want %q", st.WorktreePath, s.WorktreeDir)
	}
	if st.WorktreeBranch != "issue/123" {
		t.Fatalf("state WorktreeBranch = %q, want issue/123", st.WorktreeBranch)
	}
	if st.WorktreeBaseCommit != base {
		t.Fatalf("state WorktreeBaseCommit = %q, want %q", st.WorktreeBaseCommit, base)
	}
}

func TestEnsureWorktreeReusesDirtyWorktree(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	sessionDir := t.TempDir()
	s, err := NewSession(Issue{ID: 7, Number: 123}, sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureWorktree(Config{RepoDir: repo}, s); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(s.WorktreeDir, "dirty.txt")
	if err := os.WriteFile(marker, []byte("uncommitted"), 0600); err != nil {
		t.Fatal(err)
	}

	s2, err := NewSession(Issue{ID: 7, Number: 123}, sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureWorktree(Config{RepoDir: repo}, s2); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "uncommitted" {
		t.Fatalf("dirty edit after resume = %q, want uncommitted", data)
	}
	st, err := s2.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.WorktreePath != s.WorktreeDir || st.WorktreeBranch != "issue/123" {
		t.Fatalf("resumed worktree identity not preserved: %+v", st)
	}
}

func TestEnsureWorktreeRefusesExistingNonGitPath(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	sessionDir := t.TempDir()
	s, err := NewSession(Issue{ID: 7, Number: 123}, sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.WorktreeDir, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(s.WorktreeDir, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	err = ensureWorktree(Config{RepoDir: repo}, s)
	if err == nil {
		t.Fatal("expected ensureWorktree to refuse an existing non-git path")
	}
	if !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("error = %q, want refusing message", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("existing path was changed: %q", data)
	}
}

func TestTwoSequentialIssuesHaveDistinctWorktrees(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	sessionDir := t.TempDir()
	s1, err := NewSession(Issue{ID: 1, Number: 101}, sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := NewSession(Issue{ID: 2, Number: 102}, sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureWorktree(Config{RepoDir: repo}, s1); err != nil {
		t.Fatal(err)
	}
	if err := ensureWorktree(Config{RepoDir: repo}, s2); err != nil {
		t.Fatal(err)
	}
	if s1.WorktreeDir == s2.WorktreeDir {
		t.Fatalf("two issues share worktree %s", s1.WorktreeDir)
	}
	onlyOne := filepath.Join(s1.WorktreeDir, "only-one.txt")
	if err := os.WriteFile(onlyOne, []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(s1.WorktreeDir, "add", "only-one.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(s1.WorktreeDir, "-c", "user.name=IterXP Test", "-c", "user.email=iterxp@example.com", "commit", "-m", "one-change"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s2.WorktreeDir, "only-one.txt")); err == nil {
		t.Fatal("issue 101 file appeared in issue 102 worktree")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat issue 102 file: %v", err)
	}
	log, err := runGit(s2.WorktreeDir, "log", "--oneline", "-n", "1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(log, "one-change") {
		t.Fatalf("issue 101 commit reached issue 102 branch: %s", log)
	}
	st1, err := s1.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if st1.WorktreeBranch != "issue/101" {
		t.Fatalf("issue 101 branch = %q", st1.WorktreeBranch)
	}
	st2, err := s2.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if st2.WorktreeBranch != "issue/102" {
		t.Fatalf("issue 102 branch = %q", st2.WorktreeBranch)
	}
}

func TestRunBashExecutesInIssueWorktree(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	sessionDir := t.TempDir()
	s, err := NewSession(Issue{ID: 7, Number: 123}, sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureWorktree(Config{RepoDir: repo}, s); err != nil {
		t.Fatal(err)
	}
	a := &Agent{cfg: &Config{RepoDir: repo}}
	stdout, stderr, code := a.runBash("pwd", s)
	if code != 0 {
		t.Fatalf("runBash pwd exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	want, err := filepath.EvalSymlinks(s.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(stdout)
	gotEval, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("bash pwd output %q is not a path: %v", got, err)
	}
	if gotEval != want {
		t.Fatalf("runBash cwd = %q, want %q", gotEval, want)
	}
}

func TestBuildPromptLoadsIssueInstructions(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "skills", "rca"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("main-agent"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "skills", "rca", "SKILL.md"), []byte("main-skill"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(repo, "add", "AGENTS.md", "skills/rca/SKILL.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(repo, "-c", "user.name=IterXP Test", "-c", "user.email=iterxp@example.com", "commit", "-m", "instructions"); err != nil {
		t.Fatal(err)
	}

	sessionDir := t.TempDir()
	sharedSkills := filepath.Join(sessionDir, "shared-skills")
	if err := os.MkdirAll(filepath.Join(sharedSkills, "rca"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sharedSkills, "rca", "SKILL.md"), []byte("shared-skill"), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := NewSession(Issue{ID: 7, Number: 123}, sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{RepoDir: repo, SkillsDir: sharedSkills}
	if err := ensureWorktree(cfg, s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.WorktreeDir, "AGENTS.md"), []byte("issue-agent"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.WorktreeDir, "skills", "rca", "SKILL.md"), []byte("issue-skill"), 0644); err != nil {
		t.Fatal(err)
	}

	a := &Agent{cfg: &cfg}
	prompt := a.buildPromptForSession(s)
	for _, want := range []string{"issue-agent", "## Skill: rca", "issue-skill"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
	for _, unwanted := range []string{"main-agent", "main-skill", "shared-skill"} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("prompt unexpectedly contains %q", unwanted)
		}
	}
}
