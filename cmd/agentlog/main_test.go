package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// bin is the compiled binary under test. Exit codes and the never-write-stdout
// rule are part of the hook's contract with the harness, and neither survives a
// call into main(), so these tests drive the real process.
var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "agentlog-test")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "agentlog")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		panic("building agentlog: " + err.Error() + "\n" + string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// run invokes the binary against a throwaway log dir, returning its exit code,
// whatever it put on stdout, and every event that actually landed on disk.
func run(t *testing.T, stdin string, args ...string) (code int, stdout string, events []map[string]any) {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "AGENTLOG_DIR="+dir)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running %v: %v", args, err)
		}
		code = ee.ExitCode()
	}

	// Glob rather than rebuild the dated name: a run straddling UTC midnight
	// would otherwise look like a lost event.
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if line == "" {
				continue
			}
			var e map[string]any
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Fatalf("emitted invalid JSONL %q: %v", line, err)
			}
			events = append(events, e)
		}
	}
	return code, out.String(), events
}

func attrsOf(t *testing.T, e map[string]any) map[string]any {
	t.Helper()
	a, ok := e["attrs"].(map[string]any)
	if !ok {
		t.Fatalf("event has no attrs object: %v", e)
	}
	return a
}

func TestEmitRoundTripsAttrs(t *testing.T) {
	code, _, events := run(t, "", "emit", "--stage", "tool_call", "--summary", "x",
		"--status", "ok", "--attr", "repo=auth", "--attr", "severity=high")
	if code != 0 {
		t.Fatalf("want exit 0, got %d", code)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	a := attrsOf(t, events[0])
	if a["repo"] != "auth" {
		t.Errorf("want repo=auth, got %v", a["repo"])
	}
	if a["severity"] != "high" {
		t.Errorf("want severity=high, got %v", a["severity"])
	}
}

func TestHookRoundTripsAttrsAndKeepsProjection(t *testing.T) {
	payload := `{"session_id":"sess-9","tool_name":"Bash",
		"tool_input":{"command":"go test ./..."},"tool_response":{"is_error":true}}`
	code, stdout, events := run(t, payload, "hook", "--attr", "session=demo")
	if code != 0 {
		t.Fatalf("hook must exit 0, got %d", code)
	}
	if stdout != "" {
		t.Errorf("hook wrote to stdout: %q", stdout)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	e := events[0]

	// Annotations must layer on without disturbing anything projected.
	if e["run_id"] != "sess-9" {
		t.Errorf("projection lost run_id: %v", e["run_id"])
	}
	if e["stage"] != "tool_call" {
		t.Errorf("projection lost stage: %v", e["stage"])
	}
	if e["actor"] != "agent" {
		t.Errorf("projection lost actor: %v", e["actor"])
	}
	if e["status"] != "error" {
		t.Errorf("projection lost status: %v", e["status"])
	}
	if s, _ := e["summary"].(string); !strings.Contains(s, "go test") {
		t.Errorf("projection lost summary: %v", e["summary"])
	}
	if a := attrsOf(t, e); a["session"] != "demo" {
		t.Errorf("want session=demo, got %v", a["session"])
	}
}

// Guards the omitempty that makes this change non-breaking: an unannotated
// event must serialize byte-identical to one from before attrs existed.
func TestEmptyAttrsStaysOffTheWire(t *testing.T) {
	code, _, events := run(t, "", "emit", "--stage", "tool_call", "--summary", "x", "--status", "ok")
	if code != 0 {
		t.Fatalf("want exit 0, got %d", code)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	if _, ok := events[0]["attrs"]; ok {
		t.Errorf("empty attrs must not reach the wire: %v", events[0])
	}
}

// attrs is a separate namespace: a key naming a top-level field is dropped, and
// the field it names keeps its real value.
func TestAttrCannotOverwriteCoreOrProjectedField(t *testing.T) {
	code, _, events := run(t, "", "emit", "--stage", "tool_call", "--summary", "x",
		"--status", "ok", "--attr", "status=nonsense", "--attr", "run_id=hijack",
		"--attr", "kind=bogus", "--attr", "repo=auth")
	if code != 0 {
		t.Fatalf("a reserved --attr must not be fatal, got exit %d", code)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	e := events[0]
	if e["status"] != "ok" {
		t.Errorf("core status polluted: %v", e["status"])
	}
	if e["run_id"] == "hijack" {
		t.Errorf("core run_id polluted: %v", e["run_id"])
	}
	if e["kind"] != "observation" {
		t.Errorf("core kind polluted: %v", e["kind"])
	}
	a := attrsOf(t, e)
	for _, k := range []string{"status", "run_id", "kind"} {
		if _, ok := a[k]; ok {
			t.Errorf("reserved key %q leaked into attrs: %v", k, a)
		}
	}
	if a["repo"] != "auth" {
		t.Errorf("legitimate attr dropped alongside reserved ones: %v", a)
	}
}

// The same parser, deliberately opposite postures.
func TestMalformedAttrPosture(t *testing.T) {
	t.Run("emit fails loud and writes nothing", func(t *testing.T) {
		code, _, events := run(t, "", "emit", "--stage", "tool_call", "--summary", "x",
			"--status", "ok", "--attr", "noequals")
		if code == 0 {
			t.Error("emit must exit nonzero on a malformed --attr")
		}
		if len(events) != 0 {
			t.Errorf("emit must write nothing when --attr is malformed, got %v", events)
		}
	})

	t.Run("hook never breaks the session", func(t *testing.T) {
		payload := `{"session_id":"sess-9","tool_name":"Bash","tool_input":{"command":"ls"}}`
		code, stdout, events := run(t, payload, "hook", "--attr", "noequals", "--attr", "repo=auth")
		if code != 0 {
			t.Errorf("hook must exit 0 despite a malformed --attr, got %d", code)
		}
		if stdout != "" {
			t.Errorf("hook wrote to stdout: %q", stdout)
		}
		if len(events) != 1 {
			t.Fatalf("hook must still record the event, got %d", len(events))
		}
		if e := events[0]; e["run_id"] != "sess-9" {
			t.Errorf("projection lost run_id: %v", e["run_id"])
		}
		// The malformed one is skipped; the good one still lands.
		if a := attrsOf(t, events[0]); a["repo"] != "auth" {
			t.Errorf("want the well-formed attr kept, got %v", a)
		}
	})
}

func TestParseAttrs(t *testing.T) {
	t.Run("value may contain =", func(t *testing.T) {
		attrs, _, errs := parseAttrs([]string{"filter=a=b=c"})
		if len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if attrs["filter"] != "a=b=c" {
			t.Errorf("split on more than the first =: %q", attrs["filter"])
		}
	})

	t.Run("last duplicate wins", func(t *testing.T) {
		attrs, _, errs := parseAttrs([]string{"k=first", "k=second"})
		if len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if attrs["k"] != "second" {
			t.Errorf("want second, got %q", attrs["k"])
		}
	})

	t.Run("no attrs yields a nil map", func(t *testing.T) {
		attrs, _, errs := parseAttrs(nil)
		if attrs != nil || len(errs) != 0 {
			t.Errorf("want nil map and no errors, got %v / %v", attrs, errs)
		}
	})

	t.Run("empty key is malformed", func(t *testing.T) {
		if _, _, errs := parseAttrs([]string{"=value"}); len(errs) != 1 {
			t.Errorf("want 1 error for an empty key, got %v", errs)
		}
	})
}
