package main

import "strings"

const (
	watcherDefaultGitName  = "IterXP Agent"
	watcherDefaultGitEmail = "agent@iterxp.com"
)

func watcherResolvedGitIdentity(name, email string) (string, string) {
	if strings.TrimSpace(name) == "" {
		name = watcherDefaultGitName
	}
	if strings.TrimSpace(email) == "" {
		email = watcherDefaultGitEmail
	}
	return name, email
}

func watcherIdentityEnv(environ []string, name, email string) []string {
	name, email = watcherResolvedGitIdentity(name, email)
	out := make([]string, 0, len(environ)+4)
	for _, kv := range environ {
		key, _, _ := strings.Cut(kv, "=")
		switch key {
		case "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL":
			continue
		}
		out = append(out, kv)
	}
	out = append(out,
		"GIT_AUTHOR_NAME="+name,
		"GIT_AUTHOR_EMAIL="+email,
		"GIT_COMMITTER_NAME="+name,
		"GIT_COMMITTER_EMAIL="+email,
	)
	return out
}
