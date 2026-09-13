package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Config struct {
	Repo                    string
	Model                   string
	Reasoning               string
	MaxTokens               int
	APIBase                 string
	ProjectHeader           string
	HomeDir                 string
	RepoDir                 string
	ConfigDir               string
	SessionDir              string
	SkillsDir               string
	ToolsDir                string
	SystemPromptFile        string
	PollInterval            time.Duration
	ContextMaxTokens        int
	ContextCompactThreshold int
	ContextKeepTokens       int
	ToolTimeout             time.Duration
	KillTimeout             time.Duration
	RetryDelay              time.Duration
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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

func loadConfig() Config {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	base := getenv("ITERXP_CONFIG_DIR", filepath.Join(home, ".iterxp_v2"))
	return Config{
		Repo:                    getenv("ITERXP_REPO", "lzztt/iterxp"),
		Model:                   getenv("ITERXP_MODEL", "deepseek-ai/DeepSeek-V4-Pro-0813"),
		Reasoning:               getenv("ITERXP_REASONING_EFFORT", "high"),
		MaxTokens:               getenvInt("ITERXP_MAX_TOKENS", 16384),
		APIBase:                 getenv("ITERXP_WANDB_API", "https://api.inference.wandb.ai/v1/chat/completions"),
		ProjectHeader:           getenv("ITERXP_PROJECT_HEADER", "OpenAI-Project: longti/inference"),
		HomeDir:                 home,
		RepoDir:                 getenv("ITERXP_REPO_DIR", filepath.Join(home, "iterxp")),
		ConfigDir:               base,
		SessionDir:              filepath.Join(base, "sessions"),
		SkillsDir:               filepath.Join(base, "skills"),
		ToolsDir:                filepath.Join(base, "tools"),
		SystemPromptFile:        filepath.Join(base, "system_prompt.md"),
		PollInterval:            30 * time.Second,
		ContextMaxTokens:        getenvInt("ITERXP_CONTEXT_MAX_TOKENS", 1_000_000),
		ContextCompactThreshold: getenvInt("ITERXP_CONTEXT_COMPACT_THRESHOLD", 800_000),
		ContextKeepTokens:       getenvInt("ITERXP_CONTEXT_KEEP_TOKENS", 200_000),
		ToolTimeout:             getenvDuration("ITERXP_TOOL_TIMEOUT", 2*time.Minute),
		KillTimeout:             getenvDuration("ITERXP_KILL_TIMEOUT", 5*time.Second),
		RetryDelay:              getenvDuration("ITERXP_RETRY_DELAY", 5*time.Second),
	}
}

const usageText = `iterxp-agent-v2 [flags]

The IterXP v2 agent polls open GitHub issues and works on one issue at a time.

Flags:
  -h, --help    Show this help and exit.
`

func parseArgs(args []string) (help bool, err error) {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true, nil
		}
		if strings.HasPrefix(arg, "-") {
			return false, fmt.Errorf("unknown flag: %s", arg)
		}
		return false, fmt.Errorf("unexpected argument: %s", arg)
	}
	return false, nil
}

func acquireWorkerLock(configDir string) (func(), error) {
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(configDir, ".worker.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("config dir %s is already in use by another worker: %w", configDir, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

func runSmokeTest(cfg Config, marker string) error {
	marker = strings.TrimSpace(marker)
	if marker == "" {
		return fmt.Errorf("ITERXP_SMOKE_MARKER is empty")
	}
	session, err := NewSession(Issue{Number: 999901}, cfg.SessionDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.RepoDir, 0700); err != nil {
		return err
	}
	contextText := "Run the marker-printing command. The marker value is only in ITERPXP_SMOKE_MARKER.\n"
	if err := os.WriteFile(session.ContextPath, []byte(contextText), 0600); err != nil {
		return err
	}
	cfg.RepoDir = session.Dir
	agent := &Agent{cfg: &cfg}
	stdout, stderr, code := agent.runBash(`printf '%s' "$ITERXP_SMOKE_MARKER"`, session)
	if code != 0 || stdout != marker {
		return fmt.Errorf("smoke tool cycle failed: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.Contains(contextText, marker) {
		return fmt.Errorf("smoke marker was already present in input")
	}
	result := fmt.Sprintf("$ printf '%%s' \"$ITERXP_SMOKE_MARKER\"\nstdout:\n%s\nstderr:\n%s\nexit_code: %d", stdout, stderr, code)
	if err := session.AppendContext(result); err != nil {
		return err
	}
	st, err := session.LoadState()
	if err != nil {
		return err
	}
	st.IssueID = 999901
	st.IssueNumber = 999901
	st.ContextHash = hashString(contextText)
	st.PendingContextHash = ""
	st.Done = false
	return session.SaveState(st)
}

func runCLI(args []string, stdout, stderr io.Writer) int {
	help, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "iterxp-agent-v2: %v\n", err)
		return 2
	}
	if help {
		fmt.Fprint(stdout, usageText)
		return 0
	}

	if os.Getenv("ITERXP_SMOKE_TEST") == "1" {
		cfg := loadConfig()
		if err := runSmokeTest(cfg, os.Getenv("ITERXP_SMOKE_MARKER")); err != nil {
			fmt.Fprintf(stderr, "smoke test failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "smoke ok")
		return 0
	}

	cfg := loadConfig()
	for _, dir := range []string{cfg.SessionDir, cfg.SkillsDir, cfg.ToolsDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			log.Fatal(err)
		}
	}
	needsPromptWrite := false
	if data, err := os.ReadFile(cfg.SystemPromptFile); err != nil {
		needsPromptWrite = true
	} else if !strings.Contains(string(data), promptVersion) {
		needsPromptWrite = true
	}
	if needsPromptWrite {
		if err := os.WriteFile(cfg.SystemPromptFile, []byte(defaultSystemPrompt), 0600); err != nil {
			log.Fatal(err)
		}
	}

	unlock, err := acquireWorkerLock(cfg.ConfigDir)
	if err != nil {
		log.Printf("%v", err)
		return 1
	}
	defer unlock()

	client, err := NewClient(cfg)
	if err != nil {
		log.Fatal(err)
	}
	agent := NewAgent(&cfg, client)
	log.Printf("iterxp-v2 started: model=%s reason=%s sessions=%s", cfg.Model, cfg.Reasoning, cfg.SessionDir)
	agent.Run()
	return 0
}

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}
