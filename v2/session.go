package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Session struct {
	IssueID     int64
	IssueNumber int
	Dir         string
	WorktreeDir string
	ContextPath string
	StatePath   string
	PlanPath    string
	LLMPath     string
}

func NewSession(issue Issue, baseDir string) (*Session, error) {
	if issue.Number <= 0 {
		return nil, fmt.Errorf("invalid issue number: %d", issue.Number)
	}
	dir := filepath.Join(baseDir, fmt.Sprintf("issue-%d", issue.Number))
	s := &Session{
		IssueID:     issue.ID,
		IssueNumber: issue.Number,
		Dir:         dir,
		WorktreeDir: filepath.Join(dir, "worktree"),
		ContextPath: filepath.Join(dir, "context.log"),
		StatePath:   filepath.Join(dir, "state.json"),
		PlanPath:    filepath.Join(dir, "plan.md"),
		LLMPath:     filepath.Join(dir, "llm_calls.jsonl"),
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	for _, p := range []string{s.ContextPath, s.StatePath, s.PlanPath, s.LLMPath} {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	}

	if issue.ID > 0 {
		st, err := s.LoadState()
		if err != nil {
			return nil, err
		}
		if st.IssueID == 0 {
			st.IssueID = issue.ID
			st.IssueNumber = issue.Number
			st.Title = issue.Title
			st.ContextHash = hashString("")
			if err := s.SaveState(st); err != nil {
				return nil, err
			}
		}
	}
	return s, nil
}

func (s *Session) AppendContext(text string) error {
	f, err := os.OpenFile(s.ContextPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, text); err != nil {
		return err
	}
	return nil
}

func (s *Session) AppendLLMCall(record LLMCallRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.LLMPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func ApplyLLMCallState(st *SessionState, record LLMCallRecord) {
	st.LLMCalls++
	st.LLMPromptTokens += int64(record.PromptTokens)
	st.LLMCompletionTokens += int64(record.CompletionTokens)
	st.LLMTotalTokens += int64(record.TotalTokens)
	st.LLMDurationMS += record.DurationMS
	st.LLMLastCallAt = record.Timestamp
	if record.Error != "" {
		st.LLMLastError = record.Error
	} else {
		st.LLMLastError = ""
	}
}

func (s *Session) ReadContext() (string, error) {
	data, err := os.ReadFile(s.ContextPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *Session) LoadState() (SessionState, error) {
	var st SessionState
	data, err := os.ReadFile(s.StatePath)
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return st, err
	}
	if len(data) == 0 {
		return st, nil
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, err
	}
	return st, nil
}

func (s *Session) SaveState(st SessionState) error {
	st.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.StatePath, data, 0600)
}

func (s *Session) ReadPlan() (string, error) {
	data, err := os.ReadFile(s.PlanPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *Session) WritePlan(plan string) error {
	return os.WriteFile(s.PlanPath, []byte(plan), 0600)
}
