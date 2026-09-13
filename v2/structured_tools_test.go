package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func structuredToolCallResponse(callID, name, arguments, reasoning string) string {
	payload := map[string]any{
		"choices": []map[string]any{{
			"message": map[string]any{
				"role":              "assistant",
				"content":           "",
				"refusal":           "",
				"reasoning_content": reasoning,
				"tool_calls": []map[string]any{{
					"id":   callID,
					"type": "function",
					"function": map[string]any{
						"name":      name,
						"arguments": arguments,
					},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4},
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

func newStructuredAgent(t *testing.T, respond func(int) (int, string), inspect func(int, ChatRequest)) (*Agent, *Session, *int32) {
	t.Helper()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&calls, 1))
		if inspect != nil {
			var req ChatRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
				inspect(n, req)
			}
		}
		status, body := respond(n)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	cfg := &Config{
		Repo:             "acme/widgets",
		Model:            "model-x",
		Reasoning:        "low",
		MaxTokens:        128,
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
	return a, s, &calls
}

func TestBuiltinToolDefinitionsIncludeStructuredSchemas(t *testing.T) {
	defs := builtinToolDefinitions()
	if len(defs) != 3 {
		t.Fatalf("tool definitions = %d, want 3", len(defs))
	}
	byName := map[string]ToolDefinition{}
	for _, def := range defs {
		byName[def.Function.Name] = def
	}
	for _, name := range []string{"bash", "apply_patch", "finish_issue"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("missing tool definition %q", name)
		}
	}
	bash := byName["bash"]
	required := bash.Function.Parameters["required"].([]string)
	if len(required) != 1 || required[0] != "command" {
		t.Fatalf("bash required = %v, want [command]", required)
	}
	patch := byName["apply_patch"]
	required = patch.Function.Parameters["required"].([]string)
	if len(required) != 1 || required[0] != "patch" {
		t.Fatalf("apply_patch required = %v, want [patch]", required)
	}
	finishIssue := byName["finish_issue"]
	required = finishIssue.Function.Parameters["required"].([]string)
	if len(required) != 2 || required[0] != "handoff_note" || required[1] != "issue_type_label" {
		t.Fatalf("finish_issue required = %v, want [handoff_note issue_type_label]", required)
	}
}

func TestStructuredToolRoundTripAndResumeHistory(t *testing.T) {
	var mu sync.Mutex
	var requests []ChatRequest
	responseCount := 0
	a, s, calls := newStructuredAgent(t, func(n int) (int, string) {
		responseCount = n
		if n == 1 {
			return http.StatusOK, structuredToolCallResponse("call_1", "bash", `{"command":"printf 'structured-ok'"}`, "reasoning must never execute")
		}
		return http.StatusOK, chatBody("Done")
	}, func(n int, req ChatRequest) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, req)
	})

	contextText := "Issue #1: run a structured command and resume\n"
	if err := os.WriteFile(s.ContextPath, []byte(contextText), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveState(SessionState{IssueID: 1, IssueNumber: 1}); err != nil {
		t.Fatal(err)
	}

	a.runSession(s)
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("first runSession model calls = %d, want 1", got)
	}
	if responseCount != 1 {
		t.Fatalf("first response count = %d, want 1", responseCount)
	}

	mu.Lock()
	if len(requests) != 1 {
		t.Fatalf("captured requests = %d, want 1", len(requests))
	}
	first := requests[0]
	mu.Unlock()
	if len(first.Tools) != 3 {
		t.Fatalf("first request tools = %d, want 3", len(first.Tools))
	}
	toolNames := map[string]bool{}
	for _, tool := range first.Tools {
		toolNames[tool.Function.Name] = true
	}
	if !toolNames["bash"] || !toolNames["apply_patch"] || !toolNames["finish_issue"] {
		t.Fatalf("first request tool names = %v", toolNames)
	}

	contextData, err := os.ReadFile(s.ContextPath)
	if err != nil {
		t.Fatal(err)
	}
	contextText = string(contextData)
	if !strings.Contains(contextText, "structured-ok") {
		t.Fatalf("bash tool output not appended to context: %s", contextText)
	}
	if !strings.Contains(contextText, `"exit_code":0`) {
		t.Fatalf("structured exit code not present in context: %s", contextText)
	}
	if strings.Contains(contextText, "reasoning must never execute") {
		t.Fatalf("reasoning text was appended to executable context: %s", contextText)
	}

	historyData, err := os.ReadFile(s.HistoryPath)
	if err != nil {
		t.Fatal(err)
	}
	var history []Message
	if err := json.Unmarshal(historyData, &history); err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("history messages = %d, want 2", len(history))
	}
	if history[0].Role != "assistant" || len(history[0].ToolCalls) != 1 {
		t.Fatalf("history assistant tool-call message invalid: %+v", history[0])
	}
	if history[0].ToolCalls[0].ID != "call_1" || history[0].ToolCalls[0].Function.Name != "bash" {
		t.Fatalf("history tool call identity invalid: %+v", history[0].ToolCalls[0])
	}
	if history[0].ReasoningContent != "reasoning must never execute" {
		t.Fatalf("reasoning metadata not preserved: %q", history[0].ReasoningContent)
	}
	if history[1].Role != "tool" || history[1].ToolCallID != "call_1" {
		t.Fatalf("history tool result message invalid: %+v", history[1])
	}
	if !strings.Contains(history[1].Content, "structured-ok") {
		t.Fatalf("history tool result missing output: %s", history[1].Content)
	}

	a.runSession(s)
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("resume model calls = %d, want 2", got)
	}
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Done {
		t.Fatalf("Done = false after resumed response")
	}

	mu.Lock()
	if len(requests) != 2 {
		t.Fatalf("captured requests after resume = %d, want 2", len(requests))
	}
	second := requests[1]
	mu.Unlock()
	var sawAssistant, sawTool bool
	for _, msg := range second.Messages {
		if msg.Role == "assistant" && len(msg.ToolCalls) == 1 && msg.ToolCalls[0].ID == "call_1" {
			sawAssistant = true
		}
		if msg.Role == "tool" && msg.ToolCallID == "call_1" {
			sawTool = true
		}
	}
	if !sawAssistant || !sawTool {
		t.Fatalf("resumed request did not preserve tool-call history: sawAssistant=%v sawTool=%v", sawAssistant, sawTool)
	}
}

