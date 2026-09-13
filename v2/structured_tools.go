package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ToolDefinition struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

func builtinToolDefinitions() []ToolDefinition {
	return []ToolDefinition{
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "bash",
				Description: "Execute a bash command in the assigned issue worktree with fail-fast shell options. Returns structured stdout, stderr, and exit_code.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{
							"type":        "string",
							"description": "Bash script body to execute. It runs with bash -e -o pipefail from the issue worktree.",
						},
					},
					"required": []string{"command"},
				},
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "finish_issue",
				Description: "Record the issue handoff note and issue type label to finalize the issue. The runtime will post the handoff note as a comment, apply the issue type label, and close the issue after Done.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"handoff_note": map[string]any{
							"type":        "string",
							"description": "Concise handoff note for future agents: root cause, trigger, fix, and what is not fixed. Highest signal-to-noise ratio.",
						},
						"issue_type_label": map[string]any{
							"type":        "string",
							"description": "Short kebab-case issue type label used to query similar past issues, e.g. state-machine-bug, deployment-gap, completion-discipline-gap.",
						},
					},
					"required": []string{"handoff_note", "issue_type_label"},
				},
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "apply_patch",
				Description: "Apply a unified diff patch to files inside the assigned issue worktree. The patch is passed to git apply through stdin. Creates, edits, and deletes files without rewriting unrelated content.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"patch": map[string]any{
							"type":        "string",
							"description": "Unified diff to apply. Paths must resolve inside the assigned issue worktree.",
						},
					},
					"required": []string{"patch"},
				},
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "web_fetch",
				Description: "Fetch a public HTTP(S) URL and return readable Markdown (HTML is converted), plain text, or pretty-printed JSON. Rejects non-public/loopback destinations, enforces a 30s timeout, 5 redirects, a 2 MiB body limit, and a 20,000-character content limit.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"url": map[string]any{
							"type":        "string",
							"description": "Public HTTP(S) URL to fetch. Static content only; client-rendered pages will not work.",
						},
					},
					"required": []string{"url"},
				},
			},
		},
	}
}

func builtinToolsBlock() string {
	return `

# Tool calling
Use the model tool-calling interface to select exactly one tool call per assistant response.
Available tools:
- bash: Execute a bash command in the issue worktree.
- apply_patch: Apply a unified diff patch in the issue worktree.
- finish_issue: Record a concise handoff note and issue type label for the issue, then return Done to close it.
- web_fetch: Fetch a public HTTP(S) URL and return readable Markdown, plain text, or JSON.
Do not return Markdown fences, legacy tool markup, XML tags, or raw shell source as executable actions.
Assistant prose and reasoning are never executed. When no further action is needed, return the single word Done as assistant text.`
}

func (a *Agent) redact(s string) string {
	if a != nil && a.client != nil {
		return a.client.Redact(s)
	}
	return s
}

func (a *Agent) toolResultText(call ToolCall, result ToolResult) string {
	result.Stdout = a.redact(result.Stdout)
	result.Stderr = a.redact(result.Stderr)
	result.Error = a.redact(result.Error)
	data, err := json.Marshal(result)
	if err != nil {
		data = []byte(fmt.Sprintf(`{"stdout":"","stderr":%q,"exit_code":%d,"error":"marshal result: %s"}`, result.Stderr, result.ExitCode, err.Error()))
	}
	return fmt.Sprintf("tool_call_id: %s\ntool: %s\nresult: %s", call.ID, call.Function.Name, data)
}

func (a *Agent) executeToolCall(session *Session, call ToolCall) ToolResult {
	name := strings.TrimSpace(call.Function.Name)
	switch name {
	case "bash":
		var args struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return ToolResult{ExitCode: -1, Error: "invalid bash arguments: " + err.Error()}
		}
		if strings.TrimSpace(args.Command) == "" {
			return ToolResult{ExitCode: -1, Error: "bash command is empty"}
		}
		stdout, stderr, code := a.runBash(args.Command, session)
		return ToolResult{Stdout: stdout, Stderr: stderr, ExitCode: code}
	case "apply_patch":
		var args struct {
			Patch string `json:"patch"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return ToolResult{ExitCode: -1, Error: "invalid apply_patch arguments: " + err.Error()}
		}
		stdout, stderr, code := a.applyPatch(session, args.Patch)
		return ToolResult{Stdout: stdout, Stderr: stderr, ExitCode: code}
	case "finish_issue":
		var args struct {
			HandoffNote    string `json:"handoff_note"`
			IssueTypeLabel string `json:"issue_type_label"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return ToolResult{ExitCode: -1, Error: "invalid finish_issue arguments: " + err.Error()}
		}
		if strings.TrimSpace(args.HandoffNote) == "" {
			return ToolResult{ExitCode: -1, Error: "handoff_note is empty"}
		}
		if strings.TrimSpace(args.IssueTypeLabel) == "" {
			return ToolResult{ExitCode: -1, Error: "issue_type_label is empty"}
		}
		if err := a.recordHandoff(session, args.HandoffNote, args.IssueTypeLabel); err != nil {
			return ToolResult{ExitCode: -1, Error: err.Error()}
		}
		return ToolResult{Stdout: "handoff recorded", Stderr: "", ExitCode: 0}
	case "web_fetch":
		var args struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return ToolResult{ExitCode: -1, Error: "invalid web_fetch arguments: " + err.Error()}
		}
		if strings.TrimSpace(args.URL) == "" {
			return ToolResult{ExitCode: -1, Error: "web_fetch URL is empty"}
		}
		return a.webFetchTool(session, call.Function.Arguments)
	default:
		return ToolResult{ExitCode: -1, Error: "unsupported tool call: " + name}
	}
}

