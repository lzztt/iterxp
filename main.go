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
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

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

type Issue struct {
	ID          int64           `json:"id"`
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	HTMLURL     string          `json:"html_url"`
	Body        string          `json:"body"`
	PullRequest json.RawMessage `json:"pull_request"`
}

var (
	homeDir       string
	contextPath   string
	toolPath      string
	tokens        map[string]string
	redactStrings []string
	repo          = "lzztt/iterxp"
	model         = getenv("SEED_MODEL", "deepseek-ai/DeepSeek-V4-Pro-0813")
	apiBase       = "https://api.inference.wandb.ai/v1/chat/completions"
	projectHdr    = "OpenAI-Project: longti/inference"
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

func appendText(text string) {
	f, err := os.OpenFile(contextPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if _, err := io.WriteString(f, redact(text)+"\n"); err != nil {
		log.Fatal(err)
	}
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
	resp, err := http.DefaultClient.Do(req)
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

func isExecutable(content string) bool {
	s := strings.TrimSpace(content)
	return len(s) > 0 && !strings.Contains(s, "```") && s != "Done"
}

func main() {
	syscall.Umask(0o077)
	homeDir, _ = os.UserHomeDir()
	contextPath = filepath.Join(homeDir, "context.log")
	toolPath = filepath.Join(homeDir, "tool.sh")
	tokens = make(map[string]string)
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
	if _, err := os.OpenFile(contextPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err != nil {
		log.Fatal(err)
	}
	seen := map[int64]bool{}
	lastContext := ""
	nextPoll := time.Now().Add(5 * time.Second)
	emptyRetry := false
	log.Printf("Watching %s; polling %s.", contextPath, repo)
	for {
		if time.Now().After(nextPoll) {
			page := 1
			for {
				url := fmt.Sprintf("https://api.github.com/repos/%s/issues?state=open&per_page=100&page=%d", repo, page)
				data, err := apiCall("GET", url, tokens["github"], nil, nil)
				if err != nil {
					log.Printf("GitHub list error: %v", err)
					break
				}
				var issues []Issue
				if err := json.Unmarshal(data, &issues); err != nil {
					log.Printf("unmarshal issues: %v", err)
					break
				}
				for _, issue := range issues {
					if len(issue.PullRequest) > 0 || seen[issue.ID] {
						continue
					}
					appendText(fmt.Sprintf("Issue #%d: %s\n%s\n%s", issue.Number, issue.Title, issue.HTMLURL, issue.Body))
					seen[issue.ID] = true
				}
				if len(issues) < 100 {
					break
				}
				page++
			}
			nextPoll = time.Now().Add(30 * time.Second)
		}
		textData, err := os.ReadFile(contextPath)
		if err != nil {
			log.Fatal(err)
		}
		text := string(textData)
		if text == lastContext {
			time.Sleep(1 * time.Second)
			continue
		}
		payload := ChatRequest{
			Model:           model,
			MaxTokens:       16384,
			ReasoningEffort: "low",
			Messages: []Message{
				{Role: "system", Content: systemPrompt},
				{Role: "user", Content: redact(text)},
			},
		}
		data, err := apiCall("POST", apiBase, tokens["wandb"], payload, map[string]string{"OpenAI-Project": "longti/inference"})
		if err != nil {
			log.Printf("wandb API error: %v", err)
			time.Sleep(1 * time.Second)
			continue
		}
		var chat ChatResponse
		if err := json.Unmarshal(data, &chat); err != nil {
			log.Printf("unmarshal chat: %v", err)
			time.Sleep(1 * time.Second)
			continue
		}
		if len(chat.Choices) == 0 {
			log.Println("no choices")
			time.Sleep(1 * time.Second)
			continue
		}
		choice := chat.Choices[0]
		content := choice.Message.Content
		reason := choice.FinishReason
		if reason == "length" || !isExecutable(content) || choice.Message.Refusal != "" || len(choice.Message.ToolCalls) > 0 {
			log.Printf("No complete Bash response: %s completion_tokens: %d", reason, chat.Usage.CompletionTokens)
			if emptyRetry || reason != "stop" || choice.Message.Refusal != "" || len(choice.Message.ToolCalls) > 0 || !isExecutable(content) {
				log.Fatal("No executable Bash response; stopping without running anything.")
			}
			emptyRetry = true
			appendText("The previous model response contained no complete Bash script. Return just one small next step.")
			continue
		}
		emptyRetry = false
		command := strings.TrimSpace(content)
		lastContext = text
		if command == "Done" {
			log.Println("Done; waiting.")
			continue
		}
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
		appendText(fmt.Sprintf("$ %s\nstdout:\n%s\nstderr:\n%s\nexit_code: %d", command, stdout.String(), stderr.String(), exitCode))
		log.Printf("Bash exit_code=%d", exitCode)
	}
}
