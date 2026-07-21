package agentlog

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func keysOf(m map[string]any) []string {
	var k []string
	for key := range m {
		k = append(k, key)
	}
	sort.Strings(k)
	return k
}

// orderedKeys returns an object's keys in the order they appear on the wire.
// Field order is part of what the contract promises, and encoding/json emits
// struct fields in declaration order, so this pins the wire struct's layout
// rather than just its contents.
func orderedKeys(t *testing.T, line string) []string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(line))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		t.Fatalf("not a JSON object: %q", line)
	}
	var keys []string
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k.(string))
		var skip json.RawMessage // consumes exactly one value, nesting and all
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

func decode(t *testing.T, line string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("emitted invalid JSON %q: %v", line, err)
	}
	return m
}

func TestEmitObservationStampsCoreFields(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, "run-1")
	if err := l.EmitObservation(Observation{Stage: "tool_call", Summary: "ls"}); err != nil {
		t.Fatal(err)
	}
	if err := l.EmitObservation(Observation{Stage: "tool_call", Summary: "cat"}); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lines))
	}
	e := decode(t, lines[1])
	if e["seq"] != float64(2) {
		t.Errorf("want seq 2, got %v", e["seq"])
	}
	if e["run_id"] != "run-1" {
		t.Errorf("want run-1, got %v", e["run_id"])
	}
	if ts, _ := e["ts"].(string); ts == "" {
		t.Error("timestamp not set")
	}
	if e["kind"] != "observation" {
		t.Errorf("want kind observation, got %v", e["kind"])
	}
}

func TestEmitObservationRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, "run-1")
	err := l.EmitObservation(Observation{
		Stage: "tool_call", Actor: "agent", Summary: "Bash: go test ./...",
		DurMS: 12, Status: "error", Attrs: map[string]string{"repo": "auth"},
	})
	if err != nil {
		t.Fatal(err)
	}
	e := decode(t, strings.TrimSpace(buf.String()))
	for k, want := range map[string]any{
		"kind": "observation", "stage": "tool_call", "actor": "agent",
		"summary": "Bash: go test ./...", "dur_ms": float64(12), "status": "error",
	} {
		if e[k] != want {
			t.Errorf("%s: want %v, got %v", k, want, e[k])
		}
	}
	if a, _ := e["attrs"].(map[string]any); a["repo"] != "auth" {
		t.Errorf("attrs lost: %v", e["attrs"])
	}
}

func TestEmitVerdictRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, "run-2")
	err := l.EmitVerdict(Verdict{
		Gate: "tests", Verdict: "fail", Witness: "sha256:9f2c1a",
		Adjudicator: "ratchet-check", Enforce: "block", Summary: "3 failing in ./auth",
		Attrs: map[string]string{"pkg": "./auth"},
	})
	if err != nil {
		t.Fatal(err)
	}
	e := decode(t, strings.TrimSpace(buf.String()))
	for k, want := range map[string]any{
		"kind": "verdict", "gate": "tests", "verdict": "fail",
		"witness": "sha256:9f2c1a", "adjudicator": "ratchet-check",
		"enforce": "block", "summary": "3 failing in ./auth",
	} {
		if e[k] != want {
			t.Errorf("%s: want %v, got %v", k, want, e[k])
		}
	}
	if a, _ := e["attrs"].(map[string]any); a["pkg"] != "./auth" {
		t.Errorf("attrs lost: %v", e["attrs"])
	}
}

// A verdict names both the proposer and the ratifier, which is what keeps
// invariant I3 (actor != adjudicator) checkable over the stream rather than
// vacuous for everything this package writes.
func TestEmitVerdictCarriesActorDistinctFromAdjudicator(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, "sess-9")
	err := l.EmitVerdict(Verdict{
		Gate: "tests", Verdict: "fail", Witness: "sha256:9f2c1a",
		Actor: "agent", Adjudicator: "ratchet-check", Enforce: "block",
		Summary: "3 failing in ./auth",
	})
	if err != nil {
		t.Fatal(err)
	}
	e := decode(t, strings.TrimSpace(buf.String()))
	if e["actor"] != "agent" {
		t.Errorf("verdict lost actor: %v", e["actor"])
	}
	if e["adjudicator"] != "ratchet-check" {
		t.Errorf("verdict lost adjudicator: %v", e["adjudicator"])
	}
	// Two distinct fields, not one aliased to the other: I3 is only meaningful
	// if a reader can compare them.
	if e["actor"] == e["adjudicator"] {
		t.Errorf("actor and adjudicator collapsed into one value: %v", e["actor"])
	}
}

