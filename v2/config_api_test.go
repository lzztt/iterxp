package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestTokens(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, "token")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"wandb", "github", "typesafe"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("test-token\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadConfigMaxTokensAndInferenceTimeoutDefaultsAndOverride(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("ITERXP_CONFIG_DIR", filepath.Join(base, ".iterxp_v2"))
	t.Setenv("ITERXP_MAX_TOKENS", "")
	t.Setenv("ITERXP_INFERENCE_TIMEOUT", "")

	cfg := loadConfig()
	if cfg.MaxTokens != 32768 {
		t.Fatalf("default MaxTokens = %d, want 32768", cfg.MaxTokens)
	}
	if cfg.InferenceTimeout != 300*time.Second {
		t.Fatalf("default InferenceTimeout = %s, want 300s", cfg.InferenceTimeout)
	}

	t.Setenv("ITERXP_MAX_TOKENS", "12345")
	t.Setenv("ITERXP_INFERENCE_TIMEOUT", "17s")
	cfg = loadConfig()
	if cfg.MaxTokens != 12345 {
		t.Fatalf("overridden MaxTokens = %d, want 12345", cfg.MaxTokens)
	}
	if cfg.InferenceTimeout != 17*time.Second {
		t.Fatalf("overridden InferenceTimeout = %s, want 17s", cfg.InferenceTimeout)
	}
}

func TestNewClientInferenceTimeoutUsesConfig(t *testing.T) {
	home := t.TempDir()
	writeTestTokens(t, home)

	client, err := NewClient(Config{HomeDir: home})
	if err != nil {
		t.Fatal(err)
	}
	if client.http.Timeout != 300*time.Second {
		t.Fatalf("zero-value InferenceTimeout produced HTTP timeout %s, want 300s", client.http.Timeout)
	}

	override := 17 * time.Second
	client, err = NewClient(Config{HomeDir: home, InferenceTimeout: override})
	if err != nil {
		t.Fatal(err)
	}
	if client.http.Timeout != override {
		t.Fatalf("configured InferenceTimeout produced HTTP timeout %s, want %s", client.http.Timeout, override)
	}
}

func TestChatRequestCarriesConfiguredMaxTokens(t *testing.T) {
	var got ChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]any{"content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":     1,
				"completion_tokens": 2,
				"total_tokens":      3,
			},
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer srv.Close()

	home := t.TempDir()
	writeTestTokens(t, home)

	cfg := loadConfig()
	cfg.HomeDir = home
	cfg.APIBase = srv.URL
	cfg.ProjectHeader = "OpenAI-Project: test/proj"

	client, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.ChatDetailed([]Message{{Role: "user", Content: "hello"}}); err != nil {
		t.Fatal(err)
	}
	if got.MaxTokens != 32768 {
		t.Fatalf("ChatRequest MaxTokens = %d, want default 32768", got.MaxTokens)
	}

	cfg.MaxTokens = 45678
	client, err = NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.ChatDetailed([]Message{{Role: "user", Content: "hello"}}); err != nil {
		t.Fatal(err)
	}
	if got.MaxTokens != 45678 {
		t.Fatalf("ChatRequest MaxTokens = %d, want override 45678", got.MaxTokens)
	}
}

func TestLoadConfigGitIdentityDefaultsAndOverride(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("ITERXP_CONFIG_DIR", filepath.Join(base, ".iterxp_v2"))
	t.Setenv("ITERXP_GIT_NAME", "")
	t.Setenv("ITERXP_GIT_EMAIL", "")

	cfg := loadConfig()
	if cfg.GitName != "IterXP Agent" || cfg.GitEmail != "agent@iterxp.com" {
		t.Fatalf("default identity = %q <%s>, want IterXP Agent <agent@iterxp.com>", cfg.GitName, cfg.GitEmail)
	}

	t.Setenv("ITERXP_GIT_NAME", "Custom Agent")
	t.Setenv("ITERXP_GIT_EMAIL", "custom@example.com")
	cfg = loadConfig()
	if cfg.GitName != "Custom Agent" || cfg.GitEmail != "custom@example.com" {
		t.Fatalf("overridden identity = %q <%s>, want Custom Agent <custom@example.com>", cfg.GitName, cfg.GitEmail)
	}
}
