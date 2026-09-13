package main

import (
	"bytes"
	"fmt"
	"io"
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
	stdout     *bytes.Buffer
	stderr     *bytes.Buffer
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
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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

func terminate(p *procInfo) {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

func startAgent(cfg Config) (*procInfo, error) {
	cmd := exec.Command(cfg.AgentPath)
	cmd.Dir = cfg.RepoDir
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &stdout)
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	hash, _ := hashFile(cfg.AgentPath)
	if hash == "" {
		hash = "unknown"
	}
	p := &procInfo{
		cmd:     cmd,
		hash:    hash,
		started: time.Now(),
		done:    make(chan error, 1),
		stdout:  &stdout,
		stderr:  &stderr,
	}
	go func() { p.done <- cmd.Wait() }()
	return p, nil
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
			if current != nil {
				terminate(current)
				current = nil
			}
			log.Printf("new agent source detected; building %s", cfg.AgentPath)
			tmp := cfg.AgentPath + ".new"
			if err := buildAgent(cfg, tmp); err != nil {
				log.Printf("agent build failed: %v", err)
				title := "IterXP agent build failed"
				body := fmt.Sprintf("## IterXP agent build failed\n\n- Time: %s\n- Repo: %s\n- Target: %s\n\n```\n%s\n```",
					time.Now().UTC().Format(time.RFC3339), cfg.RepoDir, cfg.AgentPath, truncate(err.Error(), 8000))
				st.PinnedSourceHash = sourceHash
				maybeReport(cfg, &st, sourceHash, title, body)
				if saveErr := saveState(cfg.StatePath, st); saveErr != nil {
					log.Printf("save state after build failure: %v", saveErr)
				}
				if rbErr := rollback(cfg, &st); rbErr != nil {
					log.Printf("rollback after build failure: %v", rbErr)
				} else {
					log.Printf("rolled back to last good agent after build failure")
				}
				time.Sleep(cfg.PollInterval)
				continue
			}
			if err := os.Chmod(tmp, 0700); err != nil {
				log.Printf("chmod candidate: %v", err)
			}
			if err := os.Rename(tmp, cfg.AgentPath); err != nil {
				log.Printf("replace agent binary: %v", err)
				time.Sleep(cfg.PollInterval)
				continue
			}
			log.Printf("built new agent binary: %s", cfg.AgentPath)
			if st.PinnedSourceHash != "" {
				st.PinnedSourceHash = ""
				if saveErr := saveState(cfg.StatePath, st); saveErr != nil {
					log.Printf("save state after source unpin: %v", saveErr)
				}
			}
		}

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

		select {
		case waitErr := <-current.done:
			code := exitCode(waitErr)
			stdout := truncate(current.stdout.String(), 8000)
			stderr := truncate(current.stderr.String(), 8000)
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
				time.Now().UTC().Format(time.RFC3339), cfg.AgentPath, hash, code, stdout, stderr)
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
			if !current.accepted && time.Since(current.started) >= cfg.StableWindow {
				log.Printf("agent hash=%s stable for %s", current.hash, cfg.StableWindow)
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
					log.Printf("save state after stable: %v", err)
				}
			}
		}
	}
}
