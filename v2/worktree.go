package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func pathExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func isGitWorktree(path string) bool {
	return pathExists(filepath.Join(path, ".git"))
}

func runGit(dir string, args ...string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("empty git directory")
	}
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func isGitRepo(dir string) bool {
	_, err := runGit(dir, "rev-parse", "--git-dir")
	return err == nil
}

func branchExists(dir, branch string) bool {
	_, err := runGit(dir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func mainBaseCommit(dir string) (string, error) {
	for _, ref := range []string{"refs/heads/main", "refs/remotes/origin/main", "HEAD"} {
		out, err := runGit(dir, "rev-parse", "--verify", ref)
		if err == nil && out != "" {
			return out, nil
		}
	}
	return "", fmt.Errorf("cannot determine main base commit in %s", dir)
}

func ensureWorktree(cfg Config, session *Session) error {
	if session == nil {
		return fmt.Errorf("nil session")
	}
	if session.IssueNumber <= 0 {
		return fmt.Errorf("invalid issue number: %d", session.IssueNumber)
	}
	if cfg.RepoDir == "" {
		return nil
	}
	if !isGitRepo(cfg.RepoDir) {
		return fmt.Errorf("main repository %s is not a git repository", cfg.RepoDir)
	}

	worktreeDir := session.WorktreeDir
	if worktreeDir == "" {
		worktreeDir = filepath.Join(session.Dir, "worktree")
		session.WorktreeDir = worktreeDir
	}

	st, err := session.LoadState()
	if err != nil {
		return err
	}

	if isGitWorktree(worktreeDir) {
		branch, err := runGit(worktreeDir, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return fmt.Errorf("read worktree branch: %w", err)
		}
		if branch == "HEAD" {
			branch = ""
		}
		if st.WorktreePath != "" && st.WorktreePath != worktreeDir {
			return fmt.Errorf("recorded worktree path %s does not match existing %s", st.WorktreePath, worktreeDir)
		}
		if st.WorktreeBranch != "" && branch != "" && st.WorktreeBranch != branch {
			return fmt.Errorf("worktree branch mismatch: recorded %s, actual %s", st.WorktreeBranch, branch)
		}
		base := st.WorktreeBaseCommit
		if base == "" {
			base, err = runGit(worktreeDir, "rev-parse", "HEAD")
			if err != nil {
				return fmt.Errorf("read worktree head: %w", err)
			}
		}
		st.WorktreePath = worktreeDir
		if branch != "" {
			st.WorktreeBranch = branch
		}
		if st.WorktreeBranch == "" {
			st.WorktreeBranch = fmt.Sprintf("issue/%d", session.IssueNumber)
		}
		st.WorktreeBaseCommit = base
		if st.IssueID == 0 && session.IssueID > 0 {
			st.IssueID = session.IssueID
		}
		if st.IssueNumber == 0 {
			st.IssueNumber = session.IssueNumber
		}
		return session.SaveState(st)
	}

	if pathExists(worktreeDir) {
		return fmt.Errorf("worktree path %s exists but is not a git worktree; refusing to reset or relocate", worktreeDir)
	}
	if st.WorktreePath != "" && st.WorktreePath != worktreeDir {
		return fmt.Errorf("recorded worktree path %s does not match expected %s; refusing relocation", st.WorktreePath, worktreeDir)
	}
	if st.WorktreePath != "" && !pathExists(st.WorktreePath) {
		return fmt.Errorf("recorded worktree %s is missing; refusing to recreate", st.WorktreePath)
	}

	branch := fmt.Sprintf("issue/%d", session.IssueNumber)
	base, err := mainBaseCommit(cfg.RepoDir)
	if err != nil {
		return err
	}
	args := []string{"worktree", "add"}
	if branchExists(cfg.RepoDir, branch) {
		args = append(args, worktreeDir, branch)
	} else {
		args = append(args, "-b", branch, worktreeDir, base)
	}
	if _, err := runGit(cfg.RepoDir, args...); err != nil {
		return fmt.Errorf("create worktree %s: %w", worktreeDir, err)
	}

	st.WorktreePath = worktreeDir
	st.WorktreeBranch = branch
	st.WorktreeBaseCommit = base
	if st.IssueID == 0 && session.IssueID > 0 {
		st.IssueID = session.IssueID
	}
	if st.IssueNumber == 0 {
		st.IssueNumber = session.IssueNumber
	}
	return session.SaveState(st)
}

func (a *Agent) ensureSessionWorktree(session *Session) error {
	if a == nil || a.cfg == nil || a.cfg.RepoDir == "" {
		return nil
	}
	if !isGitRepo(a.cfg.RepoDir) {
		return nil
	}
	return ensureWorktree(*a.cfg, session)
}
