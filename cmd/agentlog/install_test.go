package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runIn executes the binary with extra env, returning exit code, stdout and
// stderr.
//
// Every caller pins both --settings and HOME inside t.TempDir(). HOME is the
// belt to --settings' braces: --global resolves through os.UserHomeDir(), so a
// test that ever reached that path would edit the developer's real
// ~/.claude/settings.json. Pinning HOME makes that structurally impossible
// rather than merely unlikely.
func runIn(t *testing.T, extraEnv []string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	return code, out.String(), errOut.String()
}

// sandbox returns a temp dir plus the env that confines the binary to it.
func sandbox(t *testing.T) (dir string, env []string) {
	t.Helper()
	dir = t.TempDir()
	return dir, []string{"HOME=" + dir, "AGENTLOG_DIR=" + filepath.Join(dir, "logs")}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s is not valid JSON: %v\n%s", path, err, b)
	}
	return m
}

// commandsIn returns every PostToolUse hook command in the file, flattened.
func commandsIn(t *testing.T, data map[string]any) []string {
	t.Helper()
	var got []string
	for _, group := range postToolUseGroups(data) {
		g, ok := group.(map[string]any)
		if !ok {
			continue
		}
		hooks, _ := g["hooks"].([]any)
		for _, h := range hooks {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if c, ok := hm["command"].(string); ok {
				got = append(got, c)
			}
		}
	}
	return got
}

func countAgentlogHooks(cmds []string) int {
	n := 0
	for _, c := range cmds {
		if strings.Contains(c, "agentlog hook") {
			n++
		}
	}
	return n
}

func TestInstallHookRequiresATarget(t *testing.T) {
	_, env := sandbox(t)
	code, _, stderr := runIn(t, env, "install-hook")
	if code == 0 {
		t.Error("a file-mutating command must not guess its target; want nonzero exit")
	}
	if !strings.Contains(stderr, "--global") {
		t.Errorf("error should name the options, got %q", stderr)
	}
}

func TestInstallHookCreatesMissingFile(t *testing.T) {
	dir, env := sandbox(t)
	path := filepath.Join(dir, "nested", "settings.json") // dir does not exist either
	code, stdout, _ := runIn(t, env, "install-hook", "--settings", path)
	if code != 0 {
		t.Fatalf("want exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "installed") {
		t.Errorf("want an installed report, got %q", stdout)
	}
	cmds := commandsIn(t, readJSON(t, path))
	if len(cmds) != 1 || !strings.Contains(cmds[0], "agentlog hook") {
		t.Fatalf("want one agentlog hook, got %v", cmds)
	}
	// Creating is not clobbering, so there is nothing to back up.
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("a created file must not produce a .bak")
	}
}

func TestInstallHookGlobalAndProjectCommands(t *testing.T) {
	t.Run("global names an absolute log dir", func(t *testing.T) {
		dir, env := sandbox(t)
		path := filepath.Join(dir, "settings.json")
		if code, _, _ := runIn(t, env, "install-hook", "--global", "--settings", path); code != 0 {
			t.Fatalf("want exit 0, got %d", code)
		}
		cmds := commandsIn(t, readJSON(t, path))
		if len(cmds) != 1 || cmds[0] != globalHookCommand {
			t.Fatalf("want %q, got %v", globalHookCommand, cmds)
		}
	})

	t.Run("project leans on the cwd default", func(t *testing.T) {
		dir, env := sandbox(t)
		path := filepath.Join(dir, "settings.json")
		if code, _, _ := runIn(t, env, "install-hook", "--project", "--settings", path); code != 0 {
			t.Fatalf("want exit 0, got %d", code)
		}
		cmds := commandsIn(t, readJSON(t, path))
		if len(cmds) != 1 || cmds[0] != projectHookCommand {
			t.Fatalf("want %q, got %v", projectHookCommand, cmds)
		}
		// The hazard this guards: an unset var in a path position.
		if strings.Contains(cmds[0], "AGENTLOG_DIR=$CLAUDE_PROJECT_DIR") {
			t.Errorf("a possibly-unset var must never sit in a path: %q", cmds[0])
		}
	})
}

func TestInstallHookMergesAndPreservesUnrelatedKeys(t *testing.T) {
	dir, env := sandbox(t)
	path := filepath.Join(dir, "settings.json")
	prior := `{
  "model": "opus",
  "env": {"FOO": "bar"},
  "permissions": {"allow": ["Bash(ls:*)"]}
}`
	if err := os.WriteFile(path, []byte(prior), 0o644); err != nil {
		t.Fatal(err)
	}

	if code, _, stderr := runIn(t, env, "install-hook", "--settings", path); code != 0 {
		t.Fatalf("want exit 0, got %d: %s", code, stderr)
	}

	got := readJSON(t, path)
	if got["model"] != "opus" {
		t.Errorf("unrelated key lost: model = %v", got["model"])
	}
	if e, ok := got["env"].(map[string]any); !ok || e["FOO"] != "bar" {
		t.Errorf("unrelated nested key lost: env = %v", got["env"])
	}
	if _, ok := got["permissions"]; !ok {
		t.Error("unrelated key lost: permissions")
	}
	if n := countAgentlogHooks(commandsIn(t, got)); n != 1 {
		t.Errorf("want exactly 1 agentlog hook, got %d", n)
	}
}

