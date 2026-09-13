package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Client struct {
	cfg    Config
	tokens map[string]string
	redact []string
	http   *http.Client
}

func NewClient(cfg Config) (*Client, error) {
	tokens := map[string]string{}
	var redact []string
	for _, name := range []string{"wandb", "github", "typesafe"} {
		data, err := os.ReadFile(filepath.Join(cfg.HomeDir, "token", name))
		if err != nil {
			return nil, fmt.Errorf("read token %s: %w", name, err)
		}
		val := strings.TrimSpace(string(data))
		tokens[name] = val
		if val != "" {
			redact = append(redact, val)
		}
	}
	inferenceTimeout := cfg.InferenceTimeout
	if inferenceTimeout <= 0 {
		inferenceTimeout = 300 * time.Second
	}
	return &Client{
		cfg:    cfg,
		tokens: tokens,
		redact: redact,
		http:   &http.Client{Timeout: inferenceTimeout},
	}, nil
}

func (c *Client) Redact(s string) string {
	for _, val := range c.redact {
		s = strings.ReplaceAll(s, val, "[REDACTED]")
	}
	return s
}

func (c *Client) apiCall(method, url, key string, payload interface{}, headers map[string]string) ([]byte, error) {
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequest(method, url, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API status %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}

func (c *Client) fetchJSON(url string, out interface{}) error {
	data, err := c.apiCall("GET", url, c.tokens["github"], nil, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (c *Client) ChatDetailed(messages []Message) (*ChatResponse, LLMCallRecord, error) {
	start := time.Now()
	record := LLMCallRecord{
		Timestamp: start.UTC(),
		Model:     c.cfg.Model,
	}
	payload := ChatRequest{
		Model:           c.cfg.Model,
		MaxTokens:       c.cfg.MaxTokens,
		ReasoningEffort: c.cfg.Reasoning,
		Messages:        messages,
		Tools:           builtinToolDefinitions(),
	}
	data, err := c.apiCall("POST", c.cfg.APIBase, c.tokens["wandb"], payload, map[string]string{
		"OpenAI-Project": strings.TrimSpace(strings.TrimPrefix(c.cfg.ProjectHeader, "OpenAI-Project:")),
	})
	if err != nil {
		record.DurationMS = time.Since(start).Milliseconds()
		record.Error = c.Redact(err.Error())
		return nil, record, err
	}
	var resp ChatResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		record.DurationMS = time.Since(start).Milliseconds()
		record.Error = c.Redact(err.Error())
		return nil, record, err
	}
	record.DurationMS = time.Since(start).Milliseconds()
	record.PromptTokens = resp.Usage.PromptTokens
	record.CompletionTokens = resp.Usage.CompletionTokens
	record.TotalTokens = resp.Usage.TotalTokens
	if len(resp.Choices) > 0 {
		record.FinishReason = resp.Choices[0].FinishReason
	}
	return &resp, record, nil
}

func (c *Client) Chat(messages []Message) (*ChatResponse, error) {
	resp, _, err := c.ChatDetailed(messages)
	return resp, err
}
