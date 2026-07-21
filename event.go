// Package agentlog is a black box for agent sessions: it appends structured,
// one-event-per-line JSONL records that let you reconstruct what an agent did.
//
// It is deliberately small and opinion-free. It owns exactly one thing: the
// event schema and the append path. Higher layers — for example an enforcement
// tool like ratchet — import this package and write verdict events through it;
// a standalone session hook writes observation events through it. The
// dependency only ever points one way: consumers depend on agentlog, never the
// reverse. That keeps this package at pure substrate, portable into any repo.
//
// The log carries two kinds of event, and they are two distinct Go types:
// write an Observation with EmitObservation, a Verdict with EmitVerdict. The
// stream is heterogeneous on disk, keyed by run_id, but never in Go — every
// call site knows which kind it is writing at compile time. See
// docs/adr/0001-observation-verdict-as-separate-methods.md.
package agentlog

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The two kinds the log can carry — the substrate/bindingness line drawn inside
// the schema itself. Unexported because kind is never the caller's to choose:
// it follows from which method you call, so it can never disagree with the
// payload beside it.
const (
	kindObservation = "observation"
	kindVerdict     = "verdict"
)

// Observation records something that happened. Advisory: you read it later to
// debug. This is all a standalone logger ever writes.
//
// It declares no witness, gate, adjudicator or enforce field. That absence is
// the enforcement: contract invariant I2 (no illegal field mixing) holds
// because an observation carrying a witness is not a value this package can
// represent, so it needs no runtime check and admits no bug.
type Observation struct {
	Stage string // collection|llm_call|tool_call|safety|delivery

	// Actor identity. Actor is the kind of proposer — "agent" for the
	// top-level loop, "subagent" for a nested one. AgentID and AgentType name
	// the specific instance and its type (e.g. "Explore") when the harness
	// reports them; both are empty for the top-level agent. They exist because
	// subagents run concurrently, so one log interleaves many actors by ts and
	// AgentID is the only key that demuxes them. Parent/tree structure is never
	// stamped here — it is reconstructed downstream from spawn events. See
	// docs/adr/0004-agent-identity-as-core-fields.md.
	Actor     string
	AgentID   string
	AgentType string

	Summary string // enough to reproduce/verify, never the full payload
	DurMS   int64
	Status  string // success|error|timeout|fallback|ok

	// Attrs carries domain annotations, string→string: the contract's only
	// extension point. Empty stays off the wire.
	Attrs map[string]string
}

// Verdict records that a gate adjudicated an artifact. Binding: the action it
// describes was already enforced by whoever wrote it. Normally produced by an
// enforcement layer, not by a bare session hook.
//
// It declares no stage, status or dur_ms field, for the same reason Observation
// declares no witness: the type system holds I2, not a runtime check. It does
// declare Actor, which I2 never forbade a verdict: a verdict names both the
// proposer and the ratifier so that a reader can check I3 (Actor != Adjudicator)
// over the stream.
type Verdict struct {
	Gate        string
	Verdict     string // pass|fail|waived|error
	Witness     string // content hash of the adjudicated artifact
	Actor       string // the proposer whose work was adjudicated
	Adjudicator string // the ratifier; invariant I3 wants this to differ from Actor
	Enforce     string // block|warn|record
	Summary     string // enough to reproduce/verify, never the full payload

	// AgentID and AgentType qualify Actor exactly as they do on an Observation:
	// when the adjudicated work was produced by a subagent, they name which one.
	// Normally empty, since verdicts come from an enforcement layer rather than
	// the session hook.
	AgentID   string
	AgentType string

	// Attrs carries domain annotations, string→string: the contract's only
	// extension point. Empty stays off the wire.
	Attrs map[string]string
}

// wire is the on-disk shape: one flat object per line, carrying exactly the
// field names, order and omitempty behavior published in spec/event.schema.json.
// The public API splits in two, but the contract is one line per event, so both
// kinds funnel through here.
//
// Renaming a tag, reordering a field or dropping an omitempty in this struct
// changes the published contract, not just this code. The split above is a Go
// concern; this struct is the promise to every other language.
type wire struct {
	RunID string    `json:"run_id"`
	Seq   uint64    `json:"seq"`
	TS    time.Time `json:"ts"`
	Kind  string    `json:"kind"`

	// Observation fields.
	Stage string `json:"stage,omitempty"`

	// Actor identity — shared by both kinds. actor rides in the observation
	// slot but a verdict emits it too; agent_id/agent_type sit beside it. All
	// three are omitempty, so an event that names no subagent is byte-identical
	// to what contract v1 emitted.
	Actor     string `json:"actor,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	AgentType string `json:"agent_type,omitempty"`

	Summary string `json:"summary,omitempty"`
	DurMS   int64  `json:"dur_ms,omitempty"`
	Status  string `json:"status,omitempty"`

	// Verdict fields.
	Gate        string `json:"gate,omitempty"`
	Verdict     string `json:"verdict,omitempty"`
	Witness     string `json:"witness,omitempty"`
	Adjudicator string `json:"adjudicator,omitempty"`
	Enforce     string `json:"enforce,omitempty"`

	Attrs map[string]string `json:"attrs,omitempty"`
}

// Logger appends events to an io.Writer as JSONL. Safe for concurrent use.
type Logger struct {
	mu    sync.Mutex
	w     io.Writer
	runID string
	seq   uint64
}

// New returns a Logger that writes to w, stamping every event with runID.
func New(w io.Writer, runID string) *Logger {
	return &Logger{w: w, runID: runID}
}

// Open returns a Logger appending to dir/YYYY-MM-DD.jsonl, creating dir if
// needed. The returned func closes the underlying file. One file per day keeps
// the log greppable; reconstruct a single run by filtering on run_id.
func Open(dir, runID string) (*Logger, func() error, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	name := time.Now().UTC().Format("2006-01-02") + ".jsonl"
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}
	return New(f, runID), f.Close, nil
}

// EmitObservation appends one observation to the log.
func (l *Logger) EmitObservation(o Observation) error {
	return l.emit(wire{
		Kind:      kindObservation,
		Stage:     o.Stage,
		Actor:     o.Actor,
		AgentID:   o.AgentID,
		AgentType: o.AgentType,
		Summary:   o.Summary,
		DurMS:     o.DurMS,
		Status:    o.Status,
		Attrs:     o.Attrs,
	})
}

// EmitVerdict appends one verdict to the log.
func (l *Logger) EmitVerdict(v Verdict) error {
	return l.emit(wire{
		Kind:        kindVerdict,
		Gate:        v.Gate,
		Verdict:     v.Verdict,
		Witness:     v.Witness,
		Actor:       v.Actor,
		AgentID:     v.AgentID,
		AgentType:   v.AgentType,
		Adjudicator: v.Adjudicator,
		Enforce:     v.Enforce,
		Summary:     v.Summary,
		Attrs:       v.Attrs,
	})
}

// emit stamps the three fields only the logger owns — run id, sequence and
// timestamp — then appends one JSON line. Seq increments within a single
// Logger; across separate processes (e.g. one hook invocation per tool call)
// ordering is carried by TS, not Seq.
func (l *Logger) emit(e wire) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	e.Seq = l.seq
	e.RunID = l.runID
	e.TS = time.Now().UTC()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(l.w, string(b))
	return err
}

// NewRunID returns a time-sortable, greppable id for correlating a single run
// when no external session id is available.
func NewRunID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%013d-%x", time.Now().UTC().UnixMilli(), b)
}