// The reference implementation must be able to produce the contract's own
// golden verdict. Key order is not part of JSON object identity (and our wire
// order is pinned to v0.1.1 elsewhere), so this compares the key/value set.
func TestEmitVerdictReproducesGoldenExample(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("spec", "examples", "verdict.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := decode(t, string(golden))

	var buf bytes.Buffer
	l := New(&buf, "sess-9")
	err = l.EmitVerdict(Verdict{
		Gate: "tests", Verdict: "fail", Witness: "sha256:9f2c1a",
		Actor: "agent", Adjudicator: "ratchet-check", Enforce: "block",
		Summary: "3 failing in ./auth",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := decode(t, strings.TrimSpace(buf.String()))

	// seq and ts are the logger's to stamp and differ by construction.
	for _, volatile := range []string{"seq", "ts"} {
		delete(got, volatile)
		delete(want, volatile)
	}
	if len(got) != len(want) {
		t.Fatalf("key set differs\n got: %v\nwant: %v", keysOf(got), keysOf(want))
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: want %v, got %v", k, w, got[k])
		}
	}
}

// These cases pin the exact key set and order the wire emits, so a future edit
// to the wire struct that renames, reorders or un-omitempties a field fails
// here rather than silently breaking every consumer of the published contract.
// The agent-identity fields (contract v2) are omitempty, so any event that
// names no subagent stays byte-identical to the v1 lines pinned below.
func TestWireFormatIsUnchanged(t *testing.T) {
	t.Run("observation", func(t *testing.T) {
		var buf bytes.Buffer
		l := New(&buf, "sess-9")
		err := l.EmitObservation(Observation{
			Stage: "tool_call", Actor: "agent", Summary: "Bash: go test ./...",
			DurMS: 12, Status: "error", Attrs: map[string]string{"repo": "auth"},
		})
		if err != nil {
			t.Fatal(err)
		}
		line := strings.TrimSpace(buf.String())
		want := []string{"run_id", "seq", "ts", "kind", "stage", "actor", "summary", "dur_ms", "status", "attrs"}
		if got := orderedKeys(t, line); !equal(got, want) {
			t.Errorf("wire keys drifted\n got: %v\nwant: %v", got, want)
		}
	})

	t.Run("verdict", func(t *testing.T) {
		var buf bytes.Buffer
		l := New(&buf, "sess-9")
		err := l.EmitVerdict(Verdict{
			Gate: "tests", Verdict: "fail", Witness: "sha256:9f2c1a",
			Adjudicator: "ratchet-check", Enforce: "block", Summary: "3 failing in ./auth",
		})
		if err != nil {
			t.Fatal(err)
		}
		line := strings.TrimSpace(buf.String())
		// summary sits before gate because the wire struct declares the
		// observation block first — exactly as v0.1.1 emitted it.
		want := []string{"run_id", "seq", "ts", "kind", "summary", "gate", "verdict", "witness", "adjudicator", "enforce"}
		if got := orderedKeys(t, line); !equal(got, want) {
			t.Errorf("wire keys drifted\n got: %v\nwant: %v", got, want)
		}
	})

	t.Run("verdict with actor", func(t *testing.T) {
		var buf bytes.Buffer
		l := New(&buf, "sess-9")
		err := l.EmitVerdict(Verdict{
			Gate: "tests", Verdict: "fail", Witness: "sha256:9f2c1a",
			Actor: "agent", Adjudicator: "ratchet-check", Enforce: "block",
			Summary: "3 failing in ./auth",
		})
		if err != nil {
			t.Fatal(err)
		}
		line := strings.TrimSpace(buf.String())
		// actor rides in the wire struct's observation slot, which is where
		// v0.1.1's flat Event put it for a verdict too.
		want := []string{"run_id", "seq", "ts", "kind", "actor", "summary", "gate", "verdict", "witness", "adjudicator", "enforce"}
		if got := orderedKeys(t, line); !equal(got, want) {
			t.Errorf("wire keys drifted\n got: %v\nwant: %v", got, want)
		}
	})

	t.Run("observation with agent identity", func(t *testing.T) {
		var buf bytes.Buffer
		l := New(&buf, "sess-9")
		err := l.EmitObservation(Observation{
			Stage: "tool_call", Actor: "subagent", AgentID: "ad7001", AgentType: "Explore",
			Summary: "Bash: rg -n secret", Status: "ok",
		})
		if err != nil {
			t.Fatal(err)
		}
		line := strings.TrimSpace(buf.String())
		// agent_id and agent_type sit immediately after actor, before summary.
		want := []string{"run_id", "seq", "ts", "kind", "stage", "actor", "agent_id", "agent_type", "summary", "status"}
		if got := orderedKeys(t, line); !equal(got, want) {
			t.Errorf("wire keys drifted\n got: %v\nwant: %v", got, want)
		}
	})
}

// An observation must not carry empty verdict keys, and vice versa. The types
// make the fields unsettable; this guards the wire struct's omitempty, which is
// the other half of the promise.
func TestEmptyFieldsStayOffTheWire(t *testing.T) {
	t.Run("observation carries no verdict keys", func(t *testing.T) {
		var buf bytes.Buffer
		l := New(&buf, "run-1")
		if err := l.EmitObservation(Observation{Summary: "x"}); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"witness", "adjudicator", "gate", "enforce"} {
			if strings.Contains(buf.String(), k) {
				t.Errorf("observation leaked %q: %s", k, buf.String())
			}
		}
	})

	t.Run("verdict carries no observation keys", func(t *testing.T) {
		var buf bytes.Buffer
		l := New(&buf, "run-1")
		if err := l.EmitVerdict(Verdict{Gate: "tests", Verdict: "pass"}); err != nil {
			t.Fatal(err)
		}
		// actor is absent from this list on purpose: I2 never forbade a verdict
		// an actor, and a verdict names one to keep I3 checkable.
		for _, k := range []string{"stage", "status", "dur_ms"} {
			if strings.Contains(buf.String(), k) {
				t.Errorf("verdict leaked %q: %s", k, buf.String())
			}
		}
	})

	t.Run("empty attrs stays off the wire", func(t *testing.T) {
		var buf bytes.Buffer
		l := New(&buf, "run-1")
		if err := l.EmitObservation(Observation{Summary: "x"}); err != nil {
			t.Fatal(err)
		}
		if _, ok := decode(t, strings.TrimSpace(buf.String()))["attrs"]; ok {
			t.Errorf("empty attrs must not reach the wire: %s", buf.String())
		}
	})
}

func TestProjectHookPayload(t *testing.T) {
	raw := []byte(`{
		"session_id": "sess-9",
		"tool_name": "Bash",
		"tool_input": {"command": "go test ./..."},
		"tool_response": {"is_error": true}
	}`)
	p := ProjectHookPayload(raw)
	o := p.Observation

	if p.RunID != "sess-9" {
		t.Errorf("want session id as run, got %q", p.RunID)
	}
	if o.Stage != "tool_call" {
		t.Errorf("want tool_call, got %q", o.Stage)
	}
	if o.Actor != "agent" {
		t.Errorf("want agent, got %q", o.Actor)
	}
	if !strings.Contains(o.Summary, "go test") {
		t.Errorf("summary missing command: %q", o.Summary)
	}
	if o.Status != "error" {
		t.Errorf("want error status, got %q", o.Status)
	}
	// A main-loop payload names no subagent, so identity stays empty and the
	// actor is the top-level kind.
	if o.AgentID != "" || o.AgentType != "" {
		t.Errorf("main-loop call should carry no agent identity, got id=%q type=%q", o.AgentID, o.AgentType)
	}
}

// A tool call inside a subagent carries agent_id/agent_type, which the
// projection reflects in the observation and turns into a subagent actor.
func TestProjectHookPayloadSubagent(t *testing.T) {
	raw := []byte(`{
		"session_id": "sess-9",
		"tool_name": "Bash",
		"tool_input": {"command": "rg -n secret"},
		"tool_response": {"is_error": false},
		"agent_id": "ad7001",
		"agent_type": "Explore"
	}`)
	o := ProjectHookPayload(raw).Observation
	if o.Actor != "subagent" {
		t.Errorf("want subagent actor, got %q", o.Actor)
	}
	if o.AgentID != "ad7001" {
		t.Errorf("want agent id ad7001, got %q", o.AgentID)
	}
	if o.AgentType != "Explore" {
		t.Errorf("want agent type Explore, got %q", o.AgentType)
	}
}

// A spawn tool call records the parent→child edge and per-subagent telemetry in
// attrs, so a reader can rebuild the tree without the recorder holding state.
func TestProjectHookPayloadSpawnEdge(t *testing.T) {
	raw := []byte(`{
		"session_id": "sess-9",
		"tool_name": "Agent",
		"tool_input": {"description": "explore auth"},
		"tool_response": {"agentId": "ad7001", "totalTokens": 55823, "totalToolUseCount": 10}
	}`)
	o := ProjectHookPayload(raw).Observation
	if o.Actor != "agent" {
		t.Errorf("spawn happens in the parent loop; want agent actor, got %q", o.Actor)
	}
	if o.Attrs["spawned_agent_id"] != "ad7001" {
		t.Errorf("want spawn edge to child ad7001, got %q", o.Attrs["spawned_agent_id"])
	}
	if o.Attrs["spawned_tokens"] != "55823" {
		t.Errorf("want per-subagent token telemetry, got %q", o.Attrs["spawned_tokens"])
	}
	if o.Attrs["spawned_tool_uses"] != "10" {
		t.Errorf("want per-subagent tool-use telemetry, got %q", o.Attrs["spawned_tool_uses"])
	}
}

// An ordinary (non-spawn) tool call must not manufacture an empty attrs map:
// the projection returns nil so nothing reaches the wire.
func TestProjectHookPayloadNoSpawnNoAttrs(t *testing.T) {
	raw := []byte(`{"tool_name":"Bash","tool_input":{"command":"ls"},"tool_response":{}}`)
	if a := ProjectHookPayload(raw).Observation.Attrs; a != nil {
		t.Errorf("non-spawn call should carry no attrs, got %v", a)
	}
}

func TestProjectHookPayloadKeepsStatusInEnum(t *testing.T) {
	// A harness status the contract's enum does not name must not reach the log.
	raw := []byte(`{"tool_name":"Bash","tool_response":{"status":"completed"}}`)
	if p := ProjectHookPayload(raw); p.Observation.Status != "ok" {
		t.Errorf("want out-of-enum status degraded to ok, got %q", p.Observation.Status)
	}
	// One the enum does name still passes through.
	raw = []byte(`{"tool_name":"Bash","tool_response":{"status":"timeout"}}`)
	if p := ProjectHookPayload(raw); p.Observation.Status != "timeout" {
		t.Errorf("want timeout preserved, got %q", p.Observation.Status)
	}
}

func TestProjectHookPayloadToleratesGarbage(t *testing.T) {
	p := ProjectHookPayload([]byte("not json at all"))
	if p.Observation.Status != "ok" {
		t.Errorf("want ok default on unstructured input, got %q", p.Observation.Status)
	}
	if p.RunID != "" {
		t.Errorf("want no run id from garbage, got %q", p.RunID)
	}
	if p.Observation.Stage != "tool_call" {
		t.Errorf("want tool_call even on garbage, got %q", p.Observation.Stage)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
