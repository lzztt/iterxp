package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFakeCLI(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCLIInventoryBlockAvailableMissingNewlyAdded(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	rgPath := writeFakeCLI(t, bin, "rg")
	newPath := writeFakeCLI(t, bin, "newcmd")

	cmds := []CLICommand{
		{Name: "rg", Purpose: "ripgrep search"},
		{Name: "tree", Purpose: "directory tree"},
		{Name: "newcmd", Purpose: "new command"},
	}

	block := cliInventoryBlock(cmds)
	wantRG := fmt.Sprintf("- rg -> %s (ripgrep search)", rgPath)
	if !strings.Contains(block, wantRG) {
		t.Fatalf("inventory missing available rg line %q:\n%s", wantRG, block)
	}
	wantNew := fmt.Sprintf("- newcmd -> %s (new command)", newPath)
	if !strings.Contains(block, wantNew) {
		t.Fatalf("inventory missing available newcmd line %q:\n%s", wantNew, block)
	}
	if !strings.Contains(block, "## Missing CLI commands\n- tree\n") {
		t.Fatalf("inventory missing unavailable tree line:\n%s", block)
	}

	treePath := writeFakeCLI(t, bin, "tree")
	block = cliInventoryBlock(cmds)
	wantTree := fmt.Sprintf("- tree -> %s (directory tree)", treePath)
	if !strings.Contains(block, wantTree) {
		t.Fatalf("inventory missing newly added tree line %q:\n%s", wantTree, block)
	}
	if !strings.Contains(block, "## Missing CLI commands\n(none)\n") {
		t.Fatalf("inventory should have no missing commands after adding tree:\n%s", block)
	}
}

func TestCLIInventoryBlockAllMissing(t *testing.T) {
	empty := t.TempDir()
	t.Setenv("PATH", empty)
	block := cliInventoryBlock([]CLICommand{{Name: "ghost-command", Purpose: "ghost"}})
	if !strings.Contains(block, "## Available CLI commands\n(none)\n") {
		t.Fatalf("inventory available section should be empty:\n%s", block)
	}
	if !strings.Contains(block, "## Missing CLI commands\n- ghost-command\n") {
		t.Fatalf("inventory missing unavailable command:\n%s", block)
	}
}

func TestBuildPromptIncludesCLIInventory(t *testing.T) {
	repoDir := t.TempDir()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, "AGENTS.md"), []byte("Build prompt agent context."), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	cfg := Config{RepoDir: repoDir, ToolsDir: filepath.Join(repoDir, "tools")}
	a := &Agent{cfg: &cfg}
	prompt := a.buildPrompt()
	if !strings.Contains(prompt, "## Available CLI commands") {
		t.Fatalf("buildPrompt missing available CLI inventory:\n%s", prompt)
	}
	if !strings.Contains(prompt, "## Missing CLI commands") {
		t.Fatalf("buildPrompt missing missing CLI inventory:\n%s", prompt)
	}
}
