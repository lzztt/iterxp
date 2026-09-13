package main

import "testing"

func TestRedact(t *testing.T) {
	redactStrings = []string{"secret-token", "another-secret"}
	got := redact("use secret-token now")
	want := "use [REDACTED] now"
	if got != want {
		t.Fatalf("redact = %q, want %q", got, want)
	}
}
