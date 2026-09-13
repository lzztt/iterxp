package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCLIHelpExitsWithoutConfigOrSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ITERXP_CONFIG_DIR", filepath.Join(home, ".iterxp_v2"))

	var stdout, stderr bytes.Buffer
	code := runCLI([]string{"-h"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("help exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), usageText) {
		t.Fatalf("help output missing usage text: %q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".iterxp_v2")); !os.IsNotExist(err) {
		t.Fatalf("-h created config dir: %v", err)
	}
}

func TestRunCLIRejectsUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runCLI([]string{"--definitely-not-a-flag"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("unknown flag exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown flag") {
		t.Fatalf("stderr = %q, want unknown flag message", stderr.String())
	}
}

func TestAcquireWorkerLockRejectsSecondOwner(t *testing.T) {
	dir := t.TempDir()
	unlock1, err := acquireWorkerLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock1()

	unlock2, err := acquireWorkerLock(dir)
	if err == nil {
		unlock2()
		t.Fatal("second lock on same config dir succeeded")
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("second lock error = %q, want already in use", err)
	}
}

func TestParseArgsIssueWorkerModes(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantIssue int
		wantErr   string
	}{
		{name: "space-form", args: []string{"--issue", "7"}, wantIssue: 7},
		{name: "equals-form", args: []string{"--issue=9"}, wantIssue: 9},
		{name: "zero", args: []string{"--issue", "0"}, wantErr: "invalid issue number"},
		{name: "negative", args: []string{"--issue=-1"}, wantErr: "invalid issue number"},
		{name: "missing", args: []string{"--issue"}, wantErr: "requires a positive issue number"},
		{name: "duplicate", args: []string{"--issue", "7", "--issue=8"}, wantErr: "specified multiple times"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			help, issueNumber, err := parseArgs(tt.args)
			if help {
				t.Fatalf("help = true, want false")
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseArgs(%q) error = %v, want containing %q", tt.args, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseArgs(%q) error = %v", tt.args, err)
			}
			if issueNumber != tt.wantIssue {
				t.Fatalf("parseArgs(%q) issue = %d, want %d", tt.args, issueNumber, tt.wantIssue)
			}
		})
	}
}

func TestAcquireSessionWorkerLockIsPerSession(t *testing.T) {
	dir := t.TempDir()
	s1, err := NewSession(Issue{Number: 1}, dir)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := NewSession(Issue{Number: 2}, dir)
	if err != nil {
		t.Fatal(err)
	}

	unlock1, err := acquireSessionWorkerLock(s1)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock1()

	if _, err := acquireSessionWorkerLock(s1); err == nil {
		t.Fatal("second lock on same issue session succeeded")
	}

	unlock2, err := acquireSessionWorkerLock(s2)
	if err != nil {
		t.Fatalf("different issue session could not be locked concurrently: %v", err)
	}
	unlock2()
}
