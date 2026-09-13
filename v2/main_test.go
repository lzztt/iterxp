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
