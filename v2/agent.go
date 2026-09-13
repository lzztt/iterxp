package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Agent struct {
	cfg         *Config
	client      *Client
	tools       map[string]Tool
	skillsBlock string
	sessions    map[int]*Session
	seenIssues  map[int64]bool
	mu          sync.Mutex
	lastPoll    time.Time
}

func NewAgent(cfg *Config, client *Client) *Agent {
	skills, skillText := loadSkills(*cfg)
	agent := &Agent{
		cfg:         cfg,
		client:      client,
		tools:       map[string]Tool{},
		skillsBlock: skillText,
		sessions:    map[int]*Session{},
		seenIssues:  map[int64]bool{},
	}
	_ = skills
	agent.loadTools()
	agent.loadSessions()
	return agent
}

func (a *Agent) loadTools() {
	entries, err := os.ReadDir(a.cfg.ToolsDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Mode()&0111 == 0 {
			continue
		}
		name := entry.Name()
		a.tools[name] = Tool{
			Name:        name,
			Path:        filepath.Join(a.cfg.ToolsDir, name),
			Description: "",
		}
	}
}

func (a *Agent) loadSessions() {
	entries, err := os.ReadDir(a.cfg.SessionDir)
	if err != nil {
		log.Printf("load sessions: %v", err)
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "issue-") {
			continue
		}
		numText := strings.TrimPrefix(name, "issue-")
		num, err := strconv.Atoi(numText)
		if err != nil {
			continue
		}
		session, err := NewSession(Issue{Number: num}, a.cfg.SessionDir)
		if err != nil {
			log.Printf("resume session %s: %v", name, err)
			continue
		}
		st, err := session.LoadState()
		if err != nil {
			log.Printf("load state %s: %v", name, err)
			continue
		}
		if st.IssueID > 0 {
			session.IssueID = st.IssueID
			a.seenIssues[st.IssueID] = true
		}
		a.sessions[num] = session
	}
}

func (a *Agent) toolBlock() string {
	if len(a.tools) == 0 {
		return ""
	}
	var names []string
	for name := range a.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("\n\n# Available non-bash tools\n")
	for _, name := range names {
		tool := a.tools[name]
		if tool.Description != "" {
			fmt.Fprintf(&b, "- %s: %s\n", name, tool.Description)
		} else {
			fmt.Fprintf(&b, "- %s\n", name)
		}
	}
	b.WriteString("\nTo invoke a non-bash tool, start your response with:\n@tool <name>\n<JSON argument>\n")
	return b.String()
}

func (a *Agent) buildPrompt() string {
	system := loadSystemPrompt(*a.cfg)
	parts := []string{system}
	if a.skillsBlock != "" {
		parts = append(parts, a.skillsBlock)
	}
	if toolText := a.toolBlock(); toolText != "" {
		parts = append(parts, toolText)
	}
	return strings.Join(parts, "\n")
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (a *Agent) pollGitHub() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	page := 1
	for {
		url := fmt.Sprintf("https://api.github.com/repos/%s/issues?state=open&per_page=100&page=%d", a.cfg.Repo, page)
		var issues []Issue
		if err := a.client.fetchJSON(url, &issues); err != nil {
			log.Printf("poll issues: %v", err)
			return err
		}
		for _, issue := range issues {
			if len(issue.PullRequest) > 0 {
				continue
			}
			session, exists := a.sessions[issue.Number]
			if !exists {
				var err error
				session, err = NewSession(issue, a.cfg.SessionDir)
				if err != nil {
					log.Printf("create session %d: %v", issue.Number, err)
					continue
				}
				a.sessions[issue.Number] = session
			}
			if issue.ID > 0 {
				session.IssueID = issue.ID
				a.seenIssues[issue.ID] = true
			}

			data, _ := os.ReadFile(session.ContextPath)
			if strings.TrimSpace(string(data)) == "" {
				header := fmt.Sprintf("Issue #%d: %s\n%s\n%s", issue.Number, issue.Title, issue.HTMLURL, issue.Body)
				if err := session.AppendContext(header); err != nil {
					log.Printf("append issue header %d: %v", issue.Number, err)
					continue
				}
			}

			st, err := session.LoadState()
			if err != nil {
				log.Printf("load state for issue %d: %v", issue.Number, err)
				continue
			}
			if st.IssueID == 0 {
				st.IssueID = issue.ID
				st.IssueNumber = issue.Number
				st.Title = issue.Title
			}

			seenComment := map[int64]bool{}
			for _, id := range st.SeenCommentIDs {
				seenComment[id] = true
			}
			commentPage := 1
			commentAppended := false
			for {
				commentURL := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/comments?per_page=100&page=%d", a.cfg.Repo, issue.Number, commentPage)
				var comments []Comment
				if err := a.client.fetchJSON(commentURL, &comments); err != nil {
					log.Printf("poll comments for issue %d: %v", issue.Number, err)
					break
				}
				for _, comment := range comments {
					if seenComment[comment.ID] {
						continue
					}
					seenComment[comment.ID] = true
					text := fmt.Sprintf("Comment on issue #%d by %s:\n%s\n%s", issue.Number, comment.User.Login, comment.HTMLURL, comment.Body)
					if err := session.AppendContext(text); err != nil {
						log.Printf("append comment %d for issue %d: %v", comment.ID, issue.Number, err)
						continue
					}
					st.SeenCommentIDs = append(st.SeenCommentIDs, comment.ID)
					commentAppended = true
				}
				if len(comments) < 100 {
					break
				}
				commentPage++
			}
			if commentAppended || st.IssueID == 0 || st.IssueNumber == 0 {
				if err := session.SaveState(st); err != nil {
					log.Printf("save comments state for issue %d: %v", issue.Number, err)
				}
			}
		}
		if len(issues) < 100 {
			break
		}
		page++
	}
	return nil
}

