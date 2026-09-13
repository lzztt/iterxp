package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type workerStateFile struct {
	PID           int       `json:"pid,omitempty"`
	ProcessStart  string    `json:"process_start,omitempty"`
	Version       string    `json:"version,omitempty"`
	Status        string    `json:"status,omitempty"`
	Operation     string    `json:"operation,omitempty"`
	Deadline      time.Time `json:"deadline,omitempty"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	LastHeartbeat time.Time `json:"last_heartbeat,omitempty"`
}

type sessionStateFile struct {
	IssueID            int64            `json:"issue_id"`
	IssueNumber        int              `json:"issue_number"`
	Title              string           `json:"title"`
	Priority           string           `json:"priority,omitempty"`
	WorktreePath       string           `json:"worktree_path,omitempty"`
	WorktreeBranch     string           `json:"worktree_branch,omitempty"`
	WorktreeBaseCommit string           `json:"worktree_base_commit,omitempty"`
	ContextHash        string           `json:"context_hash"`
	PendingContextHash string           `json:"pending_context_hash,omitempty"`
	Done               bool             `json:"done"`
	Worker             *workerStateFile `json:"worker,omitempty"`
}

type sessionDiscovery struct {
	Number      int
	Dir         string
	StatePath   string
	ContextPath string
	LockPath    string
	State       sessionStateFile
	Context     string
}

func discoverSessions(sessionDir string) []sessionDiscovery {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return nil
	}
	var out []sessionDiscovery
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "issue-") {
			continue
		}
		numText := strings.TrimPrefix(entry.Name(), "issue-")
		num, err := strconv.Atoi(numText)
		if err != nil || num <= 0 {
			continue
		}
		dir := filepath.Join(sessionDir, entry.Name())
		rec := sessionDiscovery{
			Number:      num,
			Dir:         dir,
			StatePath:   filepath.Join(dir, "state.json"),
			ContextPath: filepath.Join(dir, "context.log"),
			LockPath:    filepath.Join(dir, "worker.lock"),
		}
		if data, err := os.ReadFile(rec.StatePath); err == nil {
			_ = json.Unmarshal(data, &rec.State)
		}
		if rec.State.IssueNumber == 0 {
			rec.State.IssueNumber = num
		}
		if data, err := os.ReadFile(rec.ContextPath); err == nil {
			rec.Context = string(data)
		}
		out = append(out, rec)
	}
	return out
}

func contextHashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func sessionWorkPending(rec sessionDiscovery) bool {
	if strings.TrimSpace(rec.Context) == "" {
		return false
	}
	if rec.State.PendingContextHash != "" {
		return true
	}
	return rec.State.ContextHash != contextHashString(rec.Context)
}

func processStartIdentity(pid int) (string, bool) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", false
	}
	text := string(data)
	idx := strings.LastIndex(text, ")")
	if idx < 0 || idx+2 >= len(text) {
		return "", false
	}
	fields := strings.Fields(text[idx+2:])
	if len(fields) < 20 {
		return "", false
	}
	return fields[19], true
}

func processHasIssueArg(pid, issueNumber int) bool {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false
	}
	args := strings.Split(string(data), "\x00")
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--issue" {
			if i+1 >= len(args) {
				return false
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n != issueNumber {
				return false
			}
			return true
		}
		if strings.HasPrefix(arg, "--issue=") {
			n, err := strconv.Atoi(strings.TrimPrefix(arg, "--issue="))
			if err != nil || n != issueNumber {
				return false
			}
			return true
		}
	}
	return false
}

func workerProcessMatches(cfg Config, pid int, processStart string, issueNumber int) bool {
	if pid <= 0 || processStart == "" {
		return false
	}
	actualStart, ok := processStartIdentity(pid)
	if !ok || actualStart != processStart {
		return false
	}
	if !processHasIssueArg(pid, issueNumber) {
		return false
	}
	exe, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	if err != nil {
		return false
	}
	exe = strings.TrimSuffix(exe, " (deleted)")
	agentPath := filepath.Clean(cfg.AgentPath)
	if filepath.Clean(exe) != agentPath && filepath.Base(exe) != filepath.Base(agentPath) {
		return false
	}
	return true
}
func workerIdentityValid(cfg Config, rec sessionDiscovery) bool {
	if rec.State.Worker == nil {
		return false
	}
	return workerProcessMatches(cfg, rec.State.Worker.PID, rec.State.Worker.ProcessStart, rec.Number)
}

func processGroupID(pid int) (int, bool) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	text := string(data)
	idx := strings.LastIndex(text, ")")
	if idx < 0 || idx+2 >= len(text) {
		return 0, false
	}
	fields := strings.Fields(text[idx+2:])
	if len(fields) < 3 {
		return 0, false
	}
	groupID, err := strconv.Atoi(fields[2])
	if err != nil || groupID <= 0 {
		return 0, false
	}
	return groupID, true
}

func terminateWorkerPID(pid int) {
	if pid <= 0 {
		return
	}
	if groupID, ok := processGroupID(pid); ok {
		_ = syscall.Kill(-groupID, syscall.SIGTERM)
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
}

func workerLockHeld(lockPath string) bool {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return true
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

func workerIsLive(cfg Config, rec sessionDiscovery) bool {
	if rec.State.Worker == nil {
		return false
	}
	w := rec.State.Worker
	if !workerProcessMatches(cfg, w.PID, w.ProcessStart, rec.Number) {
		return false
	}
	if !workerLockHeld(rec.LockPath) {
		return false
	}
	if !w.Deadline.IsZero() && time.Now().After(w.Deadline) {
		return false
	}
	return true
}

func startWorkerProcess(cfg Config, issueNumber int) (*workerProcess, error) {
	stdoutPath := filepath.Join(filepath.Dir(cfg.StatePath), fmt.Sprintf("worker-issue-%d-%d.stdout.log", issueNumber, time.Now().UnixNano()))
	stderrPath := filepath.Join(filepath.Dir(cfg.StatePath), fmt.Sprintf("worker-issue-%d-%d.stderr.log", issueNumber, time.Now().UnixNano()))
	stdout, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return nil, err
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return nil, err
	}
	defer stderr.Close()

	cmd := execCommand(cfg.AgentPath, "--issue", strconv.Itoa(issueNumber))
	cmd.Dir = cfg.RepoDir
	cmd.Env = watcherIdentityEnv(os.Environ(), cfg.GitName, cfg.GitEmail)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	wp := &workerProcess{
		issue:      issueNumber,
		cmd:        cmd,
		done:       make(chan error, 1),
		stdoutPath: stdoutPath,
		stderrPath: stderrPath,
		pgid:       cmd.Process.Pid,
	}
	go func() { wp.done <- cmd.Wait() }()
	return wp, nil
}
