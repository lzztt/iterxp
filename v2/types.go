package main

import (
	"encoding/json"
	"time"
)

type Issue struct {
	ID          int64           `json:"id"`
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	HTMLURL     string          `json:"html_url"`
	Labels      []IssueLabel    `json:"labels"`
	PullRequest json.RawMessage `json:"pull_request"`
}

type IssueLabel struct {
	Name string `json:"name"`
}

type Comment struct {
	ID      int64  `json:"id"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
}

type SessionState struct {
	IssueID            int64        `json:"issue_id"`
	IssueNumber        int          `json:"issue_number"`
	Title              string       `json:"title"`
	Priority           string       `json:"priority,omitempty"`
	WorktreePath       string       `json:"worktree_path,omitempty"`
	WorktreeBranch     string       `json:"worktree_branch,omitempty"`
	WorktreeBaseCommit string       `json:"worktree_base_commit,omitempty"`
	ContextHash        string       `json:"context_hash"`
	PendingContextHash string       `json:"pending_context_hash,omitempty"`
	Done               bool         `json:"done"`
	SeenCommentIDs     []int64      `json:"seen_comment_ids,omitempty"`
	Worker             *WorkerState `json:"worker,omitempty"`
	UpdatedAt          time.Time    `json:"updated_at"`

	LLMCalls            int64     `json:"llm_calls,omitempty"`
	LLMPromptTokens     int64     `json:"llm_prompt_tokens,omitempty"`
	LLMCompletionTokens int64     `json:"llm_completion_tokens,omitempty"`
	LLMTotalTokens      int64     `json:"llm_total_tokens,omitempty"`
	LLMDurationMS       int64     `json:"llm_duration_ms,omitempty"`
	LLMLastError        string    `json:"llm_last_error,omitempty"`
	LLMLastCallAt       time.Time `json:"llm_last_call_at,omitempty"`
	ContextCompactions  int       `json:"context_compactions,omitempty"`
	LastCompactionAt    time.Time `json:"last_compaction_at,omitempty"`
}

type Message struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	Refusal          string     `json:"refusal,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	Name             string     `json:"name,omitempty"`
}

type ChatRequest struct {
	Model           string           `json:"model"`
	MaxTokens       int              `json:"max_tokens"`
	ReasoningEffort string           `json:"reasoning_effort"`
	Messages        []Message        `json:"messages"`
	Tools           []ToolDefinition `json:"tools,omitempty"`
}

type ChatResponse struct {
	Choices []struct {
		Message struct {
			Role             string     `json:"role"`
			Content          string     `json:"content"`
			Refusal          string     `json:"refusal"`
			ReasoningContent string     `json:"reasoning_content,omitempty"`
			ToolCalls        []ToolCall `json:"tool_calls"`
			ToolCallID       string     `json:"tool_call_id,omitempty"`
			Name             string     `json:"name,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type LLMCallRecord struct {
	Timestamp        time.Time `json:"timestamp"`
	Model            string    `json:"model"`
	DurationMS       int64     `json:"duration_ms"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	FinishReason     string    `json:"finish_reason,omitempty"`
	Error            string    `json:"error,omitempty"`
}

func estimateTokens(s string) int {
	if len(s) == 0 {
		return 0
	}
	return (len(s) + 3) / 4
}

type Tool struct {
	Name        string
	Path        string
	Description string
}

type Skill struct {
	Name        string
	Path        string
	Description string
}

type WorkerState struct {
	PID           int       `json:"pid,omitempty"`
	ProcessStart  string    `json:"process_start,omitempty"`
	Version       string    `json:"version,omitempty"`
	Status        string    `json:"status,omitempty"`
	Operation     string    `json:"operation,omitempty"`
	Deadline      time.Time `json:"deadline,omitempty"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	LastHeartbeat time.Time `json:"last_heartbeat,omitempty"`
}
