package main

import "strings"

const (
	defaultGitName  = "IterXP Agent"
	defaultGitEmail = "agent@iterxp.com"
)

func resolvedGitIdentity(name, email string) (string, string) {
	if strings.TrimSpace(name) == "" {
		name = defaultGitName
	}
	if strings.TrimSpace(email) == "" {
		email = defaultGitEmail
	}
	return name, email
}

func isGitIdentityKey(kv string) bool {
	key, _, _ := strings.Cut(kv, "=")
	switch key {
	case "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL":
		return true
	}
	return false
}

func withGitIdentityEnv(environ []string, extra []string, name, email string) []string {
	name, email = resolvedGitIdentity(name, email)
	out := make([]string, 0, len(environ)+len(extra)+4)
	for _, kv := range environ {
		if !isGitIdentityKey(kv) {
			out = append(out, kv)
		}
	}
	for _, kv := range extra {
		if !isGitIdentityKey(kv) {
			out = append(out, kv)
		}
	}
	out = append(out,
		"GIT_AUTHOR_NAME="+name,
		"GIT_AUTHOR_EMAIL="+email,
		"GIT_COMMITTER_NAME="+name,
		"GIT_COMMITTER_EMAIL="+email,
	)
	return out
}

func ensureRepoGitIdentity(cfg Config) error {
	if cfg.RepoDir == "" || !isGitRepo(cfg.RepoDir) {
		return nil
	}
	name, email := resolvedGitIdentity(cfg.GitName, cfg.GitEmail)
	if _, err := runGit(cfg.RepoDir, "config", "user.name", name); err != nil {
		return err
	}
	if _, err := runGit(cfg.RepoDir, "config", "user.email", email); err != nil {
		return err
	}
	return nil
}
