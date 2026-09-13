package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewSessionCreatesFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSession(Issue{ID: 7, Number: 123}, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{s.ContextPath, s.StatePath, s.PlanPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected session file %s: %v", p, err)
		}
	}
	if got := filepath.Base(s.Dir); got != "issue-123" {
		t.Fatalf("session dir base = %q, want issue-123", got)
	}
}

func TestSessionContextAndStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSession(Issue{ID: 7, Number: 123}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendContext("hello"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadContext()
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello\n" {
		t.Fatalf("context = %q, want %q", got, "hello\n")
	}

	st := SessionState{IssueID: 7, IssueNumber: 123, ContextHash: "abc", Done: false}
	if err := s.SaveState(st); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ContextHash != "abc" || loaded.IssueNumber != 123 {
		t.Fatalf("loaded state mismatch: %+v", loaded)
	}
	if loaded.UpdatedAt.IsZero() {
		t.Fatal("expected UpdatedAt to be set")
	}

	if err := s.WritePlan("# plan"); err != nil {
		t.Fatal(err)
	}
	plan, err := s.ReadPlan()
	if err != nil {
		t.Fatal(err)
	}
	if plan != "# plan" {
		t.Fatalf("plan = %q", plan)
	}
}

func TestEstimateTokens(t *testing.T) {
	if got := estimateTokens(""); got != 0 {
		t.Fatalf("estimate empty = %d, want 0", got)
	}
	if got := estimateTokens("abcd"); got != 1 {
		t.Fatalf("estimate abcd = %d, want 1", got)
	}
	if got := estimateTokens("abcde"); got != 2 {
		t.Fatalf("estimate abcde = %d, want 2", got)
	}
}

func TestApplyLLMCallState(t *testing.T) {
	now := time.Now().UTC()
	st := SessionState{}
	ApplyLLMCallState(&st, LLMCallRecord{Timestamp: now, PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, DurationMS: 900})
	if st.LLMCalls != 1 || st.LLMPromptTokens != 100 || st.LLMCompletionTokens != 50 || st.LLMTotalTokens != 150 || st.LLMDurationMS != 900 {
		t.Fatalf("state after apply = %+v", st)
	}
	if !st.LLMLastCallAt.Equal(now) {
		t.Fatalf("LLMLastCallAt = %v, want %v", st.LLMLastCallAt, now)
	}
	if st.LLMLastError != "" {
		t.Fatalf("LLMLastError = %q, want empty", st.LLMLastError)
	}
}

func TestAppendLLMCall(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSession(Issue{ID: 7, Number: 123}, dir)
	if err != nil {
		t.Fatal(err)
	}
	record := LLMCallRecord{Timestamp: time.Now(), Model: "model-x", PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	if err := s.AppendLLMCall(record); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.LLMPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if len(text) == 0 {
		t.Fatal("expected llm_calls.jsonl to contain a record")
	}
}
