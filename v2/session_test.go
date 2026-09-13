package main

import (
	"os"
	"path/filepath"
	"testing"
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
