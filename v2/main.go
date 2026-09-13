package main

import (
	"log"
	"os"
	"path/filepath"
	"time"
)

type Config struct {
	Repo             string
	Model            string
	Reasoning        string
	MaxTokens        int
	APIBase          string
	ProjectHeader    string
	HomeDir          string
	ConfigDir        string
	SessionDir       string
	SkillsDir        string
	ToolsDir         string
	SystemPromptFile string
	PollInterval     time.Duration
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func loadConfig() Config {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	base := filepath.Join(home, ".iterxp_v2")
	return Config{
		Repo:             getenv("ITERXP_REPO", "lzztt/iterxp"),
		Model:            getenv("ITERXP_MODEL", "deepseek-ai/DeepSeek-V4-Pro-0813"),
		Reasoning:        getenv("ITERXP_REASONING_EFFORT", "high"),
		MaxTokens:        16384,
		APIBase:          getenv("ITERXP_WANDB_API", "https://api.inference.wandb.ai/v1/chat/completions"),
		ProjectHeader:    getenv("ITERXP_PROJECT_HEADER", "OpenAI-Project: longti/inference"),
		HomeDir:          home,
		ConfigDir:        base,
		SessionDir:       filepath.Join(base, "sessions"),
		SkillsDir:        filepath.Join(base, "skills"),
		ToolsDir:         filepath.Join(base, "tools"),
		SystemPromptFile: filepath.Join(base, "system_prompt.md"),
		PollInterval:     30 * time.Second,
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