func TestInstallHookIsIdempotent(t *testing.T) {
	dir, env := sandbox(t)
	path := filepath.Join(dir, "settings.json")
	if code, _, _ := runIn(t, env, "install-hook", "--settings", path); code != 0 {
		t.Fatalf("first install failed: %d", code)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	code, stdout, _ := runIn(t, env, "install-hook", "--settings", path)
	if code != 0 {
		t.Fatalf("a second install must exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "already installed") {
		t.Errorf("want an already-installed report, got %q", stdout)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("a no-op install rewrote the file\nbefore: %s\nafter:  %s", before, after)
	}
	if n := countAgentlogHooks(commandsIn(t, readJSON(t, path))); n != 1 {
		t.Errorf("want no duplicate, got %d agentlog hooks", n)
	}
	// Nothing changed, so nothing should have been backed up.
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("a no-op install must not write a .bak")
	}
}

func TestInstallHookAppendsAlongsideExistingHook(t *testing.T) {
	dir, env := sandbox(t)
	path := filepath.Join(dir, "settings.json")
	prior := `{
  "hooks": {
    "PostToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "my-own-linter"}]}
    ]
  }
}`
	if err := os.WriteFile(path, []byte(prior), 0o644); err != nil {
		t.Fatal(err)
	}

	if code, _, stderr := runIn(t, env, "install-hook", "--settings", path); code != 0 {
		t.Fatalf("want exit 0, got %d: %s", code, stderr)
	}

	cmds := commandsIn(t, readJSON(t, path))
	var foundOther bool
	for _, c := range cmds {
		if c == "my-own-linter" {
			foundOther = true
		}
	}
	if !foundOther {
		t.Errorf("existing non-agentlog hook was destroyed: %v", cmds)
	}
	if n := countAgentlogHooks(cmds); n != 1 {
		t.Errorf("want 1 agentlog hook added alongside, got %d in %v", n, cmds)
	}
	if len(cmds) != 2 {
		t.Errorf("want 2 hooks total, got %v", cmds)
	}
}

func TestInstallHookRefusesMalformedSettings(t *testing.T) {
	dir, env := sandbox(t)
	path := filepath.Join(dir, "settings.json")
	prior := `{"hooks": {"PostToolUse": [ THIS IS NOT JSON`
	if err := os.WriteFile(path, []byte(prior), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runIn(t, env, "install-hook", "--settings", path)
	if code == 0 {
		t.Error("want nonzero exit on unparseable settings")
	}
	if !strings.Contains(stderr, "refusing") {
		t.Errorf("want an explicit refusal, got %q", stderr)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != prior {
		t.Errorf("a file we could not parse was modified anyway\nwant: %s\ngot:  %s", prior, after)
	}
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("a refused install must not leave a .bak")
	}
}

// Refusing to clobber applies to a file that parses but holds an unexpected
// shape, too — it never reaches the JSON check.
func TestInstallHookRefusesUnexpectedHooksShape(t *testing.T) {
	dir, env := sandbox(t)
	path := filepath.Join(dir, "settings.json")
	prior := `{"hooks": "not-an-object"}`
	if err := os.WriteFile(path, []byte(prior), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runIn(t, env, "install-hook", "--settings", path)
	if code == 0 {
		t.Error("want nonzero exit when hooks is not an object")
	}
	if !strings.Contains(stderr, "refusing") {
		t.Errorf("want an explicit refusal, got %q", stderr)
	}
	after, _ := os.ReadFile(path)
	if string(after) != prior {
		t.Errorf("file modified despite refusal: %s", after)
	}
}

func TestInstallHookBacksUpExactPriorBytes(t *testing.T) {
	dir, env := sandbox(t)
	path := filepath.Join(dir, "settings.json")
	// Deliberately idiosyncratic formatting: the backup must reproduce the file
	// byte for byte, not a re-encoding of it.
	prior := "{\n\t\"model\":   \"opus\"\n}\n"
	if err := os.WriteFile(path, []byte(prior), 0o644); err != nil {
		t.Fatal(err)
	}

	if code, _, stderr := runIn(t, env, "install-hook", "--settings", path); code != 0 {
		t.Fatalf("want exit 0, got %d: %s", code, stderr)
	}

	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("no backup written before modifying: %v", err)
	}
	if string(bak) != prior {
		t.Errorf("backup is not the exact prior content\nwant: %q\ngot:  %q", prior, bak)
	}
	// And the backup is a real undo: restoring it returns the original.
	if got := readJSON(t, path); got["model"] != "opus" {
		t.Errorf("merge lost data: %v", got)
	}
}

func TestInstallHookDryRunWritesNothing(t *testing.T) {
	dir, env := sandbox(t)
	path := filepath.Join(dir, "settings.json")
	prior := `{"model": "opus"}`
	if err := os.WriteFile(path, []byte(prior), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdout, _ := runIn(t, env, "install-hook", "--settings", path, "--dry-run")
	if code != 0 {
		t.Fatalf("want exit 0, got %d", code)
	}

	// It prints what it *would* write...
	var shown map[string]any
	if err := json.Unmarshal([]byte(stdout), &shown); err != nil {
		t.Fatalf("dry-run stdout is not the resulting JSON: %v\n%s", err, stdout)
	}
	if n := countAgentlogHooks(commandsIn(t, shown)); n != 1 {
		t.Errorf("dry-run output should show the stanza, got %s", stdout)
	}
	if shown["model"] != "opus" {
		t.Errorf("dry-run output should show the merge, got %s", stdout)
	}

	// ...and changes nothing.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != prior {
		t.Errorf("dry-run modified the file: %s", after)
	}
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("dry-run must not write a .bak")
	}
}
