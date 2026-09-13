package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeChatHandler struct {
	calls int32
	reqs  func(int) (int, string)
}

func (h *fakeChatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := int(atomic.AddInt32(&h.calls, 1))
	status, body := h.reqs(n)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func chatBody(content string) string {
	if content == "Done" {
		return `{"choices":[{"message":{"content":"Done","refusal":"","tool_calls":null},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`
	}
	return `{"choices":[{"message":{"content":"` + strings.ReplaceAll(content, `"`, `\"`) + `","refusal":"","tool_calls":null},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`
}

func newTestAgentForACK(t *testing.T, handler http.Handler) (*Agent, *Session, *Config, *int32) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	dir := t.TempDir()
	cfg := &Config{
		Repo:             "acme/widgets",
		Model:            "model-x",
		Reasoning:        "low",
		MaxTokens:        16,
		APIBase:          server.URL,
		ProjectHeader:    "OpenAI-Project: test",
		HomeDir:          dir,
		RepoDir:          filepath.Join(dir, "repo"),
		ConfigDir:        filepath.Join(dir, "config"),
		SessionDir:       filepath.Join(dir, "sessions"),
		SkillsDir:        filepath.Join(dir, "skills"),
		ToolsDir:         filepath.Join(dir, "tools"),
		SystemPromptFile: filepath.Join(dir, "system_prompt.md"),
		PollInterval:     time.Hour,
		RetryDelay:       0,
		ToolTimeout:      2 * time.Second,
		KillTimeout:      time.Second,
	}
	for _, d := range []string{cfg.RepoDir, cfg.ConfigDir, cfg.SessionDir, cfg.SkillsDir, cfg.ToolsDir} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	s, err := NewSession(Issue{Number: 1}, cfg.SessionDir)
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{cfg: *cfg, tokens: map[string]string{"wandb": "x", "github": "x", "typesafe": "x"}, http: server.Client()}
	a := &Agent{cfg: cfg, client: client}
	return a, s, cfg, nil
}

func TestFailedRequestDoesNotConsumeContext(t *testing.T) {
	handler := &fakeChatHandler{reqs: func(n int) (int, string) {
		if n == 1 {
			return http.StatusInternalServerError, `{"error":"boom"}`
		}
		return http.StatusOK, chatBody("Done")
	}}
	a, s, cfg, _ := newTestAgentForACK(t, handler)

	contextText := "Issue #1: build a thing\n"
	if err := os.WriteFile(s.ContextPath, []byte(contextText), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveState(SessionState{IssueID: 1, IssueNumber: 1}); err != nil {
		t.Fatal(err)
	}
	wantHash := hashString(contextText)

	a.runSession(s)
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.ContextHash != "" {
		t.Fatalf("failed request consumed context: ContextHash=%q", st.ContextHash)
	}
	if st.PendingContextHash != wantHash {
		t.Fatalf("PendingContextHash = %q, want %q", st.PendingContextHash, wantHash)
	}

	a.runSession(s)
	st, err = s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.ContextHash != wantHash {
		t.Fatalf("resumed request did not commit pre-Done input hash: got %q want %q", st.ContextHash, wantHash)
	}
	if !st.Done {
		t.Fatalf("Done = false after resume completed")
	}
	if cfg == nil {
		t.Fatal("cfg is nil")
	}
}

func TestDoneIdlesAndNewCommentResumes(t *testing.T) {
	handler := &fakeChatHandler{reqs: func(n int) (int, string) {
		return http.StatusOK, chatBody("Done")
	}}
	a, s, _, _ := newTestAgentForACK(t, handler)

	contextText := "Issue #1: build a thing\n"
	if err := os.WriteFile(s.ContextPath, []byte(contextText), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveState(SessionState{IssueID: 1, IssueNumber: 1, ContextHash: hashString(contextText), Done: true}); err != nil {
		t.Fatal(err)
	}

	a.runSession(s)
	if got := atomic.LoadInt32(&handler.calls); got != 0 {
		t.Fatalf("Done session called model %d times, want 0", got)
	}

	if err := s.AppendContext("Comment on issue #1 by operator:\nhttps://example.invalid\nresume please\n"); err != nil {
		t.Fatal(err)
	}
	a.runSession(s)
	if got := atomic.LoadInt32(&handler.calls); got != 1 {
		t.Fatalf("new comment model calls = %d, want 1", got)
	}
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Done {
		t.Fatalf("Done = false after resumed response")
	}
}

func TestToolResultTriggersAnotherModelCall(t *testing.T) {
	handler := &fakeChatHandler{reqs: func(n int) (int, string) {
		if n == 1 {
			return http.StatusOK, chatBody("echo hello")
		}
		return http.StatusOK, chatBody("Done")
	}}
	a, s, _, _ := newTestAgentForACK(t, handler)

	contextText := "Issue #1: run a command\n"
	if err := os.WriteFile(s.ContextPath, []byte(contextText), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveState(SessionState{IssueID: 1, IssueNumber: 1}); err != nil {
		t.Fatal(err)
	}

	a.runSession(s)
	if got := atomic.LoadInt32(&handler.calls); got != 1 {
		t.Fatalf("first runSession model calls = %d, want 1", got)
	}
	data, err := os.ReadFile(s.ContextPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hello") || !strings.Contains(string(data), "exit_code: 0") {
		t.Fatalf("tool result was not appended to context:\n%s", data)
	}

	a.runSession(s)
	if got := atomic.LoadInt32(&handler.calls); got != 2 {
		t.Fatalf("tool result did not trigger second model call: calls=%d", got)
	}
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Done {
		t.Fatalf("Done = false after second response")
	}
}
