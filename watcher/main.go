package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type procInfo struct {
	cmd        *exec.Cmd
	hash       string
	sourceHash string
	started    time.Time
	accepted   bool
	done       chan error
	stdoutPath string
	stderrPath string
	pgid       int
}

func sourcesNewer(repoDir, bin string) bool {
	binInfo, err := os.Stat(bin)
	if err != nil {
		return true
	}
	if info, err := os.Stat(filepath.Join(repoDir, "go.mod")); err == nil && info.ModTime().After(binInfo.ModTime()) {
		return true
	}
	v2Dir := filepath.Join(repoDir, "v2")
	entries, err := os.ReadDir(v2Dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(binInfo.ModTime()) {
			return true
		}
	}
	return false
}

func buildAgent(cfg Config, dest string) error {
	cmd := exec.Command("go", "build", "-o", dest, "./v2")
	cmd.Dir = cfg.RepoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build ./v2: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 0 {
		return ""
	}
	return s[:max] + "\n...[truncated]"
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return -1
}

func terminate(p *procInfo, timeout time.Duration) {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	pgid := p.pgid
	if pgid <= 0 {
		pgid = p.cmd.Process.Pid
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-p.done:
		return
	case <-time.After(timeout):
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-p.done
	}
}

func startAgent(cfg Config) (*procInfo, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.StatePath), 0700); err != nil {
		return nil, err
	}
	if cfg.RepoDir == "" {
		cfg.RepoDir = "."
	}
	if err := os.MkdirAll(cfg.RepoDir, 0700); err != nil {
		return nil, err
	}
	tag := fmt.Sprintf("%d", time.Now().UnixNano())
	stdoutPath := filepath.Join(filepath.Dir(cfg.StatePath), "agent-"+tag+".stdout.log")
	stderrPath := filepath.Join(filepath.Dir(cfg.StatePath), "agent-"+tag+".stderr.log")
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

	cmd := exec.Command(cfg.AgentPath)
	cmd.Dir = cfg.RepoDir
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	hash, _ := hashFile(cfg.AgentPath)
	if hash == "" {
		hash = "unknown"
	}
	p := &procInfo{
		cmd:        cmd,
		hash:       hash,
		started:    time.Now(),
		done:       make(chan error, 1),
		stdoutPath: stdoutPath,
		stderrPath: stderrPath,
		pgid:       cmd.Process.Pid,
	}
	go func() { p.done <- cmd.Wait() }()
	return p, nil
}

func (p *procInfo) output() (string, string) {
	if p == nil {
		return "", ""
	}
	stdout, _ := os.ReadFile(p.stdoutPath)
	stderr, _ := os.ReadFile(p.stderrPath)
	return string(stdout), string(stderr)
}

func runBounded(name string, args []string, dir string, extraEnv []string, timeout time.Duration) (string, string, int) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	stdoutFile, err := os.CreateTemp("", "iterxp-watcher-stdout-")
	if err != nil {
		return "", err.Error(), -1
	}
	stdoutPath := stdoutFile.Name()
	defer os.Remove(stdoutPath)
	stderrFile, err := os.CreateTemp("", "iterxp-watcher-stderr-")
	if err != nil {
		_ = stdoutFile.Close()
		return "", err.Error(), -1
	}
	stderrPath := stderrFile.Name()
	defer os.Remove(stderrPath)

	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile

	if err := cmd.Start(); err != nil {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return readFileString(stdoutPath), err.Error() + "\n" + readFileString(stderrPath), -1
	}
	_ = stdoutFile.Close()
	_ = stderrFile.Close()

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	timedOut := false
	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-time.After(timeout):
		timedOut = true
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		killTimer := time.NewTimer(5 * time.Second)
		select {
		case waitErr = <-waitCh:
		case <-killTimer.C:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			waitErr = <-waitCh
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	stdout := readFileString(stdoutPath)
	stderr := readFileString(stderrPath)
	if timedOut {
		return stdout, stderr + fmt.Sprintf("\ncommand timed out after %s", timeout), 124
	}
	code := 0
	if waitErr != nil {
		code = exitCode(waitErr)
		if code == -1 {
			stderr += waitErr.Error()
		}
	}
	return stdout, stderr, code
}

