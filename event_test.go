package agentlog

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEmitWritesJSONLWithSeq(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, "run-1")
	if err := l.Emit(Event{Kind: KindObservation, Stage: "tool_call", Summary: "ls"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Emit(Event{Kind: KindObservation, Stage: "tool_call", Summary: "cat"}); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lines))
	}

	var e Event
	if err := json.Unmarshal([]byte(lines[1]), &e); err != nil {
		t.Fatal(err)
	}
	if e.Seq != 2 {
		t.Errorf("want seq 2, got %d", e.Seq)
	}
	if e.RunID != "run-1" {
		t.Errorf("want run-1, got %q", e.RunID)
	}
	if e.TS.IsZero() {
		t.Error("timestamp not set")
	}
}

func TestEmitOmitsEmptyVerdictFields(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, "run-2")
	if err := l.Emit(Event{Kind: KindObservation, Summary: "x"}); err != nil {
		t.Fatal(err)
	}
	// An observation should not carry empty verdict keys in its JSON.
	if strings.Contains(buf.String(), "witness") || strings.Contains(buf.String(), "adjudicator") {
		t.Errorf("observation leaked verdict fields: %s", buf.String())
	}
}

func TestProjectHookPayload(t *testing.T) {
	raw := []byte(`{
		"session_id": "sess-9",
		"tool_name": "Bash",
		"tool_input": {"command": "go test ./..."},
		"tool_response": {"is_error": true}
	}`)
	e := ProjectHookPayload(raw)

	if e.Kind != KindObservation {
		t.Errorf("want observation, got %q", e.Kind)
	}
	if e.RunID != "sess-9" {
		t.Errorf("want session id as run, got %q", e.RunID)
	}
	if !strings.Contains(e.Summary, "go test") {
		t.Errorf("summary missing command: %q", e.Summary)
	}
	if e.Status != "error" {
		t.Errorf("want error status, got %q", e.Status)
	}
}

func TestProjectHookPayloadKeepsStatusInEnum(t *testing.T) {
	// A harness status the contract's enum does not name must not reach the log.
	raw := []byte(`{"tool_name":"Bash","tool_response":{"status":"completed"}}`)
	if e := ProjectHookPayload(raw); e.Status != "ok" {
		t.Errorf("want out-of-enum status degraded to ok, got %q", e.Status)
	}
	// One the enum does name still passes through.
	raw = []byte(`{"tool_name":"Bash","tool_response":{"status":"timeout"}}`)
	if e := ProjectHookPayload(raw); e.Status != "timeout" {
		t.Errorf("want timeout preserved, got %q", e.Status)
	}
}

func TestProjectHookPayloadToleratesGarbage(t *testing.T) {
	e := ProjectHookPayload([]byte("not json at all"))
	if e.Kind != KindObservation {
		t.Errorf("want observation even on garbage, got %q", e.Kind)
	}
	if e.Status != "ok" {
		t.Errorf("want ok default on unstructured input, got %q", e.Status)
	}
}
