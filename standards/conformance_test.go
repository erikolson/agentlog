// Package standards holds the Layer-2 conformance suite: the invariants a valid
// agentlog event must satisfy, checked against the golden examples in
// ../spec/examples. It imports no schema-validation library on purpose — the
// JSON Schema in ../spec is the language-agnostic contract (validate it in CI
// with any tool, e.g. `check-jsonschema`), while this suite proves the
// invariants that live above the schema, in pure stdlib Go.
package standards

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func load(t *testing.T, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "spec", "examples", name))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func has(m map[string]any, k string) bool {
	v, ok := m[k]
	if !ok {
		return false
	}
	s, ok := v.(string)
	return !ok || s != ""
}

// I1: every event carries the core fields.
func TestCoreFieldsPresent(t *testing.T) {
	for _, name := range []string{
		"observation.json", "verdict.json",
		"observation_subagent.json", "observation_spawn.json",
	} {
		e := load(t, name)
		for _, k := range []string{"run_id", "seq", "ts", "kind"} {
			if !has(e, k) {
				t.Errorf("%s: missing core field %q", name, k)
			}
		}
	}
}

// I1: a verdict carries all five verdict fields.
func TestVerdictRequiredFields(t *testing.T) {
	e := load(t, "verdict.json")
	for _, k := range []string{"gate", "verdict", "witness", "adjudicator", "enforce"} {
		if !has(e, k) {
			t.Errorf("verdict.json: missing required field %q", k)
		}
	}
}

// I1 (negative): the invalid example is invalid for the stated reason — a
// verdict without a witness. Guards against the fixture silently becoming valid.
func TestInvalidVerdictMissingWitness(t *testing.T) {
	e := load(t, "invalid_verdict_missing_witness.json")
	if e["kind"] != "verdict" {
		t.Fatalf("fixture is not a verdict: %v", e["kind"])
	}
	if has(e, "witness") {
		t.Fatal("fixture unexpectedly has a witness; it should demonstrate the missing-witness violation")
	}
}

// I3: proposer != ratifier. Relational invariant that no JSON Schema draft can
// express, so it lives here.
func TestAdjudicatorDiffersFromActor(t *testing.T) {
	e := load(t, "verdict.json")
	adj, hasAdj := e["adjudicator"].(string)
	act, hasAct := e["actor"].(string)
	if hasAdj && hasAct && adj == act {
		t.Errorf("proposer == ratifier (%q); a verdict must be adjudicated by someone other than the proposer", adj)
	}
}

// I4: agent_id present ⇒ actor is a subagent. agent_id names the instance,
// actor names the kind; a set instance under a top-level kind is incoherent.
// Relational, so it lives here rather than in the schema.
func TestAgentIdImpliesSubagentActor(t *testing.T) {
	e := load(t, "observation_subagent.json")
	if !has(e, "agent_id") {
		t.Fatal("fixture should carry an agent_id to exercise the invariant")
	}
	if e["actor"] != "subagent" {
		t.Errorf("agent_id present but actor is %v, want subagent", e["actor"])
	}
	if !has(e, "agent_type") {
		t.Error("a subagent event should also name its agent_type")
	}
}

// The tree lives in edges, not per-event ancestry: a spawn event records the
// parent→child link in attrs (never as an invented top-level field), which is
// the only thing a reader needs to reconstruct the hierarchy at any depth.
func TestSpawnEventCarriesEdgeInAttrs(t *testing.T) {
	e := load(t, "observation_spawn.json")
	attrs, ok := e["attrs"].(map[string]any)
	if !ok {
		t.Fatal("spawn fixture should carry attrs")
	}
	if id, _ := attrs["spawned_agent_id"].(string); id == "" {
		t.Errorf("spawn edge missing: attrs.spawned_agent_id = %v", attrs["spawned_agent_id"])
	}
	if _, leaked := e["spawned_agent_id"]; leaked {
		t.Error("spawned_agent_id leaked to the top level; the edge belongs in attrs")
	}
}