func TestProseMarkdownIsNeverExecuted(t *testing.T) {
	marker := ""
	a, s, calls := newStructuredAgent(t, func(n int) (int, string) {
		return http.StatusOK, chatBody("```bash\nprintf 'should-not-run' > \"$ITERXP_SESSION_DIR/prose-executed\"\n```")
	}, nil)
	marker = filepath.Join(s.Dir, "prose-executed")

	contextText := "Issue #1: prose response\n"
	if err := os.WriteFile(s.ContextPath, []byte(contextText), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveState(SessionState{IssueID: 1, IssueNumber: 1}); err != nil {
		t.Fatal(err)
	}

	a.runSession(s)
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("prose model calls = %d, want 1", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("prose response executed a command; marker exists: %v", err)
	}
	contextData, err := os.ReadFile(s.ContextPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contextData), "Assistant text was received without a tool call") {
		t.Fatalf("context missing ignored-prose feedback: %s", contextData)
	}
	if strings.Contains(string(contextData), "should-not-run") {
		t.Fatalf("context contains prose shell source result: %s", contextData)
	}
}

func TestApplyPatchCreateEditDeleteInWorktree(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	sessionDir := t.TempDir()
	s, err := NewSession(Issue{ID: 1, Number: 301}, sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureWorktree(Config{RepoDir: repo}, s); err != nil {
		t.Fatal(err)
	}
	a := &Agent{cfg: &Config{RepoDir: repo}}

	createPatch := "--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+created\n"
	stdout, stderr, code := a.applyPatch(s, createPatch)
	if code != 0 {
		t.Fatalf("create patch failed: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	created, err := os.ReadFile(filepath.Join(s.WorktreeDir, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(created) != "created\n" {
		t.Fatalf("created content = %q, want %q", created, "created\n")
	}

	editPatch := "--- a/new.txt\n+++ b/new.txt\n@@ -1 +1 @@\n-created\n+modified\n"
	stdout, stderr, code = a.applyPatch(s, editPatch)
	if code != 0 {
		t.Fatalf("edit patch failed: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	edited, err := os.ReadFile(filepath.Join(s.WorktreeDir, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(edited) != "modified\n" {
		t.Fatalf("edited content = %q, want %q", edited, "modified\n")
	}

	if _, err := runGit(s.WorktreeDir, "add", "new.txt"); err != nil {
		t.Fatal(err)
	}
	deletePatch := "--- a/new.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-modified\n"
	stdout, stderr, code = a.applyPatch(s, deletePatch)
	if code != 0 {
		t.Fatalf("delete patch failed: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(s.WorktreeDir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted file still exists or stat error: %v", err)
	}
}

func TestApplyPatchNonMatchingFailsWithoutPartialEdit(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	sessionDir := t.TempDir()
	s, err := NewSession(Issue{ID: 1, Number: 302}, sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureWorktree(Config{RepoDir: repo}, s); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(s.WorktreeDir, "file.txt")
	original := "alpha\nbeta\n"
	if err := os.WriteFile(target, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(s.WorktreeDir, "add", "file.txt"); err != nil {
		t.Fatal(err)
	}

	badPatch := "--- a/file.txt\n+++ b/file.txt\n@@ -1,2 +1,2 @@\n alpha\n-CHANGED\n+beta\n"
	a := &Agent{cfg: &Config{RepoDir: repo}}
	stdout, stderr, code := a.applyPatch(s, badPatch)
	if code == 0 {
		t.Fatalf("nonmatching patch succeeded: stdout=%q stderr=%q", stdout, stderr)
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != original {
		t.Fatalf("nonmatching patch partially edited file: %q", after)
	}
}

func TestApplyPatchRejectsPathOutsideWorktree(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	sessionDir := t.TempDir()
	s, err := NewSession(Issue{ID: 1, Number: 303}, sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureWorktree(Config{RepoDir: repo}, s); err != nil {
		t.Fatal(err)
	}
	escapePath := filepath.Join(filepath.Dir(s.WorktreeDir), "escape.txt")
	patch := "--- /dev/null\n+++ b/../escape.txt\n@@ -0,0 +1 @@\n+escape\n"
	a := &Agent{cfg: &Config{RepoDir: repo}}
	stdout, stderr, code := a.applyPatch(s, patch)
	if code == 0 {
		t.Fatalf("outside-worktree patch succeeded: stdout=%q stderr=%q", stdout, stderr)
	}
	if !strings.Contains(stderr, "outside worktree") {
		t.Fatalf("stderr missing outside-worktree message: %q", stderr)
	}
	if _, err := os.Stat(escapePath); !os.IsNotExist(err) {
		t.Fatalf("outside worktree file was created: %v", err)
	}
}
