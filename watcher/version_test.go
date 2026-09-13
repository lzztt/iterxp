package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bin")
	if err := os.WriteFile(p, []byte("hello"), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := hashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 64 {
		t.Fatalf("hash length = %d, want 64", len(got))
	}
}

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("payload"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "payload" {
		t.Fatalf("dst = %q, want payload", data)
	}
}

func TestRollbackUsesLastGood(t *testing.T) {
	dir := t.TempDir()
	agent := filepath.Join(dir, "agent")
	lastGood := filepath.Join(dir, "last-good")
	state := filepath.Join(dir, "state.json")
	if err := os.WriteFile(agent, []byte("bad"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lastGood, []byte("good"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		AgentPath:    agent,
		LastGoodPath: lastGood,
		StatePath:    state,
	}
	st := State{Failures: 4, UpdatedAt: time.Now()}
	if err := rollback(cfg, &st); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(agent)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "good" {
		t.Fatalf("agent = %q, want good", data)
	}
	if st.Failures != 0 {
		t.Fatalf("failures = %d, want 0", st.Failures)
	}
}

func TestLoadConfigUsesEnv(t *testing.T) {
	repoDir := t.TempDir()
	t.Setenv("ITERXP_REPO_DIR", repoDir)
	t.Setenv("ITERXP_AGENT_BIN", filepath.Join(repoDir, "custom-agent"))
	t.Setenv("ITERXP_LAST_GOOD_BIN", filepath.Join(repoDir, "last-good-agent"))
	t.Setenv("ITERXP_WATCHER_STATE", filepath.Join(repoDir, "watch-state.json"))
	t.Setenv("ITERXP_WATCHER_POLL_INTERVAL", "11s")
	t.Setenv("ITERXP_WATCHER_STABLE_WINDOW", "17s")
	t.Setenv("ITERXP_WATCHER_START_TIMEOUT", "3s")
	t.Setenv("ITERXP_WATCHER_MAX_FAILURES", "9")

	cfg := loadConfig()
	if cfg.RepoDir != repoDir {
		t.Fatalf("RepoDir = %q, want %q", cfg.RepoDir, repoDir)
	}
	if cfg.AgentPath != filepath.Join(repoDir, "custom-agent") {
		t.Fatalf("AgentPath = %q", cfg.AgentPath)
	}
	if cfg.LastGoodPath != filepath.Join(repoDir, "last-good-agent") {
		t.Fatalf("LastGoodPath = %q", cfg.LastGoodPath)
	}
	if cfg.StatePath != filepath.Join(repoDir, "watch-state.json") {
		t.Fatalf("StatePath = %q", cfg.StatePath)
	}
	if cfg.PollInterval != 11*time.Second {
		t.Fatalf("PollInterval = %v, want 11s", cfg.PollInterval)
	}
	if cfg.StableWindow != 17*time.Second {
		t.Fatalf("StableWindow = %v, want 17s", cfg.StableWindow)
	}
	if cfg.StartTimeout != 3*time.Second {
		t.Fatalf("StartTimeout = %v, want 3s", cfg.StartTimeout)
	}
	if cfg.MaxFailures != 9 {
		t.Fatalf("MaxFailures = %d, want 9", cfg.MaxFailures)
	}
}

func TestHashSourceChangesWhenFilesChange(t *testing.T) {
	dir := t.TempDir()
	v2Dir := filepath.Join(dir, "v2")
	if err := os.MkdirAll(v2Dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n"), 0600); err != nil {
		t.Fatal(err)
	}
	agentFile := filepath.Join(v2Dir, "agent.go")
	if err := os.WriteFile(agentFile, []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h1, err := hashSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == "" {
		t.Fatal("empty source hash")
	}
	if err := os.WriteFile(agentFile, []byte("package main\n// changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h2, err := hashSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatalf("source hash did not change after file change: %s", h1)
	}
}

func TestLoadConfigGitIdentityDefaultsAndOverride(t *testing.T) {
	repoDir := t.TempDir()
	t.Setenv("ITERXP_REPO_DIR", repoDir)
	t.Setenv("ITERXP_GIT_NAME", "")
	t.Setenv("ITERXP_GIT_EMAIL", "")

	cfg := loadConfig()
	if cfg.GitName != "IterXP Agent" || cfg.GitEmail != "agent@iterxp.com" {
		t.Fatalf("default identity = %q <%s>, want IterXP Agent <agent@iterxp.com>", cfg.GitName, cfg.GitEmail)
	}

	t.Setenv("ITERXP_GIT_NAME", "Custom Agent")
	t.Setenv("ITERXP_GIT_EMAIL", "custom@example.com")
	cfg = loadConfig()
	if cfg.GitName != "Custom Agent" || cfg.GitEmail != "custom@example.com" {
		t.Fatalf("overridden identity = %q <%s>, want Custom Agent <custom@example.com>", cfg.GitName, cfg.GitEmail)
	}
}
