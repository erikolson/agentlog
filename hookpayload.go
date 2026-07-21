package agentlog

import (
	"encoding/json"
	"strconv"
)

// hookPayload is the subset of a coding-agent PostToolUse payload we read.
// Field names follow the Claude Code convention; other harnesses (Gemini CLI,
// Codex CLI) share the shape closely enough that this projection degrades
// gracefully rather than breaking on them.
//
// AgentID and AgentType are present only when the hook fires inside a subagent
// (the harness omits them in the main loop). Their absence is exactly how we
// tell a top-level tool call from a nested one.
type hookPayload struct {
	SessionID    string          `json:"session_id"`
	ToolName     string          `json:"tool_name"`
	ToolInput    json.RawMessage `json:"tool_input"`
	ToolResponse json.RawMessage `json:"tool_response"`
	AgentID      string          `json:"agent_id"`
	AgentType    string          `json:"agent_type"`
}

// Projection is what a hook payload projects to: the Observation to record, and
// the run id the harness supplied to correlate it.
//
// RunID sits beside the Observation rather than inside it because run_id is the
// logger's to stamp, from Open or New — an Observation never carries one. It is
// empty when the payload named no session, and the caller decides what to put in
// its place.
type Projection struct {
	Observation Observation
	RunID       string
}

// ProjectHookPayload turns a raw PostToolUse payload into a Projection. A hook
// observes, so an observation is the only thing it can ever produce.
//
// It extracts fields *mechanically* — no interpretation, no model call — which
// is what keeps the logger pure procedural code. Structured input in, known
// fields out. It is best-effort and never fails on shape: missing or malformed
// fields become sane defaults so a bad payload degrades the summary rather than
// crashing the session.
func ProjectHookPayload(raw []byte) Projection {
	var p hookPayload
	_ = json.Unmarshal(raw, &p) // tolerate anything; zero values are fine

	o := Observation{
		Stage:     "tool_call",
		Actor:     actorFrom(p.AgentID),
		AgentID:   p.AgentID,
		AgentType: p.AgentType,
		Summary:   summaryFrom(p.ToolName, p.ToolInput),
		Status:    statusFrom(p.ToolResponse),
	}
	// A spawn is the one event that carries a parent→child edge: this line runs
	// in the spawner's context (its own AgentID is on the event), and names the
	// child in its response. Recording the child here is what lets a reader
	// rebuild the whole tree — at any depth — without the recorder ever holding
	// state across calls. The telemetry rides along because a spawn response is
	// the only place per-subagent cost is reported.
	if edge := spawnEdgeFrom(p.ToolName, p.ToolResponse); edge != nil {
		o.Attrs = edge
	}

	return Projection{
		Observation: o,
		RunID:       p.SessionID, // the harness-supplied id is what correlates a run
	}
}

// actorFrom names the kind of proposer from whether the harness reported a
// subagent id. Empty id means the top-level loop; a set id means a nested one.
// The instance itself travels in AgentID beside this.
func actorFrom(agentID string) string {
	if agentID != "" {
		return "subagent"
	}
	return "agent"
}

// spawnEdgeFrom records the parent→child edge and per-subagent telemetry a
// subagent-spawning tool call leaves behind, as attrs on that one event. It is
// mechanical like everything else here: it fires only for the spawn tool and
// only reads well-known response keys. Returns nil when there is nothing to
// record, so an ordinary tool call keeps an empty attrs off the wire.
func spawnEdgeFrom(tool string, resp json.RawMessage) map[string]string {
	if tool != "Agent" && tool != "Task" { // Task is the older name for the spawn tool
		return nil
	}
	var m map[string]any
	if json.Unmarshal(resp, &m) != nil {
		return nil
	}
	out := make(map[string]string)
	if id, ok := m["agentId"].(string); ok && id != "" {
		out["spawned_agent_id"] = id
	}
	for src, dst := range map[string]string{
		"totalTokens":       "spawned_tokens",
		"totalDurationMs":   "spawned_dur_ms",
		"totalToolUseCount": "spawned_tool_uses",
	} {
		if v, ok := m[src].(float64); ok { // JSON numbers decode to float64
			out[dst] = strconv.FormatInt(int64(v), 10)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// summaryFrom pulls a short, reproducible detail from the tool input without
// storing the whole payload. It prefers well-known keys, then falls back to a
// compacted, truncated form.
func summaryFrom(tool string, input json.RawMessage) string {
	if tool == "" {
		tool = "tool"
	}
	detail := "n/a"
	var m map[string]any
	if json.Unmarshal(input, &m) == nil {
		for _, k := range []string{"command", "file_path", "path", "url", "query", "pattern", "description"} {
			if v, ok := m[k].(string); ok && v != "" {
				detail = v
				break
			}
		}
		if detail == "n/a" && len(m) > 0 {
			if b, err := json.Marshal(m); err == nil {
				detail = string(b)
			}
		}
	}
	return truncate(tool+": "+detail, 160)
}

// statusFrom reads an error signal mechanically. It never interprets free text;
// if the response is not structured, it reports "ok" rather than guessing.
func statusFrom(resp json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(resp, &m) != nil {
		return "ok"
	}
	if b, ok := m["is_error"].(bool); ok && b {
		return "error"
	}
	if s, ok := m["error"].(string); ok && s != "" {
		return "error"
	}
	if s, ok := m["status"].(string); ok && validStatus[s] {
		return s
	}
	return "ok"
}

// validStatus is the closed status enum from the contract. A harness status
// outside it degrades to "ok": passing it through would emit a line the schema
// rejects, and guessing a mapping would be interpretation.
var validStatus = map[string]bool{
	"success":  true,
	"error":    true,
	"timeout":  true,
	"fallback": true,
	"ok":       true,
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
