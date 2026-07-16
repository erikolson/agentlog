package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func okf(format string, a ...any)   { fmt.Printf("ok    "+format+"\n", a...) }
func warnf(format string, a ...any) { fmt.Printf("warn  "+format+"\n", a...) }

// runDoctor answers one question: is the black box actually recording?
//
// It is a report, not a gate. Nothing here modifies anything, and an unwired
// hook or an empty log is a finding to print, not a failure to exit on — the
// same stance the logger itself takes toward the session it observes. The only
// nonzero exit is doctor being unable to look, which is a different claim from
// having looked and found nothing.
func runDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	global := fs.Bool("global", false, "inspect ~/.claude/settings.json and ~/.agentlog")
	project := fs.Bool("project", false, "inspect ./.claude/settings.json and ./.agentlog (default)")
	settings := fs.String("settings", "", "explicit settings.json path; overrides --global/--project")
	_ = fs.Parse(args)

	// Read-only, so a default target is friendly rather than reckless.
	t, err := resolveTarget(*global, *project, *settings, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentlog:", err)
		os.Exit(1)
	}

	okf("agentlog %s", version)

	// A settings file that will not parse means doctor cannot answer the
	// question it was asked, which is the one thing worth failing over.
	sf, err := loadSettings(t.settingsPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentlog:", err)
		os.Exit(1)
	}
	switch {
	case !sf.exists:
		warnf("no settings file at %s — nothing is wired; try `agentlog install-hook`", t.settingsPath)
	case hasAgentlogHook(sf.data):
		okf("hook wired in %s", t.settingsPath)
	default:
		warnf("no agentlog hook in %s — try `agentlog install-hook`", t.settingsPath)
	}

	dir, err := t.logDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentlog:", err)
		os.Exit(1)
	}
	reportLogDir(dir)
}

// reportLogDir prints where events land and whether they can.
func reportLogDir(dir string) {
	abs := dir
	if a, err := filepath.Abs(dir); err == nil {
		abs = a
	}

	fi, err := os.Stat(dir)
	switch {
	case os.IsNotExist(err):
		// Not a problem: the logger creates it on first write.
		warnf("log dir %s does not exist yet — it is created on the first event", abs)
		return
	case err != nil:
		warnf("log dir %s cannot be read: %v", abs, err)
		return
	case !fi.IsDir():
		warnf("log dir %s exists but is not a directory — nothing can be logged there", abs)
		return
	}

	// Permission bits rather than a probe file: doctor does not write, not even
	// briefly, into a directory whose whole job is to be an append-only record.
	// This reads the owner write bit, so it can be optimistic about a directory
	// owned by someone else — a wrong answer here is a warning, not a gate.
	if fi.Mode().Perm()&0o200 == 0 {
		warnf("log dir %s is not writable — events will be dropped", abs)
	} else {
		okf("log dir %s is writable", abs)
	}

	reportToday(dir, abs)
}

// reportToday summarizes the file the logger would append to right now.
func reportToday(dir, abs string) {
	name := time.Now().UTC().Format("2006-01-02") + ".jsonl"
	path := filepath.Join(dir, name)

	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		warnf("no events today: %s does not exist", filepath.Join(abs, name))
		return
	}
	if err != nil {
		warnf("cannot read %s: %v", filepath.Join(abs, name), err)
		return
	}

	var events, malformed int
	runs := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) != nil {
			malformed++
			continue
		}
		events++
		if id, ok := e["run_id"].(string); ok {
			runs[id] = true
		}
	}

	okf("today %s: %d events across %d runs", name, events, len(runs))
	if malformed > 0 {
		// Someone else is appending to this file, or a write was torn.
		warnf("today %s: %d malformed lines (not valid JSON)", name, malformed)
	}
}
