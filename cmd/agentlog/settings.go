package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The hook command install-hook writes, one per mode.
//
// Neither puts a possibly-unset variable in AGENTLOG_DIR's path position. An
// unset $CLAUDE_PROJECT_DIR expands to nothing, and `AGENTLOG_DIR=/.agentlog`
// is a broken path that fails silently on every tool call; the same expansion
// inside --attr just yields an empty annotation, which the schema allows. So
// project mode leans on the cwd default (.agentlog) rather than naming a path
// it cannot be sure of.
const (
	globalHookCommand  = "AGENTLOG_DIR=$HOME/.agentlog agentlog hook --attr project=$CLAUDE_PROJECT_DIR"
	projectHookCommand = "agentlog hook --attr project=$CLAUDE_PROJECT_DIR"
)

// agentlogHookMarker is what identifies our stanza in someone else's settings
// file. It is deliberately the command substring rather than a sentinel key:
// the file belongs to the user, and a hand-written agentlog hook should count
// as installed just as much as one we wrote.
const agentlogHookMarker = "agentlog hook"

// target says which settings file to act on and which hook command belongs in
// it. The two travel together because they are one decision: a global install
// needs an absolute log dir, a project install needs the cwd default.
type target struct {
	settingsPath string
	hookCommand  string
	global       bool
}

// resolveTarget turns the selector flags into a target.
//
// requireSelector is the difference between the two commands. install-hook
// mutates a file, so it refuses to guess which one; doctor only reads, so it
// defaults to project rather than making you say it.
func resolveTarget(global, project bool, settings string, requireSelector bool) (target, error) {
	if global && project {
		return target{}, errors.New("--global and --project are mutually exclusive")
	}
	if requireSelector && !global && !project && settings == "" {
		return target{}, errors.New("pick a target: --global, --project, or --settings PATH")
	}

	t := target{global: global, hookCommand: projectHookCommand}
	if global {
		t.hookCommand = globalHookCommand
	}

	switch {
	case settings != "":
		t.settingsPath = settings // explicit path wins; mode still picks the command
	case global:
		home, err := os.UserHomeDir()
		if err != nil {
			return target{}, fmt.Errorf("locating home directory: %w", err)
		}
		t.settingsPath = filepath.Join(home, ".claude", "settings.json")
	default:
		t.settingsPath = filepath.Join(".claude", "settings.json")
	}
	return t, nil
}

// logDir reports where events will actually land, mirroring what the hook
// command in each mode arranges. AGENTLOG_DIR always wins, exactly as it does
// for the emit and hook paths.
func (t target) logDir() (string, error) {
	if d := os.Getenv("AGENTLOG_DIR"); d != "" {
		return d, nil
	}
	if t.global {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locating home directory: %w", err)
		}
		return filepath.Join(home, ".agentlog"), nil
	}
	return defaultLogDir, nil
}

// settingsFile is a settings.json as read from disk: the decoded tree, plus the
// exact bytes it came from. The raw bytes are kept because a backup must
// reproduce the file that was there, not a re-encoding of it.
type settingsFile struct {
	data   map[string]any
	raw    []byte
	exists bool
}

// loadSettings reads a settings file without judging it. A missing file is not
// an error — install-hook creates one — but a file that exists and does not
// parse is, because the only safe thing to do with JSON we cannot read is leave
// it alone.
func loadSettings(path string) (settingsFile, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return settingsFile{data: map[string]any{}}, nil
	}
	if err != nil {
		return settingsFile{}, err
	}
	s := settingsFile{raw: raw, exists: true, data: map[string]any{}}
	// An empty file is a common half-created state; treat it as an empty object
	// rather than a parse failure.
	if len(strings.TrimSpace(string(raw))) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return settingsFile{}, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	return s, nil
}

// postToolUseGroups returns the PostToolUse entries, walking defensively: this
// is a user's file and may hold any shape at any of these keys.
func postToolUseGroups(data map[string]any) []any {
	hooks, ok := data["hooks"].(map[string]any)
	if !ok {
		return nil
	}
	groups, _ := hooks["PostToolUse"].([]any)
	return groups
}

// hasAgentlogHook reports whether any PostToolUse group already runs agentlog.
// This is what makes install-hook idempotent and what doctor reports on.
func hasAgentlogHook(data map[string]any) bool {
	for _, group := range postToolUseGroups(data) {
		g, ok := group.(map[string]any)
		if !ok {
			continue
		}
		hooks, ok := g["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range hooks {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if cmd, ok := hm["command"].(string); ok && strings.Contains(cmd, agentlogHookMarker) {
				return true
			}
		}
	}
	return false
}

// addHookStanza appends our stanza to hooks.PostToolUse, creating either level
// if absent and preserving every existing key and hook.
//
// It refuses rather than replaces when either key holds something other than
// the shape we expect. Such a file parses as JSON, so it never reaches the
// parse check, and overwriting it would be exactly the clobbering this command
// exists to avoid — we cannot know what the user meant by it.
func addHookStanza(data map[string]any, command string) error {
	var hooks map[string]any
	switch h := data["hooks"].(type) {
	case nil:
		hooks = map[string]any{}
		data["hooks"] = hooks
	case map[string]any:
		hooks = h
	default:
		return fmt.Errorf("\"hooks\" is %T, not an object; refusing to overwrite it", h)
	}

	var groups []any
	switch g := hooks["PostToolUse"].(type) {
	case nil:
	case []any:
		groups = g
	default:
		return fmt.Errorf("\"hooks.PostToolUse\" is %T, not an array; refusing to overwrite it", g)
	}

	hooks["PostToolUse"] = append(groups, map[string]any{
		"matcher": "*",
		"hooks": []any{
			map[string]any{"type": "command", "command": command},
		},
	})
	return nil
}

// renderSettings encodes the tree the way a human will have to read it.
func renderSettings(data map[string]any) ([]byte, error) {
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}
