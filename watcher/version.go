package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Repo            string
	GitHubTokenPath string
	RepoDir         string
	AgentPath       string
	LastGoodPath    string
	StatePath       string
	SessionDir      string
	PollInterval    time.Duration
	StableWindow    time.Duration
	StartTimeout    time.Duration
	SmokeTimeout    time.Duration
	MaxFailures     int
	MaxWorkers      int
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func getenvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func loadConfig() Config {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	repoDir := getenv("ITERXP_REPO_DIR", filepath.Join(home, "iterxp"))
	configDir := getenv("ITERXP_WATCHER_CONFIG_DIR", filepath.Join(home, ".iterxp_v2"))
	return Config{
		Repo:            getenv("ITERXP_REPO", "lzztt/iterxp"),
		GitHubTokenPath: getenv("ITERXP_GITHUB_TOKEN", filepath.Join(home, "token", "github")),
		RepoDir:         repoDir,
		AgentPath:       getenv("ITERXP_AGENT_BIN", filepath.Join(repoDir, "iterxp-agent-v2")),
		LastGoodPath:    getenv("ITERXP_LAST_GOOD_BIN", filepath.Join(repoDir, "iterxp-agent-v2.last_good")),
		StatePath:       getenv("ITERXP_WATCHER_STATE", filepath.Join(configDir, "watcher_state.json")),
		SessionDir:      getenv("ITERXP_WATCHER_SESSION_DIR", filepath.Join(configDir, "sessions")),
		PollInterval:    getenvDuration("ITERXP_WATCHER_POLL_INTERVAL", 5*time.Second),
		StableWindow:    getenvDuration("ITERXP_WATCHER_STABLE_WINDOW", 30*time.Second),
		StartTimeout:    getenvDuration("ITERXP_WATCHER_START_TIMEOUT", 10*time.Second),
		SmokeTimeout:    getenvDuration("ITERXP_WATCHER_SMOKE_TIMEOUT", 15*time.Second),
		MaxFailures:     getenvInt("ITERXP_WATCHER_MAX_FAILURES", 3),
		MaxWorkers:      getenvInt("ITERXP_MAX_WORKERS", 2),
	}
}

type State struct {
	ActiveHash       string    `json:"active_hash"`
	LastGoodHash     string    `json:"last_good_hash"`
	FailedSourceHash string    `json:"failed_source_hash,omitempty"`
	PinnedSourceHash string    `json:"pinned_source_hash,omitempty"`
	Failures         int       `json:"failures"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashSource(repoDir string) (string, error) {
	h := sha256.New()
	goMod := filepath.Join(repoDir, "go.mod")
	if data, err := os.ReadFile(goMod); err == nil {
		h.Write([]byte("go.mod\x00"))
		h.Write(data)
	}
	v2Dir := filepath.Join(repoDir, "v2")
	entries, err := os.ReadDir(v2Dir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(v2Dir, entry.Name()))
		if err != nil {
			continue
		}
		h.Write([]byte(entry.Name()))
		h.Write([]byte{0})
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func loadState(path string) (State, error) {
	var st State
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return st, err
	}
	if len(data) == 0 {
		return st, nil
	}
	err = json.Unmarshal(data, &st)
	return st, err
}

func saveState(path string, st State) error {
	st.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func rollback(cfg Config, st *State) error {
	if _, err := os.Stat(cfg.LastGoodPath); err != nil {
		return fmt.Errorf("last good binary unavailable: %w", err)
	}
	if err := copyFile(cfg.LastGoodPath, cfg.AgentPath, 0700); err != nil {
		return err
	}
	h, err := hashFile(cfg.LastGoodPath)
	if err == nil {
		st.ActiveHash = h
	}
	st.Failures = 0
	return saveState(cfg.StatePath, *st)
}
