package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seed writes raw lines into a named day file, bypassing the logger so a test
// can control ts, seq and file placement exactly.
func seed(t *testing.T, logDir, day string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(logDir, day+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func showLines(t *testing.T, env []string, args ...string) []string {
	t.Helper()
	code, stdout, stderr := runIn(t, env, args...)
	if code != 0 {
		t.Fatalf("show exited %d: %s", code, stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		return nil
	}
	return strings.Split(strings.TrimSpace(stdout), "\n")
}

// The footgun that motivated the command: a substring match on sess-9 also
// finds sess-90, silently.
func TestShowMatchesRunIDExactly(t *testing.T) {
	dir, env := sandbox(t)
	logDir := filepath.Join(dir, "logs")
	seed(t, logDir, "2026-07-15",
		`{"run_id":"sess-9","seq":1,"ts":"2026-07-15T10:00:00Z","kind":"observation","summary":"mine"}`,
		`{"run_id":"sess-90","seq":1,"ts":"2026-07-15T10:00:01Z","kind":"observation","summary":"NOT mine"}`,
		`{"run_id":"sess-9x","seq":1,"ts":"2026-07-15T10:00:02Z","kind":"observation","summary":"NOT mine"}`,
		`{"run_id":"sess-9","seq":1,"ts":"2026-07-15T10:00:03Z","kind":"observation","summary":"mine too"}`,
	)

	got := showLines(t, env, "show", "--run", "sess-9")
	if len(got) != 2 {
		t.Fatalf("want exactly the 2 sess-9 lines, got %d:\n%s", len(got), strings.Join(got, "\n"))
	}
	for _, l := range got {
		if strings.Contains(l, "NOT mine") {
			t.Errorf("selected another run's event: %s", l)
		}
	}
}

// seq is per-process, so it cannot order anything; file order is not a
// guarantee either. ts decides.
func TestShowSortsByTimestampNotSeqOrFileOrder(t *testing.T) {
	dir, env := sandbox(t)
	logDir := filepath.Join(dir, "logs")
	// Written out of order, and every seq is 1 — exactly what a day of hook
	// invocations looks like.
	seed(t, logDir, "2026-07-15",
		`{"run_id":"r","seq":1,"ts":"2026-07-15T10:00:02Z","kind":"observation","summary":"third"}`,
		`{"run_id":"r","seq":1,"ts":"2026-07-15T10:00:00Z","kind":"observation","summary":"first"}`,
		`{"run_id":"r","seq":1,"ts":"2026-07-15T10:00:01Z","kind":"observation","summary":"second"}`,
	)

	got := showLines(t, env, "show", "--run", "r")
	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("want %d lines, got %d", len(want), len(got))
	}
	for i, w := range want {
		if !strings.Contains(got[i], `"summary":"`+w+`"`) {
			t.Errorf("position %d: want %q, got %s", i, w, got[i])
		}
	}
}

// A whole-second stamp serializes without a fraction ("…:52Z"), which sorts
// after "…:52.1Z" lexically. Parsing the timestamp is what keeps that right.
func TestShowSortsWholeSecondStampsCorrectly(t *testing.T) {
	dir, env := sandbox(t)
	logDir := filepath.Join(dir, "logs")
	seed(t, logDir, "2026-07-15",
		`{"run_id":"r","seq":1,"ts":"2026-07-15T10:00:52.1Z","kind":"observation","summary":"later"}`,
		`{"run_id":"r","seq":1,"ts":"2026-07-15T10:00:52Z","kind":"observation","summary":"earlier"}`,
	)

	got := showLines(t, env, "show", "--run", "r")
	if len(got) != 2 {
		t.Fatalf("want 2 lines, got %d", len(got))
	}
	if !strings.Contains(got[0], "earlier") {
		t.Errorf("a whole-second stamp sorted wrong; got first: %s", got[0])
	}
}

// A run that crosses midnight UTC lives in two files.
func TestShowReadsEveryFile(t *testing.T) {
	dir, env := sandbox(t)
	logDir := filepath.Join(dir, "logs")
	seed(t, logDir, "2026-07-15",
		`{"run_id":"r","seq":1,"ts":"2026-07-15T23:59:59Z","kind":"observation","summary":"before midnight"}`)
	seed(t, logDir, "2026-07-16",
		`{"run_id":"r","seq":1,"ts":"2026-07-16T00:00:01Z","kind":"observation","summary":"after midnight"}`)

	got := showLines(t, env, "show", "--run", "r")
	if len(got) != 2 {
		t.Fatalf("want both sides of midnight, got %d:\n%s", len(got), strings.Join(got, "\n"))
	}
	if !strings.Contains(got[0], "before midnight") || !strings.Contains(got[1], "after midnight") {
		t.Errorf("wrong order across files:\n%s", strings.Join(got, "\n"))
	}
}

// show selects; it does not present. Lines must come back byte for byte, or it
// has started having opinions about formatting.
func TestShowPrintsRawLinesUnchanged(t *testing.T) {
	dir, env := sandbox(t)
	logDir := filepath.Join(dir, "logs")
	original := `{"run_id":"r","seq":1,"ts":"2026-07-15T10:00:00Z","kind":"verdict","actor":"agent","summary":"3 failing","gate":"tests","verdict":"fail","witness":"sha256:9f2c1a","adjudicator":"ratchet-check","enforce":"block","attrs":{"pkg":"./auth"}}`
	seed(t, logDir, "2026-07-15", original)

	got := showLines(t, env, "show", "--run", "r")
	if len(got) != 1 {
		t.Fatalf("want 1 line, got %d", len(got))
	}
	if got[0] != original {
		t.Errorf("show reformatted the line\nwant: %s\ngot:  %s", original, got[0])
	}
}

// A torn line is doctor's finding to report; show just does not select it.
func TestShowSkipsMalformedLines(t *testing.T) {
	dir, env := sandbox(t)
	logDir := filepath.Join(dir, "logs")
	seed(t, logDir, "2026-07-15",
		`{"run_id":"r","seq":1,"ts":"2026-07-15T10:00:00Z","kind":"observation","summary":"good"}`,
		`this is not json`,
		`{"truncated": `,
		`{"run_id":"r","seq":1,"ts":"2026-07-15T10:00:01Z","kind":"observation","summary":"also good"}`,
	)

	got := showLines(t, env, "show", "--run", "r")
	if len(got) != 2 {
		t.Fatalf("want the 2 parseable lines, got %d:\n%s", len(got), strings.Join(got, "\n"))
	}
}

func TestShowRequiresRun(t *testing.T) {
	_, env := sandbox(t)
	code, _, stderr := runIn(t, env, "show")
	if code == 0 {
		t.Error("want nonzero exit without --run")
	}
	if !strings.Contains(stderr, "--run is required") {
		t.Errorf("want a clear message, got %q", stderr)
	}
}

// No such run is a finding, not a failure: empty stdout stays pipeable, the
// note goes to stderr, exit stays 0.
func TestShowReportsNoMatchesWithoutFailing(t *testing.T) {
	dir, env := sandbox(t)
	seed(t, filepath.Join(dir, "logs"), "2026-07-15",
		`{"run_id":"r","seq":1,"ts":"2026-07-15T10:00:00Z","kind":"observation"}`)

	code, stdout, stderr := runIn(t, env, "show", "--run", "nope")
	if code != 0 {
		t.Errorf("want exit 0 for an absent run, got %d", code)
	}
	if stdout != "" {
		t.Errorf("want empty stdout, got %q", stdout)
	}
	if !strings.Contains(stderr, "no events for run") {
		t.Errorf("want a stderr note, got %q", stderr)
	}
}

// A missing log dir is the same finding, not a crash.
func TestShowHandlesMissingLogDir(t *testing.T) {
	dir := t.TempDir()
	env := []string{"HOME=" + dir, "AGENTLOG_DIR=" + filepath.Join(dir, "never-created")}
	code, stdout, stderr := runIn(t, env, "show", "--run", "r")
	if code != 0 {
		t.Errorf("want exit 0, got %d: %s", code, stderr)
	}
	if stdout != "" {
		t.Errorf("want empty stdout, got %q", stdout)
	}
}

// show reads, so it must leave the log exactly as it found it.
func TestShowWritesNothing(t *testing.T) {
	dir, env := sandbox(t)
	logDir := filepath.Join(dir, "logs")
	seed(t, logDir, "2026-07-15",
		`{"run_id":"r","seq":1,"ts":"2026-07-15T10:00:00Z","kind":"observation"}`)

	before := snapshot(t, dir)
	showLines(t, env, "show", "--run", "r")
	after := snapshot(t, dir)

	if len(before) != len(after) {
		t.Errorf("show changed the file set\nbefore: %v\nafter:  %v", before, after)
	}
	for k, v := range before {
		if after[k] != v {
			t.Errorf("show modified %s", k)
		}
	}
}
