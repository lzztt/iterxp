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
	InferenceTimeout        time.Duration
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
		MaxTokens:               getenvInt("ITERXP_MAX_TOKENS", 32768),
		InferenceTimeout:        getenvDuration("ITERXP_INFERENCE_TIMEOUT", 300*time.Second),
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

The IterXP v2 agent runs in two modes:

  No --issue flag:      dispatcher. Polls open GitHub issues and comments,
                        appends new input to per-issue sessions, and prepares
                        per-issue worktrees. It does not execute model/tool steps.
  --issue N:            issue worker. Loads only issue N's session and worktree,
                        executes that session's pending steps, then exits.

Flags:
  -h, --help    Show this help and exit.
  --issue N     Run the issue worker for issue N (N > 0).
  --issue=N     Equivalent to --issue N.
`

func parseArgs(args []string) (help bool, issueNumber int, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-h" || arg == "--help" {
			return true, 0, nil
		}
		if arg == "--issue" {
			if i+1 >= len(args) {
				return false, 0, fmt.Errorf("--issue requires a positive issue number")
			}
			n, convErr := strconv.Atoi(args[i+1])
			if convErr != nil || n <= 0 {
				return false, 0, fmt.Errorf("invalid issue number: %s", args[i+1])
			}
			if issueNumber != 0 {
				return false, 0, fmt.Errorf("--issue specified multiple times")
			}
			issueNumber = n
			i++
			continue
		}
		if strings.HasPrefix(arg, "--issue=") {
			value := strings.TrimPrefix(arg, "--issue=")
			n, convErr := strconv.Atoi(value)
			if convErr != nil || n <= 0 {
				return false, 0, fmt.Errorf("invalid issue number: %s", value)
			}
			if issueNumber != 0 {
				return false, 0, fmt.Errorf("--issue specified multiple times")
			}
			issueNumber = n
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return false, 0, fmt.Errorf("unknown flag: %s", arg)
		}
		return false, 0, fmt.Errorf("unexpected argument: %s", arg)
	}
	return false, issueNumber, nil
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
	help, issueNumber, err := parseArgs(args)
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

	if issueNumber > 0 {
		return runIssueWorker(cfg, issueNumber)
	}

	client, err := NewClient(cfg)
	if err != nil {
		log.Fatal(err)
	}
	agent := NewAgent(&cfg, client)
	log.Printf("iterxp-v2 dispatcher started: model=%s reason=%s maxTokens=%d inferenceTimeout=%s sessions=%s", cfg.Model, cfg.Reasoning, cfg.MaxTokens, cfg.InferenceTimeout, cfg.SessionDir)
	agent.RunDispatcher()
	return 0
}

func workerVersion() string {
	path, err := os.Executable()
	if err != nil {
		return promptVersion + "-unknown-path"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return promptVersion + "-unreadable"
	}
	return promptVersion + "-" + hashString(string(data))[:12]
}

func runIssueWorker(cfg Config, issueNumber int) int {
	session, err := NewSession(Issue{Number: issueNumber}, cfg.SessionDir)
	if err != nil {
		log.Printf("create issue %d session: %v", issueNumber, err)
		return 1
	}

	client, err := NewClient(cfg)
	if err != nil {
		log.Printf("create issue %d client: %v", issueNumber, err)
		return 1
	}

	unlock, err := acquireSessionWorkerLock(session)
	if err != nil {
		log.Printf("issue %d worker already active: %v", issueNumber, err)
		return 0
	}
	defer unlock()

	if err := ensureWorktree(cfg, session); err != nil {
		log.Printf("issue %d worktree setup: %v", issueNumber, err)
		return 1
	}

	agent := NewIssueAgent(&cfg, client, session)
	version := workerVersion()
	if err := updateWorkerState(session, version, "starting", "", time.Time{}); err != nil {
		log.Printf("issue %d write worker state: %v", issueNumber, err)
		return 1
	}
	keepWorkerStatus := false
	defer func() {
		if !keepWorkerStatus {
			_ = clearWorkerState(session)
		}
	}()

	for sessionHasPendingWork(session) {
		if err := ensureWorktree(cfg, session); err != nil {
			log.Printf("issue %d worktree refresh: %v", issueNumber, err)
			return 1
		}
		deadline := time.Now().Add(cfg.ToolTimeout + 2*time.Minute)
		if err := updateWorkerState(session, version, "working", "model-tool-step", deadline); err != nil {
			log.Printf("issue %d heartbeat: %v", issueNumber, err)
		}
		agent.runSession(session)
		if sessionHasPendingWork(session) {
			time.Sleep(1 * time.Second)
		}
	}

	if err := updateWorkerState(session, version, "idle", "", time.Time{}); err != nil {
		log.Printf("issue %d persist idle status: %v", issueNumber, err)
	}
	keepWorkerStatus = true
	log.Printf("issue %d worker finished", issueNumber)
	return 0
}

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}