func (a *Agent) snapshotSessions() []*Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]*Session, 0, len(a.sessions))
	for _, session := range a.sessions {
		out = append(out, session)
	}
	return out
}

func (a *Agent) Run() {
	if err := a.pollGitHub(); err != nil {
		log.Printf("initial poll failed: %v", err)
	}
	a.lastPoll = time.Now()

	for {
		if time.Since(a.lastPoll) >= a.cfg.PollInterval {
			if err := a.pollGitHub(); err != nil {
				log.Printf("poll failed: %v", err)
			}
			a.lastPoll = time.Now()
		}

		for _, session := range a.snapshotSessions() {
			a.runSession(session)
		}
		time.Sleep(1 * time.Second)
	}
}

func (a *Agent) runSession(session *Session) {
	state, err := session.LoadState()
	if err != nil {
		log.Printf("load state %d: %v", session.IssueNumber, err)
		return
	}
	contextData, err := os.ReadFile(session.ContextPath)
	if err != nil {
		log.Printf("read context %d: %v", session.IssueNumber, err)
		return
	}
	contextText := string(contextData)
	contextHash := hashString(contextText)
	if strings.TrimSpace(contextText) == "" || state.ContextHash == contextHash {
		return
	}

	state.ContextHash = contextHash
	if err := session.SaveState(state); err != nil {
		log.Printf("save state %d: %v", session.IssueNumber, err)
		return
	}

	messages := []Message{
		{Role: "system", Content: a.buildPrompt()},
		{Role: "user", Content: a.client.Redact(contextText)},
	}
	if planData, err := os.ReadFile(session.PlanPath); err == nil {
		plan := strings.TrimSpace(string(planData))
		if plan != "" {
			messages = append(messages, Message{Role: "user", Content: "Current plan from plan.md:\n" + a.client.Redact(plan)})
		}
	}

	response, err := a.client.Chat(messages)
	if err != nil {
		log.Printf("chat session %d: %v", session.IssueNumber, err)
		return
	}
	if len(response.Choices) == 0 {
		log.Printf("session %d: no choices", session.IssueNumber)
		return
	}

	choice := response.Choices[0]
	content := choice.Message.Content
	reason := choice.FinishReason
	refusal := choice.Message.Refusal
	hasToolCalls := len(choice.Message.ToolCalls) > 0

	if strings.TrimSpace(content) == "" || reason == "length" {
		log.Printf("session %d: incomplete response reason=%s completion_tokens=%d", session.IssueNumber, reason, response.Usage.CompletionTokens)
		if refusal != "" || hasToolCalls || (reason != "length" && reason != "stop") {
			_ = session.AppendContext("The previous model response was not executable. Return exactly one small step.")
			return
		}
		_ = session.AppendContext("The previous model response contained no complete action. Return exactly one small step.")
		return
	}

	command := strings.TrimSpace(content)
	if command == "Done" {
		state.Done = true
		if err := session.SaveState(state); err != nil {
			log.Printf("save state %d: %v", session.IssueNumber, err)
		}
		log.Printf("session %d: Done", session.IssueNumber)
		return
	}

	var stdout, stderr string
	var exitCode int
	if strings.HasPrefix(command, "@tool ") {
		stdout, stderr, exitCode = a.runTool(command, session)
	} else {
		stdout, stderr, exitCode = a.runBash(command, session)
	}

	result := fmt.Sprintf("$ %s\nstdout:\n%s\nstderr:\n%s\nexit_code: %d", command, stdout, stderr, exitCode)
	if err := session.AppendContext(a.client.Redact(result)); err != nil {
		log.Printf("append result %d: %v", session.IssueNumber, err)
		return
	}

	newData, err := os.ReadFile(session.ContextPath)
	if err != nil {
		log.Printf("read context after step %d: %v", session.IssueNumber, err)
		return
	}
	state.ContextHash = hashString(string(newData))
	state.Done = false
	if err := session.SaveState(state); err != nil {
		log.Printf("save state after step %d: %v", session.IssueNumber, err)
	}
	log.Printf("session %d exit_code=%d", session.IssueNumber, exitCode)
}

func (a *Agent) runBash(command string, session *Session) (string, string, int) {
	toolPath := filepath.Join(session.Dir, "tool.sh")
	if err := os.WriteFile(toolPath, []byte(command+"\n"), 0700); err != nil {
		return "", err.Error(), -1
	}
	cmd := exec.Command("bash", toolPath)
	cmd.Dir = session.Dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			code = -1
			stderr.WriteString(err.Error())
		}
	}
	return stdout.String(), stderr.String(), code
}

func (a *Agent) runTool(command string, session *Session) (string, string, int) {
	rest := strings.TrimSpace(strings.TrimPrefix(command, "@tool"))
	parts := strings.SplitN(rest, "\n", 2)
	fields := strings.Fields(strings.TrimSpace(parts[0]))
	if len(fields) == 0 {
		return "", "missing tool name", -1
	}
	name := fields[0]
	tool, ok := a.tools[name]
	if !ok {
		return "", "unknown tool: " + name, -1
	}
	var args []string
	if len(parts) == 2 {
		arg := strings.TrimSpace(parts[1])
		if arg != "" {
			args = append(args, arg)
		}
	}
	cmd := exec.Command(tool.Path, args...)
	cmd.Dir = session.Dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			code = -1
			stderr.WriteString(err.Error())
		}
	}
	return stdout.String(), stderr.String(), code
}
