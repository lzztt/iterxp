package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
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

func NewIssueAgent(cfg *Config, client *Client, session *Session) *Agent {
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
	if session != nil {
		agent.sessions[session.IssueNumber] = session
		if session.IssueID > 0 {
			agent.seenIssues[session.IssueID] = true
		}
	}
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
	return builtinToolsBlock()
}

func (a *Agent) repoDirForSession(session *Session) string {
	if session != nil && isGitWorktree(session.WorktreeDir) {
		return session.WorktreeDir
	}
	if a.cfg != nil && a.cfg.RepoDir != "" {
		return a.cfg.RepoDir
	}
	return ""
}

func (a *Agent) buildPromptWithRepoDir(repoDir string) string {
	if a == nil || a.cfg == nil {
		return ""
	}
	cfg := *a.cfg
	if repoDir != "" {
		cfg.RepoDir = repoDir
	}
	system := loadSystemPrompt(cfg)
	parts := []string{system}
	if agentsBlock := loadAgentsBlock(cfg); agentsBlock != "" {
		parts = append(parts, agentsBlock)
	}
	if cliBlock := cliInventoryBlock(nil); cliBlock != "" {
		parts = append(parts, cliBlock)
	}
	_, skillsText := loadSkills(cfg)
	if skillsText != "" {
		parts = append(parts, skillsText)
	}
	if toolText := a.toolBlock(); toolText != "" {
		parts = append(parts, toolText)
	}
	return strings.Join(parts, "\n")
}

func (a *Agent) buildPrompt() string {
	return a.buildPromptWithRepoDir("")
}

