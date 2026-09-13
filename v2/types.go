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
	PullRequest json.RawMessage `json:"pull_request"`
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
	IssueID        int64     `json:"issue_id"`
	IssueNumber    int       `json:"issue_number"`
	Title          string    `json:"title"`
	ContextHash    string    `json:"context_hash"`
	Done           bool      `json:"done"`
	SeenCommentIDs []int64   `json:"seen_comment_ids,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model           string    `json:"model"`
	MaxTokens       int       `json:"max_tokens"`
	ReasoningEffort string    `json:"reasoning_effort"`
	Messages        []Message `json:"messages"`
}

type ChatResponse struct {
	Choices []struct {
		Message struct {
			Content   string          `json:"content"`
			Refusal   string          `json:"refusal"`
			ToolCalls json.RawMessage `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
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
