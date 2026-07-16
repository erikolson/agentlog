package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeLog seeds today's log file — the one doctor will look at — with raw
// lines, so a test can include malformed ones on purpose.
func writeLog(t *testing.T, logDir string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := time.Now().UTC().Format("2006-01-02") + ".jsonl"
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(logDir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorReportsHealthySetup(t *testing.T) {
	dir, env := sandbox(t)
	path := filepath.Join(dir, "settings.json")
	logDir := filepath.Join(dir, "logs")

	if code, _, stderr := runIn(t, env, "install-hook", "--settings", path); code != 0 {
		t.Fatalf("setup install failed: %d %s", code, stderr)
	}
	writeLog(t, logDir,
		`{"run_id":"sess-1","seq":1,"ts":"2026-07-16T00:00:00Z","kind":"observation","summary":"a"}`,
		`{"run_id":"sess-1","seq":2,"ts":"2026-07-16T00:00:01Z","kind":"observation","summary":"b"}`,
		`{"run_id":"sess-2","seq":1,"ts":"2026-07-16T00:00:02Z","kind":"observation","summary":"c"}`,
	)

	code, stdout, _ := runIn(t, env, "doctor", "--settings", path)
	if code != 0 {
		t.Fatalf("doctor must exit 0 on a healthy setup, got %d\n%s", code, stdout)
	}
	for _, want := range []string{
		"agentlog " + version,
		"hook wired in",
		"is writable",
		"3 events across 2 runs",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("doctor output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "malformed") {
		t.Errorf("clean log reported as malformed:\n%s", stdout)
	}
}

func TestDoctorCountsMalformedLines(t *testing.T) {
	dir, env := sandbox(t)
	path := filepath.Join(dir, "settings.json")
	logDir := filepath.Join(dir, "logs")

	if code, _, _ := runIn(t, env, "install-hook", "--settings", path); code != 0 {
		t.Fatal("setup install failed")
	}
	writeLog(t, logDir,
		`{"run_id":"sess-1","seq":1,"ts":"2026-07-16T00:00:00Z","kind":"observation"}`,
		`this is not json`,
		`{"run_id":"sess-1","seq":2,"ts":"2026-07-16T00:00:01Z","kind":"observation"}`,
		`{"truncated": `,
	)

	code, stdout, _ := runIn(t, env, "doctor", "--settings", path)
	if code != 0 {
		t.Fatalf("malformed lines are a finding, not a failure; got exit %d", code)
	}
	if !strings.Contains(stdout, "2 events across 1 runs") {
		t.Errorf("want only the parseable lines counted as events:\n%s", stdout)
	}
	if !strings.Contains(stdout, "2 malformed lines") {
		t.Errorf("want 2 malformed lines reported:\n%s", stdout)
	}
}

// No hook is a diagnosis, not an error — doctor reports, it does not gate.
func TestDoctorExitsZeroWhenNothingIsWired(t *testing.T) {
	dir, env := sandbox(t)
	path := filepath.Join(dir, "settings.json")

	code, stdout, _ := runIn(t, env, "doctor", "--settings", path)
	if code != 0 {
		t.Errorf("a missing settings file is a warning, not a failure; got exit %d", code)
	}
	if !strings.Contains(stdout, "no settings file") {
		t.Errorf("want a missing-settings warning:\n%s", stdout)
	}

	if err := os.WriteFile(path, []byte(`{"model":"opus"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ = runIn(t, env, "doctor", "--settings", path)
	if code != 0 {
		t.Errorf("an unwired hook is a warning, not a failure; got exit %d", code)
	}
	if !strings.Contains(stdout, "no agentlog hook") {
		t.Errorf("want an unwired-hook warning:\n%s", stdout)
	}
}

// The one thing worth failing over: doctor cannot answer the question at all.
func TestDoctorFailsOnUnparseableSettings(t *testing.T) {
	dir, env := sandbox(t)
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{ NOT JSON`), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runIn(t, env, "doctor", "--settings", path)
	if code == 0 {
		t.Error("want nonzero exit when the settings file cannot be parsed")
	}
	if !strings.Contains(stderr, "not valid JSON") {
		t.Errorf("want a parse error, got %q", stderr)
	}
}

func TestDoctorReportsMissingLogDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	// Point AGENTLOG_DIR somewhere that does not exist yet.
	env := []string{"HOME=" + dir, "AGENTLOG_DIR=" + filepath.Join(dir, "never-created")}

	code, stdout, _ := runIn(t, env, "doctor", "--settings", path)
	if code != 0 {
		t.Errorf("a missing log dir is a warning, not a failure; got exit %d", code)
	}
	if !strings.Contains(stdout, "does not exist yet") {
		t.Errorf("want a missing-log-dir warning:\n%s", stdout)
	}
}

// doctor is read-only: pointing it at a pristine tree must leave it pristine.
func TestDoctorWritesNothing(t *testing.T) {
	dir, env := sandbox(t)
	path := filepath.Join(dir, "settings.json")
	logDir := filepath.Join(dir, "logs")
	if code, _, _ := runIn(t, env, "install-hook", "--settings", path); code != 0 {
		t.Fatal("setup install failed")
	}
	writeLog(t, logDir, `{"run_id":"sess-1","seq":1,"ts":"2026-07-16T00:00:00Z","kind":"observation"}`)

	before := snapshot(t, dir)
	if code, _, _ := runIn(t, env, "doctor", "--settings", path); code != 0 {
		t.Fatal("doctor failed")
	}
	after := snapshot(t, dir)

	if len(before) != len(after) {
		t.Errorf("doctor changed the file set\nbefore: %v\nafter:  %v", before, after)
	}
	for k, v := range before {
		if after[k] != v {
			t.Errorf("doctor modified %s", k)
		}
	}
}

// snapshot maps every file under root to its contents.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[p] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
