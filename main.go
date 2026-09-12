package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type ChatMessage struct {
	Role      string          `json:"role"`
	Content   string          `json:"content,omitempty"`
	Refusal   string          `json:"refusal,omitempty"`
	ToolCalls json.RawMessage `json:"tool_calls,omitempty"`
}

type ChatRequest struct {
	Model           string        `json:"model"`
	MaxTokens       int           `json:"max_tokens"`
	ReasoningEffort string        `json:"reasoning_effort"`
	Messages        []ChatMessage `json:"messages"`
}

type ChatResponse struct {
	Choices []struct {
		Message      ChatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type Issue struct {
	ID          int64           `json:"id"`
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	HTMLURL     string          `json:"html_url"`
	PullRequest json.RawMessage `json:"pull_request"`
}

type IssueComment struct {
	ID       int64  `json:"id"`
	Body     string `json:"body"`
	HTMLURL  string `json:"html_url"`
	IssueURL string `json:"issue_url"`
	User     struct {
		Login string `json:"login"`
	} `json:"user"`
}

type AgentState struct {
	SeenIssues   map[int64]bool `json:"seen_issues"`
	SeenComments map[int64]bool `json:"seen_comments"`
}

var (
	homeDir       string
	toolPath      string
	statePath     string
	tokens        map[string]string
	redactStrings []string
	repo          = "lzztt/iterxp"
	model         = getenv("SEED_MODEL", "deepseek-ai/DeepSeek-V4-Pro-0813")
	apiBase       = "https://api.inference.wandb.ai/v1/chat/completions"
	httpClient    = &http.Client{Timeout: 60 * time.Second}
)

const systemPrompt = `You are IterXP, a self-building agent controlled by /home/admin/seed.py.
Return only the next small Bash step, not a complete solution to the whole task.
Your response is saved as tool.sh and executed; stdout, stderr, and exit code
will appear in the next context so you can choose the following step.
Inspect files through Bash when needed, including seed.py itself.
Output Bash only, without Markdown fences. Output exactly Done when finished.
Credentials are in ~/token; never reveal their values.`

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func redact(s string) string {
	for _, t := range redactStrings {
		s = strings.ReplaceAll(s, t, "[REDACTED]")
	}
	return s
}

func apiCall(method, url, key string, payload interface{}, headers map[string]string) ([]byte, error) {
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
	resp, err := httpClient.Do(req)
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

func fetchJSON(url, key string, out interface{}) error {
	data, err := apiCall("GET", url, key, nil, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func loadState(path string) *AgentState {
	st := &AgentState{
		SeenIssues:   map[int64]bool{},
		SeenComments: map[int64]bool{},
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	if err := json.Unmarshal(data, st); err == nil {
		if st.SeenIssues == nil {
			st.SeenIssues = map[int64]bool{}
		}
		if st.SeenComments == nil {
			st.SeenComments = map[int64]bool{}
		}
	}
	return st
}

func saveState(path string, st *AgentState) {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		log.Printf("marshal state: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("write state: %v", err)
	}
}

func pollGitHub(messages *[]ChatMessage, st *AgentState) bool {
	added := false

	for page := 1; page <= 5; page++ {
		url := fmt.Sprintf("https://api.github.com/repos/%s/issues?state=all&per_page=50&sort=updated&direction=desc&page=%d", repo, page)
		var issues []Issue
		if err := fetchJSON(url, tokens["github"], &issues); err != nil {
			log.Printf("GitHub issues error: %v", err)
			break
		}
		for _, iss := range issues {
			if len(iss.PullRequest) > 0 || st.SeenIssues[iss.ID] {
				continue
			}
			st.SeenIssues[iss.ID] = true
			content := fmt.Sprintf("Issue #%d: %s\n%s\n%s", iss.Number, iss.Title, iss.HTMLURL, iss.Body)
			*messages = append(*messages, ChatMessage{Role: "user", Content: redact(content)})
			added = true
		}
		if len(issues) < 50 {
			break
		}
	}

	for page := 1; page <= 5; page++ {
		url := fmt.Sprintf("https://api.github.com/repos/%s/issues/comments?per_page=50&sort=created&direction=desc&page=%d", repo, page)
		var comments []IssueComment
		if err := fetchJSON(url, tokens["github"], &comments); err != nil {
			log.Printf("GitHub comments error: %v", err)
			break
		}
		for _, c := range comments {
			if st.SeenComments[c.ID] {
				continue
			}
			st.SeenComments[c.ID] = true
			issueNo := path.Base(c.IssueURL)
			content := fmt.Sprintf("Comment on issue #%s by %s:\n%s\n%s", issueNo, c.User.Login, c.HTMLURL, c.Body)
			*messages = append(*messages, ChatMessage{Role: "user", Content: redact(content)})
			added = true
		}
		if len(comments) < 50 {
			break
		}
	}

	return added
}

func isExecutable(content string) bool {
	s := strings.TrimSpace(content)
	return len(s) > 0 && !strings.Contains(s, "```") && s != "Done"
}

func main() {
	syscall.Umask(0o077)
	homeDir, _ = os.UserHomeDir()
	toolPath = filepath.Join(homeDir, "tool.sh")
	statePath = filepath.Join(homeDir, ".iterxp_agent_state.json")
	tokens = map[string]string{}
	for _, name := range []string{"wandb", "github", "typesafe"} {
		data, err := os.ReadFile(filepath.Join(homeDir, "token", name))
		if err != nil {
			log.Fatalf("read token %s: %v", name, err)
		}
		val := strings.TrimSpace(string(data))
		tokens[name] = val
		if val != "" {
			redactStrings = append(redactStrings, val)
		}
	}

	st := loadState(statePath)
	messages := []ChatMessage{{Role: "system", Content: systemPrompt}}
	nextPoll := time.Time{}
	needModel := false

	log.Printf("Watching %s; polling %s.", repo, "issues and comments")

	for {
		if time.Now().After(nextPoll) {
			if pollGitHub(&messages, st) {
				needModel = true
			}
			saveState(statePath, st)
			nextPoll = time.Now().Add(30 * time.Second)
		}

		if !needModel {
			time.Sleep(1 * time.Second)
			continue
		}

		payload := ChatRequest{
			Model:           model,
			MaxTokens:       16384,
			ReasoningEffort: "low",
			Messages:        messages,
		}
		data, err := apiCall("POST", apiBase, tokens["wandb"], payload, map[string]string{"OpenAI-Project": "longti/inference"})
		if err != nil {
			log.Printf("wandb API error: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}

		var chat ChatResponse
		if err := json.Unmarshal(data, &chat); err != nil {
			log.Printf("unmarshal chat: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		if len(chat.Choices) == 0 {
			log.Println("no choices")
			time.Sleep(2 * time.Second)
			continue
		}

		choice := chat.Choices[0]
		content := choice.Message.Content
		reason := choice.FinishReason

		if content == "Done" {
			messages = append(messages, ChatMessage{Role: "assistant", Content: "Done"})
			needModel = false
			log.Println("Done; waiting.")
			continue
		}

		if reason == "length" || !isExecutable(content) || choice.Message.Refusal != "" || len(choice.Message.ToolCalls) > 0 {
			log.Printf("No complete Bash response: %s completion_tokens: %d", reason, chat.Usage.CompletionTokens)
			messages = append(messages, ChatMessage{Role: "assistant", Content: redact(content)})
			messages = append(messages, ChatMessage{Role: "user", Content: "The previous response was not a complete Bash command. Return exactly one small Bash step, without Markdown fences."})
			needModel = true
			time.Sleep(1 * time.Second)
			continue
		}

		command := strings.TrimSpace(content)
		messages = append(messages, ChatMessage{Role: "assistant", Content: command})

		if err := os.WriteFile(toolPath, []byte(command+"\n"), 0700); err != nil {
			log.Fatal(err)
		}
		cmd := exec.Command("bash", toolPath)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		exitCode := 0
		if runErr != nil {
			if exitErr, ok := runErr.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				log.Printf("run error: %v", runErr)
				exitCode = -1
			}
		}
		result := fmt.Sprintf("$ %s\nstdout:\n%s\nstderr:\n%s\nexit_code: %d", command, stdout.String(), stderr.String(), exitCode)
		messages = append(messages, ChatMessage{Role: "user", Content: redact(result)})
		log.Printf("Bash exit_code=%d", exitCode)
		time.Sleep(1 * time.Second)
	}
}
