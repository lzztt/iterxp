package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func tmpGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(dir, "init", "-q")
	runGit(dir, "config", "user.name", "Wrong Name")
	runGit(dir, "config", "user.email", "wrong@example.com")
	return dir
}

func TestResolvedGitIdentityDefaultsAndOverride(t *testing.T) {
	name, email := resolvedGitIdentity("", "")
	if name != defaultGitName || email != defaultGitEmail {
		t.Fatalf("empty identity resolved to %q <%s>, want defaults", name, email)
	}
	name, email = resolvedGitIdentity("  ", "\t")
	if name != defaultGitName || email != defaultGitEmail {
		t.Fatalf("whitespace identity resolved to %q <%s>, want defaults", name, email)
	}
	name, email = resolvedGitIdentity("Custom", "custom@example.com")
	if name != "Custom" || email != "custom@example.com" {
		t.Fatalf("override identity = %q <%s>, want Custom <custom@example.com>", name, email)
	}
}

func TestWithGitIdentityEnvReplacesInheritedDefaults(t *testing.T) {
	base := []string{
		"GIT_AUTHOR_NAME=Inherited Author",
		"GIT_AUTHOR_EMAIL=author@example.com",
		"GIT_COMMITTER_NAME=Inherited Committer",
		"GIT_COMMITTER_EMAIL=committer@example.com",
		"PATH=" + os.Getenv("PATH"),
	}
	extra := []string{"GIT_AUTHOR_NAME=Extra Author", "ITERXP_SESSION_DIR=/tmp/session"}
	env := withGitIdentityEnv(base, extra, "IterXP Agent", "agent@iterxp.com")

	count := 0
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		switch key {
		case "GIT_AUTHOR_NAME":
			if kv != "GIT_AUTHOR_NAME=IterXP Agent" {
				t.Fatalf("GIT_AUTHOR_NAME = %q, want configured", kv)
			}
			count++
		case "GIT_AUTHOR_EMAIL":
			if kv != "GIT_AUTHOR_EMAIL=agent@iterxp.com" {
				t.Fatalf("GIT_AUTHOR_EMAIL = %q, want configured", kv)
			}
			count++
		case "GIT_COMMITTER_NAME":
			if kv != "GIT_COMMITTER_NAME=IterXP Agent" {
				t.Fatalf("GIT_COMMITTER_NAME = %q, want configured", kv)
			}
			count++
		case "GIT_COMMITTER_EMAIL":
			if kv != "GIT_COMMITTER_EMAIL=agent@iterxp.com" {
				t.Fatalf("GIT_COMMITTER_EMAIL = %q, want configured", kv)
			}
			count++
		}
	}
	if count != 4 {
		t.Fatalf("expected 4 identity env entries, found %d", count)
	}
}

func TestWithGitIdentityEnvProducesMatchingCommit(t *testing.T) {
	repo := tmpGitRepo(t)
	f := filepath.Join(repo, "file.txt")
	if err := os.WriteFile(f, []byte("content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	env := withGitIdentityEnv(os.Environ(), nil, "IterXP Agent", "agent@iterxp.com")
	cmd := exec.Command("git", "-C", repo, "add", "file.txt")
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	cmd = exec.Command("git", "-C", repo, "commit", "-q", "-m", "identity")
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}

	aname, _ := runGit(repo, "show", "-s", "--format=%an", "HEAD")
	aemail, _ := runGit(repo, "show", "-s", "--format=%ae", "HEAD")
	cname, _ := runGit(repo, "show", "-s", "--format=%cn", "HEAD")
	cemail, _ := runGit(repo, "show", "-s", "--format=%ce", "HEAD")
	if aname != "IterXP Agent" || aemail != "agent@iterxp.com" || cname != "IterXP Agent" || cemail != "agent@iterxp.com" {
		t.Fatalf("commit identity = author %s <%s> committer %s <%s>, want IterXP Agent <agent@iterxp.com>", aname, aemail, cname, cemail)
	}
}

func TestEnsureRepoGitIdentityWritesConfiguredIdentity(t *testing.T) {
	repo := tmpGitRepo(t)
	cfg := Config{RepoDir: repo, GitName: "IterXP Agent", GitEmail: "agent@iterxp.com"}
	if err := ensureRepoGitIdentity(cfg); err != nil {
		t.Fatal(err)
	}
	name, _ := runGit(repo, "config", "--local", "user.name")
	email, _ := runGit(repo, "config", "--local", "user.email")
	if name != "IterXP Agent" || email != "agent@iterxp.com" {
		t.Fatalf("repo identity = %q <%s>, want IterXP Agent <agent@iterxp.com>", name, email)
	}
}