func (a *Agent) buildPromptForSession(session *Session) string {
	return a.buildPromptWithRepoDir(a.repoDirForSession(session))
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func tailByTokens(s string, maxTokens int) string {
	if maxTokens <= 0 {
		return ""
	}
	if estimateTokens(s) <= maxTokens {
		return s
	}
	maxBytes := maxTokens * 4
	if len(s) <= maxBytes {
		return s
	}
	start := len(s) - maxBytes
	if idx := strings.Index(s[start:], "\n"); idx >= 0 {
		start += idx + 1
	}
	return s[start:]
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
				a.seenIssues[issue.ID] = true
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

func (a *Agent) RunDispatcher() {
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
		time.Sleep(1 * time.Second)
	}
}

func (a *Agent) retryDelay() time.Duration {
	if a.cfg == nil || a.cfg.RetryDelay <= 0 {
		return 0
	}
	return a.cfg.RetryDelay
}

func (a *Agent) runSession(session *Session) {
	state, err := session.LoadState()
	if err != nil {
		log.Printf("load state %d: %v", session.IssueNumber, err)
		return
	}
	if err := a.ensureSessionWorktree(session); err != nil {
		log.Printf("worktree setup session %d: %v", session.IssueNumber, err)
		return
	}
	contextData, err := os.ReadFile(session.ContextPath)
	if err != nil {
		log.Printf("read context %d: %v", session.IssueNumber, err)
		return
	}
	contextText := string(contextData)
	if a.cfg != nil && a.cfg.ContextCompactThreshold > 0 && estimateTokens(contextText) > a.cfg.ContextCompactThreshold {
		compacted, compactErr := a.compactContext(session, contextText)
		if compactErr != nil {
			log.Printf("compact context session %d: %v", session.IssueNumber, compactErr)
		} else {
			contextText = compacted
			state.ContextCompactions++
			state.LastCompactionAt = time.Now()
		}
	}
	contextHash := hashString(contextText)
	if strings.TrimSpace(contextText) == "" {
		return
	}
	if state.PendingContextHash == "" && state.ContextHash == contextHash {
		return
	}
	if state.Done {
		state.Done = false
	}

	if state.PendingContextHash != contextHash {
		state.PendingContextHash = contextHash
	}
	if state.ContextHash != contextHash {
		delay := a.retryDelay()
		if delay > 0 && !state.LLMLastCallAt.IsZero() && time.Since(state.LLMLastCallAt) < delay {
			if saveErr := session.SaveState(state); saveErr != nil {
				log.Printf("save pending state %d: %v", session.IssueNumber, saveErr)
			}
			return
		}
	}
	if err := session.SaveState(state); err != nil {
		log.Printf("save pending state %d: %v", session.IssueNumber, err)
		return
	}

	history, err := session.LoadHistory()
	if err != nil {
		log.Printf("load history session %d: %v", session.IssueNumber, err)
		return
	}

	messages := []Message{
		{Role: "system", Content: a.buildPromptForSession(session)},
		{Role: "user", Content: a.redact(contextText)},
	}
	if planData, err := os.ReadFile(session.PlanPath); err == nil {
		plan := strings.TrimSpace(string(planData))
		if plan != "" {
			messages = append(messages, Message{Role: "user", Content: "Current plan from plan.md:\n" + a.redact(plan)})
		}
	}
	messages = append(messages, history...)

	response, record, err := a.client.ChatDetailed(messages)
	if err := session.AppendLLMCall(record); err != nil {
		log.Printf("append llm call session %d: %v", session.IssueNumber, err)
	}
	ApplyLLMCallState(&state, record)
	if err := session.SaveState(state); err != nil {
		log.Printf("save llm call state session %d: %v", session.IssueNumber, err)
	}
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
	toolCalls := choice.Message.ToolCalls

	if len(toolCalls) > 0 {
		snapshotHash := contextHash
		if state.PendingContextHash != "" {
			snapshotHash = state.PendingContextHash
		}

		history = append(history, Message{
			Role:             "assistant",
			Content:          content,
			Refusal:          choice.Message.Refusal,
			ReasoningContent: choice.Message.ReasoningContent,
			ToolCalls:        toolCalls,
		})
		for _, call := range toolCalls {
			result := a.executeToolCall(session, call)
			result.Stdout = a.redact(result.Stdout)
			result.Stderr = a.redact(result.Stderr)
			result.Error = a.redact(result.Error)
			resultData, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				return
			}
			history = append(history, Message{Role: "tool", ToolCallID: call.ID, Content: string(resultData)})
			marker := fmt.Sprintf("tool_call_id: %s\ntool: %s\nresult: %s", call.ID, call.Function.Name, string(resultData))
			if err := session.AppendContext(a.redact(marker)); err != nil {
				log.Printf("append tool result session %d: %v", session.IssueNumber, err)
				return
			}
		}
		if err := session.SaveHistory(history); err != nil {
			log.Printf("save history session %d: %v", session.IssueNumber, err)
			return
		}
		state.ContextHash = snapshotHash
		state.PendingContextHash = ""
		state.Done = false
		if err := session.SaveState(state); err != nil {
			log.Printf("save state after tool result %d: %v", session.IssueNumber, err)
			return
		}
		log.Printf("session %d tool_calls=%d", session.IssueNumber, len(toolCalls))
		return
	}

	if strings.TrimSpace(content) == "" || reason == "length" {
		log.Printf("session %d: incomplete response reason=%s completion_tokens=%d", session.IssueNumber, reason, response.Usage.CompletionTokens)
		feedback := "The previous model response contained no complete action. Return exactly one tool call using the tool-calling interface, or exactly Done."
		if choice.Message.Refusal != "" {
			feedback = "The previous model response was refused and was not executed. Return exactly one tool call using the tool-calling interface, or exactly Done."
		}
		if err := session.AppendContext(feedback); err != nil {
			log.Printf("append incomplete feedback session %d: %v", session.IssueNumber, err)
			return
		}
		if err := a.finishPending(session, &state, contextHash); err != nil {
			log.Printf("save pending after incomplete session %d: %v", session.IssueNumber, err)
			return
		}
		return
	}

	if strings.TrimSpace(content) == "Done" {
		if err := a.finishPending(session, &state, contextHash); err != nil {
			log.Printf("save pending after done session %d: %v", session.IssueNumber, err)
			return
		}
		state.Done = true
		if err := session.SaveState(state); err != nil {
			log.Printf("save state %d: %v", session.IssueNumber, err)
		}
		log.Printf("session %d: Done", session.IssueNumber)
		return
	}

	if err := session.AppendContext("Assistant text was received without a tool call and was not executed. Return exactly one tool call using the tool-calling interface, or exactly Done."); err != nil {
		log.Printf("append non-tool feedback session %d: %v", session.IssueNumber, err)
		return
	}
	if err := a.finishPending(session, &state, contextHash); err != nil {
		log.Printf("save pending after non-tool session %d: %v", session.IssueNumber, err)
		return
	}
	log.Printf("session %d: assistant text ignored", session.IssueNumber)
}

