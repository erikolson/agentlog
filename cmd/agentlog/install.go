package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// runInstallHook wires agentlog's PostToolUse hook into a settings.json.
//
// It edits a file the user did not write and cannot easily reconstruct, so the
// whole command is organized around not damaging it: refuse what we cannot
// parse, back up what we are about to change, add rather than replace, and do
// nothing at all if the hook is already there.
func runInstallHook(args []string) {
	fs := flag.NewFlagSet("install-hook", flag.ExitOnError)
	global := fs.Bool("global", false, "install into ~/.claude/settings.json")
	project := fs.Bool("project", false, "install into ./.claude/settings.json")
	settings := fs.String("settings", "", "explicit settings.json path; overrides --global/--project")
	dryRun := fs.Bool("dry-run", false, "print the resulting settings.json and write nothing")
	_ = fs.Parse(args)

	t, err := resolveTarget(*global, *project, *settings, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentlog:", err)
		os.Exit(1)
	}

	// A file that exists but will not parse is left strictly alone.
	sf, err := loadSettings(t.settingsPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentlog:", err)
		fmt.Fprintln(os.Stderr, "agentlog: refusing to modify a file I cannot parse; nothing was written")
		os.Exit(1)
	}

	// Idempotence: an existing agentlog hook means there is nothing to do, and
	// doing nothing must not cost the user a rewritten file or a backup.
	if hasAgentlogHook(sf.data) {
		fmt.Printf("already installed: %s already runs an agentlog hook\n", t.settingsPath)
		os.Exit(0)
	}

	if err := addHookStanza(sf.data, t.hookCommand); err != nil {
		fmt.Fprintln(os.Stderr, "agentlog:", err)
		os.Exit(1)
	}
	out, err := renderSettings(sf.data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentlog:", err)
		os.Exit(1)
	}

	if *dryRun {
		fmt.Print(string(out))
		fmt.Fprintf(os.Stderr, "dry run: %s not written\n", t.settingsPath)
		return
	}

	if err := writeSettings(t, sf, out); err != nil {
		fmt.Fprintln(os.Stderr, "agentlog:", err)
		os.Exit(1)
	}

	if sf.exists {
		fmt.Printf("backed up: %s\n", t.settingsPath+".bak")
	}
	fmt.Printf("installed: %s now runs `%s`\n", t.settingsPath, t.hookCommand)
}

// writeSettings backs up the original before replacing it, so an install is
// always one `mv` away from undone. The backup holds the exact bytes that were
// on disk — not a re-encoding — because the point of it is to restore what the
// user had, formatting included.
//
// A file that did not exist gets no backup: there is nothing to preserve, and
// creating is not clobbering.
func writeSettings(t target, sf settingsFile, out []byte) error {
	if err := os.MkdirAll(filepath.Dir(t.settingsPath), 0o755); err != nil {
		return err
	}
	if sf.exists {
		if err := os.WriteFile(t.settingsPath+".bak", sf.raw, 0o644); err != nil {
			return fmt.Errorf("writing backup, so nothing was changed: %w", err)
		}
	}
	return os.WriteFile(t.settingsPath, out, 0o644)
}
