package main

import (
	"strings"
	"testing"
)

func TestWatcherResolvedGitIdentityDefaultsAndOverride(t *testing.T) {
	name, email := watcherResolvedGitIdentity("", "")
	if name != watcherDefaultGitName || email != watcherDefaultGitEmail {
		t.Fatalf("empty identity resolved to %q <%s>, want defaults", name, email)
	}
	name, email = watcherResolvedGitIdentity("Custom", "custom@example.com")
	if name != "Custom" || email != "custom@example.com" {
		t.Fatalf("override identity = %q <%s>, want Custom <custom@example.com>", name, email)
	}
}

func TestWatcherIdentityEnvReplacesInheritedValues(t *testing.T) {
	env := watcherIdentityEnv(
		[]string{
			"GIT_AUTHOR_NAME=Inherited",
			"GIT_AUTHOR_EMAIL=author@example.com",
			"GIT_COMMITTER_NAME=Inherited",
			"GIT_COMMITTER_EMAIL=committer@example.com",
			"PATH=/bin",
		},
		"IterXP Agent",
		"agent@iterxp.com",
	)
	count := 0
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		switch key {
		case "GIT_AUTHOR_NAME":
			if kv != "GIT_AUTHOR_NAME=IterXP Agent" {
				t.Fatalf("GIT_AUTHOR_NAME = %q", kv)
			}
			count++
		case "GIT_AUTHOR_EMAIL":
			if kv != "GIT_AUTHOR_EMAIL=agent@iterxp.com" {
				t.Fatalf("GIT_AUTHOR_EMAIL = %q", kv)
			}
			count++
		case "GIT_COMMITTER_NAME":
			if kv != "GIT_COMMITTER_NAME=IterXP Agent" {
				t.Fatalf("GIT_COMMITTER_NAME = %q", kv)
			}
			count++
		case "GIT_COMMITTER_EMAIL":
			if kv != "GIT_COMMITTER_EMAIL=agent@iterxp.com" {
				t.Fatalf("GIT_COMMITTER_EMAIL = %q", kv)
			}
			count++
		}
	}
	if count != 4 {
		t.Fatalf("expected 4 identity entries, found %d", count)
	}
	if len(env) != 5 {
		t.Fatalf("env length = %d, want 5 (PATH + 4 identity)", len(env))
	}
}
