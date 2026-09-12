package main

import "testing"

func TestIsExecutable(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"echo hello", true},
		{"", false},
		{"```bash\necho hello\n```", false},
		{"Done", false},
		{"   ", false},
	}
	for _, c := range cases {
		if got := isExecutable(c.in); got != c.want {
			t.Errorf("isExecutable(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestRedact(t *testing.T) {
	redactStrings = []string{"secret-token", "another-secret"}
	got := redact("use secret-token now")
	want := "use [REDACTED] now"
	if got != want {
		t.Fatalf("redact = %q, want %q", got, want)
	}
}