func (a *Agent) finishPending(session *Session, st *SessionState, snapshotHash string) error {
	st.ContextHash = snapshotHash
	st.PendingContextHash = ""
	st.Done = false
	return session.SaveState(*st)
}

func (a *Agent) compactContext(session *Session, contextText string) (string, error) {
	if estimateTokens(contextText) <= a.cfg.ContextCompactThreshold {
		return contextText, nil
	}
	archiveDir := filepath.Join(session.Dir, "context_archive")
	if err := os.MkdirAll(archiveDir, 0700); err != nil {
		return "", err
	}
	archivePath := filepath.Join(archiveDir, fmt.Sprintf("context-%s.log", time.Now().UTC().Format("20060102T150405.000000000")))
	if err := os.WriteFile(archivePath, []byte(contextText), 0600); err != nil {
		return "", err
	}
	keep := a.cfg.ContextKeepTokens
	if keep <= 0 {
		keep = 100000
	}
	const headLimit = 4000
	head := contextText
	if len(head) > headLimit {
		head = head[:headLimit]
	}
	recent := tailByTokens(contextText, keep)
	compacted := fmt.Sprintf("Context compacted at %s. Full previous context archived at %s.\n\n## Early context summary\n%s\n\n## Recent context\n%s", time.Now().UTC().Format(time.RFC3339), archivePath, head, recent)
	if err := os.WriteFile(session.ContextPath, []byte(compacted), 0600); err != nil {
		return "", err
	}
	return compacted, nil
}

func (a *Agent) workDir(session *Session) string {
	if session != nil && isGitWorktree(session.WorktreeDir) {
		return session.WorktreeDir
	}
	if a.cfg != nil && a.cfg.RepoDir != "" {
		return a.cfg.RepoDir
	}
	if session != nil {
		return session.Dir
	}
	return ""
}

func (a *Agent) sessionEnv(session *Session) []string {
	if session == nil {
		return nil
	}
	env := []string{"ITERXP_SESSION_DIR=" + session.Dir}
	if isGitWorktree(session.WorktreeDir) {
		env = append(env, "ITERXP_WORKTREE_DIR="+session.WorktreeDir)
	}
	return env
}

func (a *Agent) toolTimeout() time.Duration {
	if a.cfg != nil && a.cfg.ToolTimeout > 0 {
		return a.cfg.ToolTimeout
	}
	return 2 * time.Minute
}

func (a *Agent) killTimeout() time.Duration {
	if a.cfg != nil && a.cfg.KillTimeout > 0 {
		return a.cfg.KillTimeout
	}
	return 5 * time.Second
}

func readFileString(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func (a *Agent) runCommand(name string, args []string, dir string, extraEnv []string) (string, string, int) {
	return a.runCommandInput(name, args, dir, extraEnv, "")
}

func (a *Agent) runBash(command string, session *Session) (string, string, int) {
	toolPath := filepath.Join(session.Dir, "tool.sh")
	if err := os.WriteFile(toolPath, []byte(command+"\n"), 0700); err != nil {
		return "", err.Error(), -1
	}
	return a.runCommand("bash", []string{"-e", "-o", "pipefail", toolPath}, a.workDir(session), a.sessionEnv(session))
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
	return a.runCommand(tool.Path, args, a.workDir(session), a.sessionEnv(session))
}
