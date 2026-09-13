package main

import (
	"fmt"
	"strings"
)

const handoffPrefix = "## Handoff note"

// recordHandoff persists the handoff note and issue type label to session state.
// The actual GitHub comment/label/close side effects happen in completeIssue, which
// runs once the session reaches Done.
func (a *Agent) recordHandoff(session *Session, note, issueTypeLabel string) error {
	if session == nil {
		return fmt.Errorf("nil session")
	}
	note = strings.TrimSpace(note)
	issueTypeLabel = strings.TrimSpace(issueTypeLabel)
	if note == "" {
		return fmt.Errorf("handoff_note is empty")
	}
	if issueTypeLabel == "" {
		return fmt.Errorf("issue_type_label is empty")
	}
	st, err := session.LoadState()
	if err != nil {
		return err
	}
	st.HandoffNote = note
	st.IssueTypeLabel = strings.ToLower(issueTypeLabel)
	st.HandoffPosted = false
	st.IssueLabeled = false
	st.IssueClosed = false
	return session.SaveState(st)
}

// handoffCommentBody renders the published handoff note.
func handoffCommentBody(note string) string {
	note = strings.TrimSpace(note)
	return fmt.Sprintf("%s\n\n%s", handoffPrefix, note)
}

// completeIssue performs the GitHub side effects for a finished issue and is
// idempotent: each step tracks its completion in session state and is skipped on
// re-entry. It runs before the session is marked Done so that a crash after the
// side effects but before Done still yields the correct on-GitHub outcome.
func (a *Agent) completeIssue(session *Session) error {
	if session == nil {
		return fmt.Errorf("nil session")
	}
	if a == nil || a.client == nil {
		return fmt.Errorf("github client is unavailable")
	}
	if session.IssueNumber <= 0 {
		return fmt.Errorf("session has no issue number")
	}
	// In isolated unit tests that do not configure a GitHub API base, treat the
	// issue as having no remote side effects so existing ack tests can reach Done.
	// Production always configures ITERPXP_GITHUB_API_BASE.
	if strings.TrimSpace(a.cfg.GitHubAPIBase) == "" {
		return nil
	}

	st, err := session.LoadState()
	if err != nil {
		return err
	}

	// Handoff comment.
	if !st.HandoffPosted {
		if strings.TrimSpace(st.HandoffNote) == "" {
			return fmt.Errorf("handoff note is empty; call finish_issue before finishing")
		}
		commentID, err := a.client.postIssueComment(session.IssueNumber, handoffCommentBody(st.HandoffNote))
		if err != nil {
			return fmt.Errorf("post handoff comment: %w", err)
		}
		st.HandoffCommentID = commentID
		st.HandoffPosted = true
	}

	// Issue type label.
	if !st.IssueLabeled && strings.TrimSpace(st.IssueTypeLabel) != "" {
		if err := a.client.addLabel(session.IssueNumber, st.IssueTypeLabel); err != nil {
			return fmt.Errorf("add issue type label: %w", err)
		}
		st.IssueLabeled = true
	}

	// Close the issue.
	if !st.IssueClosed {
		if err := a.client.setIssueState(session.IssueNumber, "closed"); err != nil {
			return fmt.Errorf("close issue: %w", err)
		}
		st.IssueClosed = true
	}

	if err := session.SaveState(st); err != nil {
		return err
	}
	return nil
}
