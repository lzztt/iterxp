package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func writeSessionForWatcherTest(t *testing.T, sessionDir string, number int, contextText string, contextHash string) {
	t.Helper()
	writeSessionForWatcherTestWithPriority(t, sessionDir, number, contextText, contextHash, "")
}

func writeSessionForWatcherTestWithPriority(t *testing.T, sessionDir string, number int, contextText string, contextHash string, priority string) {
	t.Helper()
	dir := filepath.Join(sessionDir, fmt.Sprintf("issue-%d", number))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "context.log"), []byte(contextText), 0600); err != nil {
		t.Fatal(err)
	}
	st := sessionStateFile{IssueNumber: number, ContextHash: contextHash, Priority: priority}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSessionWorkPending(t *testing.T) {
	dir := t.TempDir()
	writeSessionForWatcherTest(t, dir, 1, "Issue #1: do work\n", "")

	rec := discoverSessions(dir)
	if len(rec) != 1 {
		t.Fatalf("discoverSessions count = %d, want 1", len(rec))
	}
	if !sessionWorkPending(rec[0]) {
		t.Fatal("session with new context is not pending")
	}

	contextTwo := "Issue #2: done\n"
	hashedTwo := contextHashString(contextTwo)
	writeSessionForWatcherTest(t, dir, 2, contextTwo, hashedTwo)
	rec = discoverSessions(dir)
	found := false
	for _, r := range rec {
		if r.Number == 2 {
			found = true
			if sessionWorkPending(r) {
				t.Fatal("idle session with matching context hash unexpectedly pending")
			}
		}
	}
	if !found {
		t.Fatal("issue-2 not discovered")
	}
}

func TestReconcileWorkersStartsOnlyPendingWithinLimit(t *testing.T) {
	dir := t.TempDir()
	for _, number := range []int{1, 2, 3} {
		writeSessionForWatcherTest(t, dir, number, fmt.Sprintf("Issue #%d: pending\n", number), "")
	}

	old := startWorkerProcessFn
	t.Cleanup(func() { startWorkerProcessFn = old })

	var started []int
	startWorkerProcessFn = func(cfg Config, issueNumber int) (*workerProcess, error) {
		started = append(started, issueNumber)
		return &workerProcess{issue: issueNumber}, nil
	}

	cfg := Config{SessionDir: dir, MaxWorkers: 2, AgentPath: filepath.Join(dir, "agent")}
	reconcileWorkers(cfg)

	if len(started) != 2 {
		t.Fatalf("started workers = %v, want exactly 2", started)
	}
	if started[0] != 1 || started[1] != 2 {
		t.Fatalf("started workers = %v, want [1 2]", started)
	}
}

func TestReconcileWorkersDoesNotRestartIdleSession(t *testing.T) {
	dir := t.TempDir()
	contextText := "Issue #7: already done\n"
	writeSessionForWatcherTest(t, dir, 7, contextText, contextHashString(contextText))

	old := startWorkerProcessFn
	t.Cleanup(func() { startWorkerProcessFn = old })

	started := 0
	startWorkerProcessFn = func(cfg Config, issueNumber int) (*workerProcess, error) {
		started++
		return &workerProcess{issue: issueNumber}, nil
	}

	cfg := Config{SessionDir: dir, MaxWorkers: 2, AgentPath: filepath.Join(dir, "agent")}
	reconcileWorkers(cfg)

	if started != 0 {
		t.Fatalf("idle session started worker %d times, want 0", started)
	}
}

func TestReconcileWorkersRespectsWorkerLock(t *testing.T) {
	dir := t.TempDir()
	writeSessionForWatcherTest(t, dir, 9, "Issue #9: pending\n", "")

	lockPath := filepath.Join(dir, "issue-9", "worker.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	old := startWorkerProcessFn
	t.Cleanup(func() { startWorkerProcessFn = old })

	started := 0
	startWorkerProcessFn = func(cfg Config, issueNumber int) (*workerProcess, error) {
		started++
		return &workerProcess{issue: issueNumber}, nil
	}

	cfg := Config{SessionDir: dir, MaxWorkers: 2, AgentPath: filepath.Join(dir, "agent")}
	reconcileWorkers(cfg)

	if started != 0 {
		t.Fatalf("locked session started worker %d times, want 0", started)
	}
}

func TestReconcileWorkersRunsUrgentAboveLimitedLimit(t *testing.T) {
	dir := t.TempDir()
	// Exhaust the shared limited queue with 10 non-urgent pending sessions.
	for _, number := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10} {
		writeSessionForWatcherTestWithPriority(t, dir, number, fmt.Sprintf("Issue #%d: pending\n", number), "", "high")
	}
	// One urgent issue that must still run immediately.
	writeSessionForWatcherTestWithPriority(t, dir, 11, "Issue #11: urgent\n", "", "urgent")

	old := startWorkerProcessFn
	t.Cleanup(func() { startWorkerProcessFn = old })

	var started []int
	startWorkerProcessFn = func(cfg Config, issueNumber int) (*workerProcess, error) {
		started = append(started, issueNumber)
		return &workerProcess{issue: issueNumber}, nil
	}

	cfg := Config{SessionDir: dir, MaxWorkers: 10, AgentPath: filepath.Join(dir, "agent")}
	reconcileWorkers(cfg)

	if len(started) != 11 {
		t.Fatalf("started workers = %v, want 11 (10 limited + 1 urgent)", started)
	}
	if started[0] != 11 {
		t.Fatalf("first started worker = %d, want urgent issue 11; started=%v", started[0], started)
	}
}

func TestReconcileWorkersPrefersHighOverLowWithinLimit(t *testing.T) {
	dir := t.TempDir()
	writeSessionForWatcherTestWithPriority(t, dir, 1, "Issue #1: low\n", "", "low")
	writeSessionForWatcherTestWithPriority(t, dir, 2, "Issue #2: high\n", "", "high")
	writeSessionForWatcherTestWithPriority(t, dir, 3, "Issue #3: high\n", "", "high")

	old := startWorkerProcessFn
	t.Cleanup(func() { startWorkerProcessFn = old })

	var started []int
	startWorkerProcessFn = func(cfg Config, issueNumber int) (*workerProcess, error) {
		started = append(started, issueNumber)
		return &workerProcess{issue: issueNumber}, nil
	}

	cfg := Config{SessionDir: dir, MaxWorkers: 2, AgentPath: filepath.Join(dir, "agent")}
	reconcileWorkers(cfg)

	if len(started) != 2 {
		t.Fatalf("started workers = %v, want exactly 2", started)
	}
	if started[0] != 2 || started[1] != 3 {
		t.Fatalf("started workers = %v, want [2 3] (high priority first)", started)
	}
}
