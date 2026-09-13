package main

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
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
	}
}

func main() {
	cfg := loadConfig()
	for _, dir := range []string{cfg.SessionDir, cfg.SkillsDir, cfg.ToolsDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			log.Fatal(err)
		}
	}
	if _, err := os.Stat(cfg.SystemPromptFile); os.IsNotExist(err) {
		if err := os.WriteFile(cfg.SystemPromptFile, []byte(defaultSystemPrompt), 0600); err != nil {
			log.Fatal(err)
		}
	}

	client, err := NewClient(cfg)
	if err != nil {
		log.Fatal(err)
	}
	agent := NewAgent(&cfg, client)
	log.Printf("iterxp-v2 started: model=%s reason=%s sessions=%s", cfg.Model, cfg.Reasoning, cfg.SessionDir)
	agent.Run()
}
