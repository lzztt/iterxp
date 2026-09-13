package main

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

type CLICommand struct {
	Name    string
	Purpose string
}

var defaultCLICommands = []CLICommand{
	{Name: "rg", Purpose: "recursive grep with regex"},
	{Name: "jq", Purpose: "JSON processor"},
	{Name: "gh", Purpose: "GitHub CLI"},
	{Name: "tree", Purpose: "directory tree listing"},
	{Name: "git", Purpose: "version control"},
	{Name: "go", Purpose: "Go toolchain"},
	{Name: "python3", Purpose: "Python 3 interpreter"},
	{Name: "python", Purpose: "Python interpreter alias"},
	{Name: "curl", Purpose: "HTTP client"},
	{Name: "wget", Purpose: "HTTP downloader"},
	{Name: "file", Purpose: "file type detection"},
	{Name: "which", Purpose: "locate a command"},
	{Name: "find", Purpose: "filesystem search"},
	{Name: "grep", Purpose: "text search"},
	{Name: "sed", Purpose: "stream editor"},
	{Name: "awk", Purpose: "text processing"},
	{Name: "ps", Purpose: "process snapshot"},
	{Name: "ss", Purpose: "socket statistics"},
	{Name: "timeout", Purpose: "run a command with a timeout"},
	{Name: "flock", Purpose: "file locking"},
	{Name: "patch", Purpose: "apply diffs"},
}

type cliInventoryEntry struct {
	name    string
	path    string
	purpose string
}

func cliInventoryBlock(commands []CLICommand) string {
	if len(commands) == 0 {
		commands = defaultCLICommands
	}

	var available []cliInventoryEntry
	var missing []string
	seen := map[string]bool{}
	for _, cmd := range commands {
		name := strings.TrimSpace(cmd.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		path, err := exec.LookPath(name)
		if err != nil {
			missing = append(missing, name)
			continue
		}
		available = append(available, cliInventoryEntry{
			name:    name,
			path:    path,
			purpose: strings.TrimSpace(cmd.Purpose),
		})
	}

	sort.Slice(available, func(i, j int) bool { return available[i].name < available[j].name })
	sort.Strings(missing)

	var b strings.Builder
	b.WriteString("\n\n## Available CLI commands\n")
	if len(available) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, entry := range available {
			fmt.Fprintf(&b, "- %s -> %s (%s)\n", entry.name, entry.path, entry.purpose)
		}
	}
	b.WriteString("\n## Missing CLI commands\n")
	if len(missing) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, name := range missing {
			fmt.Fprintf(&b, "- %s\n", name)
		}
	}
	return b.String()
}
