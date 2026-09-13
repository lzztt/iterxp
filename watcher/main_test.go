package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestRunBoundedTimesOut(t *testing.T) {
	start := time.Now()
	_, stderr, code := runBounded("sh", []string{"-c", "sleep 10"}, t.TempDir(), nil, 150*time.Millisecond)
	if code != 124 {
		t.Fatalf("exit code = %d, want 124; stderr=%q", code, stderr)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("runBounded took too long: %s", time.Since(start))
	}
	if !strings.Contains(stderr, "timed out") {
		t.Fatalf("stderr missing timeout marker: %q", stderr)
	}
}

func TestTerminateDoesNotHangOnOutputHoldingDescendant(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "worker")
	writeExecutable(t, script, "#!/bin/sh\nsleep 30 &\nwait\n")
	cfg := Config{
		AgentPath: script,
		RepoDir:   dir,
		StatePath: filepath.Join(dir, "state", "watcher_state.json"),
	}
	p, err := startAgent(cfg)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	terminate(p, 2*time.Second)
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("terminate took too long: %s", elapsed)
	}
}

func TestPromoteCandidateKeepsPreviousBinary(t *testing.T) {
	dir := t.TempDir()
	agent := filepath.Join(dir, "agent")
	lastGood := filepath.Join(dir, "agent.last_good")
	candidate := filepath.Join(dir, "candidate")
	writeExecutable(t, agent, "#!/bin/sh\necho old\n")
	writeExecutable(t, candidate, "#!/bin/sh\necho new\n")
	cfg := Config{AgentPath: agent, LastGoodPath: lastGood}
	if _, _, err := promoteCandidate(cfg, candidate); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(lastGood)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "old") {
		t.Fatalf("last good binary was not preserved: %q", string(data))
	}
	data, err = os.ReadFile(agent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "new") {
		t.Fatalf("candidate was not promoted: %q", string(data))
	}
}

func TestSmokeCandidateRejectsFailingCandidate(t *testing.T) {
	dir := t.TempDir()
	candidate := filepath.Join(dir, "candidate")
	writeExecutable(t, candidate, `#!/bin/sh
if [ "$1" = "-h" ]; then echo "iterxp-agent-v2 [flags]"; exit 0; fi
if [ "$ITERXP_SMOKE_TEST" = "1" ]; then echo "bad output"; exit 0; fi
`)
	cfg := Config{RepoDir: dir, StartTimeout: 2 * time.Second, SmokeTimeout: 2 * time.Second}
	if err := smokeTestCandidate(cfg, candidate, "marker-123"); err == nil {
		t.Fatal("smokeTestCandidate unexpectedly passed a failing candidate")
	}
	if _, err := os.Stat(cfg.RepoDir); err != nil {
		t.Fatalf("repo dir unexpectedly removed: %v", err)
	}
}

func TestSmokeCandidatePassesWorkingCandidate(t *testing.T) {
	dir := t.TempDir()
	candidate := filepath.Join(dir, "candidate")
	writeExecutable(t, candidate, fmt.Sprintf(`#!/bin/sh
if [ "$1" = "-h" ]; then echo "iterxp-agent-v2 [flags]"; exit 0; fi
if [ "$ITERXP_SMOKE_TEST" = "1" ]; then printf '%%s' "$ITERXP_SMOKE_MARKER" >/dev/null; echo "smoke ok"; exit 0; fi
`))
	cfg := Config{RepoDir: dir, StartTimeout: 2 * time.Second, SmokeTimeout: 2 * time.Second}
	if err := smokeTestCandidate(cfg, candidate, "marker-123"); err != nil {
		t.Fatal(err)
	}
}

func TestBuildAgentFailureLeavesCurrentWorkerRunning(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(dir, "worker")
	writeExecutable(t, agentPath, "#!/bin/sh\nsleep 30 &\nwait\n")
	cfg := Config{
		AgentPath: agentPath,
		RepoDir:   dir,
		StatePath: filepath.Join(stateDir, "watcher_state.json"),
	}
	p, err := startAgent(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer terminate(p, 2*time.Second)

	cfg.RepoDir = filepath.Join(dir, "invalid-repo")
	candidate := filepath.Join(dir, "candidate")
	if err := buildAgent(cfg, candidate); err == nil {
		t.Fatal("buildAgent unexpectedly succeeded in an invalid repo")
	}
	if err := p.cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("current worker is no longer running after failed candidate build: %v", err)
	}
}