func readFileString(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func smokeTestCandidate(cfg Config, candidate, marker string) error {
	marker = strings.TrimSpace(marker)
	if marker == "" {
		return fmt.Errorf("ITERXP_SMOKE_MARKER is empty")
	}
	smokeRoot, err := os.MkdirTemp("", "iterxp-watcher-smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(smokeRoot)

	helpConfig := filepath.Join(smokeRoot, "help-config")
	helpTimeout := cfg.StartTimeout
	if helpTimeout <= 0 {
		helpTimeout = 10 * time.Second
	}
	stdout, stderr, code := runBounded(candidate, []string{"-h"}, cfg.RepoDir, []string{"ITERXP_CONFIG_DIR=" + helpConfig}, helpTimeout)
	if code != 0 {
		return fmt.Errorf("candidate -h failed: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "iterxp-agent-v2") && !strings.Contains(stdout, "Usage") && !strings.Contains(stdout, "help") {
		return fmt.Errorf("candidate -h output did not contain help text: %q", stdout)
	}
	if _, err := os.Stat(helpConfig); !os.IsNotExist(err) {
		return fmt.Errorf("candidate -h created config dir: %v", err)
	}

	smokeConfig := filepath.Join(smokeRoot, "config")
	smokeRepo := filepath.Join(smokeRoot, "repo")
	env := []string{
		"ITERXP_SMOKE_TEST=1",
		"ITERXP_SMOKE_MARKER=" + marker,
		"ITERXP_CONFIG_DIR=" + smokeConfig,
		"ITERXP_REPO_DIR=" + smokeRepo,
	}
	smokeTimeout := cfg.SmokeTimeout
	if smokeTimeout <= 0 {
		smokeTimeout = 15 * time.Second
	}
	stdout, stderr, code = runBounded(candidate, nil, cfg.RepoDir, env, smokeTimeout)
	if code != 0 {
		return fmt.Errorf("candidate smoke cycle failed: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "smoke ok") {
		return fmt.Errorf("candidate smoke cycle missing success marker: stdout=%q stderr=%q", stdout, stderr)
	}
	return nil
}

func promoteCandidate(cfg Config, candidate string) (newHash, oldHash string, err error) {
	if _, err := os.Stat(candidate); err != nil {
		return "", "", err
	}
	oldHash = ""
	if _, statErr := os.Stat(cfg.AgentPath); statErr == nil {
		oldHash, _ = hashFile(cfg.AgentPath)
		if cfg.LastGoodPath != "" {
			if copyErr := copyFile(cfg.AgentPath, cfg.LastGoodPath, 0700); copyErr != nil {
				return "", "", copyErr
			}
		}
	} else if !os.IsNotExist(statErr) {
		return "", "", statErr
	}
	if err := os.Rename(candidate, cfg.AgentPath); err != nil {
		return "", "", err
	}
	newHash, err = hashFile(cfg.AgentPath)
	if err != nil {
		return "", "", err
	}
	return newHash, oldHash, nil
}

func maybeReport(cfg Config, st *State, hash, title, body string) {
	if hash != "" && st.FailedSourceHash == hash {
		log.Printf("already reported failure for %s; skipping GitHub issue", hash)
		return
	}
	if err := createIssue(cfg, title, body); err != nil {
		log.Printf("create GitHub issue failed: %v", err)
		return
	}
	st.FailedSourceHash = hash
	if err := saveState(cfg.StatePath, *st); err != nil {
		log.Printf("save state after issue: %v", err)
	}
}

func main() {
	cfg := loadConfig()
	st, err := loadState(cfg.StatePath)
	if err != nil {
		log.Printf("load watcher state: %v", err)
	}

	var current *procInfo
	lastStartAttempt := time.Time{}

	log.Printf("watcher started: repo=%s agent=%s last_good=%s state=%s", cfg.RepoDir, cfg.AgentPath, cfg.LastGoodPath, cfg.StatePath)

	for {
		sourceHash, sourceHashErr := hashSource(cfg.RepoDir)
		if sourceHashErr != nil {
			log.Printf("hash agent source: %v", sourceHashErr)
		}
		if sourceHash == "" {
			sourceHash = "unknown"
		}
		binMissing := false
		if _, err := os.Stat(cfg.AgentPath); err != nil {
			binMissing = true
		}
		pinnedSource := st.PinnedSourceHash != "" && sourceHash == st.PinnedSourceHash

		if (binMissing || sourcesNewer(cfg.RepoDir, cfg.AgentPath)) && !pinnedSource {
			log.Printf("new agent source detected; building candidate for %s", cfg.AgentPath)
			candidate := cfg.AgentPath + ".candidate"
			retries := cfg.BuildRetries
			if retries <= 0 {
				retries = 1
			}
			var buildErr error
			recheck := false
			for attempt := 0; attempt < retries; attempt++ {
				if attempt > 0 {
					time.Sleep(cfg.PollInterval)
					freshHash, hErr := hashSource(cfg.RepoDir)
					if hErr == nil && freshHash != sourceHash {
						// The source changed while we were retrying; resume the
						// outer loop so we build the new snapshot instead of
						// reporting a transient partial-write failure.
						sourceHash = freshHash
						recheck = true
						break
					}
				}
				buildErr = buildAgent(cfg, candidate)
				if buildErr == nil {
					break
				}
				log.Printf("agent candidate build attempt %d/%d failed: %v", attempt+1, retries, buildErr)
			}
			if recheck && buildErr != nil {
				continue
			}
			if buildErr != nil {
				log.Printf("agent candidate build failed: %v", buildErr)
				st.PinnedSourceHash = sourceHash
				if saveErr := saveState(cfg.StatePath, st); saveErr != nil {
					log.Printf("save state after build failure: %v", saveErr)
				}
				title := "IterXP agent build failed"
				body := fmt.Sprintf("## IterXP agent build failed\n\n- Time: %s\n- Repo: %s\n- Target: %s\n\n```\n%s\n```",
					time.Now().UTC().Format(time.RFC3339), cfg.RepoDir, cfg.AgentPath, truncate(buildErr.Error(), 8000))
				maybeReport(cfg, &st, sourceHash, title, body)
				time.Sleep(cfg.PollInterval)
				continue
			}
			if err := os.Chmod(candidate, 0700); err != nil {
				log.Printf("chmod candidate: %v", err)
			}
			marker := fmt.Sprintf("iterxp-smoke-%d-%s", time.Now().UnixNano(), sourceHash[:minInt(8, len(sourceHash))])
			if err := smokeTestCandidate(cfg, candidate, marker); err != nil {
				log.Printf("agent candidate smoke failed: %v", err)
				st.PinnedSourceHash = sourceHash
				if saveErr := saveState(cfg.StatePath, st); saveErr != nil {
					log.Printf("save state after smoke failure: %v", saveErr)
				}
				title := "IterXP agent smoke test failed"
				body := fmt.Sprintf("## IterXP agent smoke test failed\n\n- Time: %s\n- Candidate: %s\n- Source hash: %s\n\n```\n%s\n```",
					time.Now().UTC().Format(time.RFC3339), candidate, sourceHash, truncate(err.Error(), 8000))
				maybeReport(cfg, &st, sourceHash, title, body)
				time.Sleep(cfg.PollInterval)
				continue
			}

			log.Printf("candidate smoke passed; cutting over %s", cfg.AgentPath)
			if current != nil {
				terminate(current, 5*time.Second)
				current = nil
			}
			newHash, oldHash, promoteErr := promoteCandidate(cfg, candidate)
			if promoteErr != nil {
				log.Printf("promote candidate: %v", promoteErr)
				st.PinnedSourceHash = sourceHash
				if saveErr := saveState(cfg.StatePath, st); saveErr != nil {
					log.Printf("save state after promote failure: %v", saveErr)
				}
				time.Sleep(cfg.PollInterval)
				continue
			}
			if oldHash != "" {
				st.LastGoodHash = oldHash
			}
			st.PinnedSourceHash = ""
			st.Failures = 0
			if saveErr := saveState(cfg.StatePath, st); saveErr != nil {
				log.Printf("save state after promote: %v", saveErr)
			}
			log.Printf("promoted agent candidate hash=%s previous=%s", newHash, oldHash)
		}

		reconcileWorkers(cfg)

		if current == nil && time.Since(lastStartAttempt) >= cfg.PollInterval {
			p, err := startAgent(cfg)
			if err != nil {
				log.Printf("start agent: %v", err)
				st.ActiveHash = "start-error"
				st.Failures++
				log.Printf("agent start failure %d/%d", st.Failures, cfg.MaxFailures)
				if st.Failures < cfg.MaxFailures {
					if saveErr := saveState(cfg.StatePath, st); saveErr != nil {
						log.Printf("save state after start failure: %v", saveErr)
					}
					lastStartAttempt = time.Now()
					time.Sleep(cfg.PollInterval)
					continue
				}
				st.PinnedSourceHash = sourceHash
				title := "IterXP agent failed to start"
				body := fmt.Sprintf("## IterXP agent failed to start\n\n- Time: %s\n- Agent: %s\n\n```\n%s\n```",
					time.Now().UTC().Format(time.RFC3339), cfg.AgentPath, truncate(err.Error(), 8000))
				maybeReport(cfg, &st, sourceHash, title, body)
				if saveErr := saveState(cfg.StatePath, st); saveErr != nil {
					log.Printf("save state after start failure: %v", saveErr)
				}
				if rbErr := rollback(cfg, &st); rbErr != nil {
					log.Printf("rollback after start failures: %v", rbErr)
				} else {
					log.Printf("rolled back to last good agent after start failures")
				}
				lastStartAttempt = time.Now()
				time.Sleep(cfg.PollInterval)
				continue
			}
			current = p
			current.sourceHash = sourceHash
			lastStartAttempt = time.Now()
			st.ActiveHash = p.hash
			if err := saveState(cfg.StatePath, st); err != nil {
				log.Printf("save state after start: %v", err)
			}
			log.Printf("started agent hash=%s", p.hash)
		}

		if current == nil {
			time.Sleep(1 * time.Second)
			continue
		}

		readyTimeout := cfg.StartTimeout
		if readyTimeout <= 0 {
			readyTimeout = 10 * time.Second
		}

		select {
		case waitErr := <-current.done:
			stdout, stderr := current.output()
			code := exitCode(waitErr)
			hash := current.hash
			failedSource := current.sourceHash
			accepted := current.accepted
			current = nil
			log.Printf("agent exited code=%d hash=%s accepted=%v", code, hash, accepted)

			if accepted || hash == st.LastGoodHash {
				log.Printf("accepted or last-good agent exited; restarting without issue")
				continue
			}

			st.Failures++
			log.Printf("agent failure %d/%d for hash=%s", st.Failures, cfg.MaxFailures, hash)
			if st.Failures < cfg.MaxFailures {
				if saveErr := saveState(cfg.StatePath, st); saveErr != nil {
					log.Printf("save state after failure: %v", saveErr)
				}
				continue
			}

			st.PinnedSourceHash = failedSource
			title := "IterXP agent version failed and was rolled back"
			body := fmt.Sprintf("## IterXP agent version failed\n\n- Time: %s\n- Agent: %s\n- Hash: %s\n- Exit code: %d\n\n### stdout\n```\n%s\n```\n\n### stderr\n```\n%s\n```",
				time.Now().UTC().Format(time.RFC3339), cfg.AgentPath, hash, code, truncate(stdout, 8000), truncate(stderr, 8000))
			maybeReport(cfg, &st, hash, title, body)
			if saveErr := saveState(cfg.StatePath, st); saveErr != nil {
				log.Printf("save state after repeated failures: %v", saveErr)
			}
			if rbErr := rollback(cfg, &st); rbErr != nil {
				log.Printf("rollback failed: %v", rbErr)
			} else {
				log.Printf("rolled back to last good agent after repeated failures")
			}

		case <-time.After(cfg.PollInterval):
			if !current.accepted && time.Since(current.started) >= readyTimeout {
				log.Printf("agent hash=%s ready after %s", current.hash, readyTimeout)
				if current.hash != st.LastGoodHash {
					if err := copyFile(cfg.AgentPath, cfg.LastGoodPath, 0700); err != nil {
						log.Printf("copy last good: %v", err)
					} else {
						st.LastGoodHash = current.hash
						log.Printf("updated last good agent to hash=%s", current.hash)
					}
				}
				current.accepted = true
				st.Failures = 0
				if err := saveState(cfg.StatePath, st); err != nil {
					log.Printf("save state after ready: %v", err)
				}
			}
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