func (a *Agent) runCommandInput(name string, args []string, dir string, extraEnv []string, input string) (string, string, int) {
	timeout := a.toolTimeout()
	killTimeout := a.killTimeout()

	stdoutFile, err := os.CreateTemp("", "iterxp-stdout-")
	if err != nil {
		return "", err.Error(), -1
	}
	stdoutPath := stdoutFile.Name()
	defer os.Remove(stdoutPath)

	stderrFile, err := os.CreateTemp("", "iterxp-stderr-")
	if err != nil {
		_ = stdoutFile.Close()
		return "", err.Error(), -1
	}
	stderrPath := stderrFile.Name()
	defer os.Remove(stderrPath)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var gitName, gitEmail = defaultGitName, defaultGitEmail
	if a != nil && a.cfg != nil {
		gitName, gitEmail = resolvedGitIdentity(a.cfg.GitName, a.cfg.GitEmail)
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = withGitIdentityEnv(os.Environ(), extraEnv, gitName, gitEmail)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile

	if err := cmd.Start(); err != nil {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return readFileString(stdoutPath), err.Error() + "\n" + readFileString(stderrPath), -1
	}
	_ = stdoutFile.Close()
	_ = stderrFile.Close()

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	timedOut := false
	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-ctx.Done():
		timedOut = true
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		killTimer := time.NewTimer(killTimeout)
		select {
		case waitErr = <-waitCh:
		case <-killTimer.C:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			waitErr = <-waitCh
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	stdout := readFileString(stdoutPath)
	stderr := readFileString(stderrPath)
	code := 0
	if timedOut {
		code = 124
		stderr += fmt.Sprintf("\ncommand timed out after %s", timeout)
	} else if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			code = -1
			stderr += waitErr.Error()
		}
	}
	return stdout, stderr, code
}

func (a *Agent) applyPatch(session *Session, patch string) (string, string, int) {
	if strings.TrimSpace(patch) == "" {
		return "", "apply_patch patch is empty", -1
	}
	workDir := a.workDir(session)
	if !isGitWorktree(workDir) {
		return "", "apply_patch requires the assigned issue git worktree", -1
	}
	if err := validatePatchPaths(workDir, normalizePatch(patch)); err != nil {
		return "", err.Error(), -1
	}
	patch = normalizePatch(patch)
	checkStdout, checkStderr, code := a.runCommandInput("git", []string{"apply", "--check"}, workDir, a.sessionEnv(session), patch)
	if code != 0 {
		return checkStdout, checkStderr + "\ngit apply --check failed", code
	}
	return a.runCommandInput("git", []string{"apply"}, workDir, a.sessionEnv(session), patch)
}

func normalizePatch(patch string) string {
	if patch == "" {
		return patch
	}
	if !strings.HasSuffix(patch, "\n") {
		return patch + "\n"
	}
	return patch
}

func validatePatchPath(workDir, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "/dev/null" {
		return nil
	}
	if strings.HasPrefix(raw, `"`) {
		if unquoted, err := strconv.Unquote(raw); err == nil {
			raw = unquoted
		}
	}
	if tab := strings.IndexByte(raw, '\t'); tab >= 0 {
		raw = raw[:tab]
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "/dev/null" {
		return nil
	}
	if len(raw) > 2 && raw[1] == '/' && (raw[0] == 'a' || raw[0] == 'b') {
		raw = raw[2:]
	}
	if filepath.IsAbs(raw) {
		return fmt.Errorf("patch path outside worktree: %s", raw)
	}
	clean := filepath.Clean(raw)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("patch path outside worktree: %s", raw)
	}
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return err
	}
	realWorkDir, err := filepath.EvalSymlinks(absWorkDir)
	if err != nil {
		realWorkDir = absWorkDir
	}
	target := filepath.Join(realWorkDir, clean)
	rel, err := filepath.Rel(realWorkDir, target)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("patch path outside worktree: %s", raw)
	}
	return nil
}

func validatePatchPaths(workDir, patch string) error {
	found := false
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "--- "):
			if err := validatePatchPath(workDir, strings.TrimSpace(strings.TrimPrefix(line, "--- "))); err != nil {
				return err
			}
			found = true
		case strings.HasPrefix(line, "+++ "):
			if err := validatePatchPath(workDir, strings.TrimSpace(strings.TrimPrefix(line, "+++ "))); err != nil {
				return err
			}
			found = true
		case strings.HasPrefix(line, "diff --git "):
			for _, part := range strings.Fields(strings.TrimPrefix(line, "diff --git ")) {
				if err := validatePatchPath(workDir, part); err != nil {
					return err
				}
			}
			found = true
		case strings.HasPrefix(line, "rename from "):
			if err := validatePatchPath(workDir, strings.TrimSpace(strings.TrimPrefix(line, "rename from "))); err != nil {
				return err
			}
			found = true
		case strings.HasPrefix(line, "rename to "):
			if err := validatePatchPath(workDir, strings.TrimSpace(strings.TrimPrefix(line, "rename to "))); err != nil {
				return err
			}
			found = true
		case strings.HasPrefix(line, "copy from "):
			if err := validatePatchPath(workDir, strings.TrimSpace(strings.TrimPrefix(line, "copy from "))); err != nil {
				return err
			}
			found = true
		case strings.HasPrefix(line, "copy to "):
			if err := validatePatchPath(workDir, strings.TrimSpace(strings.TrimPrefix(line, "copy to "))); err != nil {
				return err
			}
			found = true
		}
	}
	if !found {
		return fmt.Errorf("no patch paths found in apply_patch input")
	}
	return nil
}
