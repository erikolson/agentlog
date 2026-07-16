package agentlog

import "encoding/json"

// hookPayload is the subset of a coding-agent PostToolUse payload we read.
// Field names follow the Claude Code convention; other harnesses (Gemini CLI,
// Codex CLI) share the shape closely enough that this projection degrades
// gracefully rather than breaking on them.
type hookPayload struct {
	SessionID    string          `json:"session_id"`
	ToolName     string          `json:"tool_name"`
	ToolInput    json.RawMessage `json:"tool_input"`
	ToolResponse json.RawMessage `json:"tool_response"`
}

// ProjectHookPayload turns a raw PostToolUse payload into an observation Event.
//
// It extracts fields *mechanically* — no interpretation, no model call — which
// is what keeps the logger pure procedural code. Structured input in, known
// fields out. It is best-effort and never fails on shape: missing or malformed
// fields become sane defaults so a bad payload degrades the summary rather than
// crashing the session.
func ProjectHookPayload(raw []byte) Event {
	var p hookPayload
	_ = json.Unmarshal(raw, &p) // tolerate anything; zero values are fine

	e := Event{
		Kind:    KindObservation,
		Stage:   "tool_call",
		Actor:   "agent",
		Summary: summaryFrom(p.ToolName, p.ToolInput),
		Status:  statusFrom(p.ToolResponse),
	}
	if p.SessionID != "" {
		e.RunID = p.SessionID // the harness-supplied id is what correlates a run
	}
	return e
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
		for _, k := range []string{"command", "file_path", "path", "url", "query", "pattern"} {
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
