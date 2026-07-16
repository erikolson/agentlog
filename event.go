// Package agentlog is a black box for agent sessions: it appends structured,
// one-event-per-line JSONL records that let you reconstruct what an agent did.
//
// It is deliberately small and opinion-free. It owns exactly one thing: the
// event schema and the append path. Higher layers — for example an enforcement
// tool like ratchet — import this package and write verdict events through it;
// a standalone session hook writes observation events through it. The
// dependency only ever points one way: consumers depend on agentlog, never the
// reverse. That keeps this package at pure substrate, portable into any repo.
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

// Kind distinguishes the two things the log can carry. The split is the
// substrate/bindingness line drawn inside the schema itself.
const (
	// KindObservation records something that happened. Advisory: you read it
	// later to debug. This is all a standalone logger ever writes.
	KindObservation = "observation"
	// KindVerdict records that a gate adjudicated an artifact. Binding: the
	// action it describes was already enforced by whoever wrote it. Normally
	// produced by an enforcement layer, not by a bare session hook.
	KindVerdict = "verdict"
)

// Event is one line in the log. Observation fields and verdict fields share one
// struct on purpose: a consumer that only ever writes observations still speaks
// the full schema, so an enforcement layer never has to fork it.
type Event struct {
	RunID string    `json:"run_id"`
	Seq   uint64    `json:"seq"`
	TS    time.Time `json:"ts"`
	Kind  string    `json:"kind"`

	// Observation fields.
	Stage   string `json:"stage,omitempty"`   // collection|llm_call|tool_call|safety|delivery
	Actor   string `json:"actor,omitempty"`   // the proposer that produced the event
	Summary string `json:"summary,omitempty"` // enough to reproduce/verify, never the full payload
	DurMS   int64  `json:"dur_ms,omitempty"`
	Status  string `json:"status,omitempty"` // success|error|timeout|fallback|ok

	// Verdict fields. Written by an enforcement layer, not by hand.
	Gate        string `json:"gate,omitempty"`
	Verdict     string `json:"verdict,omitempty"`     // pass|fail|waived|error
	Witness     string `json:"witness,omitempty"`     // content hash of the adjudicated artifact
	Adjudicator string `json:"adjudicator,omitempty"` // the ratifier; must differ from Actor
	Enforce     string `json:"enforce,omitempty"`     // block|warn|record

	// Attrs carries domain annotations, string→string. It is the contract's
	// only extension point: a domain annotates here and never adds a top-level
	// field, which is what lets the core stay frozen. Empty stays off the wire,
	// so an unannotated event serializes exactly as it did before Attrs existed.
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

// Emit stamps run id, sequence and timestamp, then appends one JSON line.
// Seq increments within a single Logger; across separate processes (e.g. one
// hook invocation per tool call) ordering is carried by TS, not Seq.
func (l *Logger) Emit(e Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	e.Seq = l.seq
	if e.RunID == "" {
		e.RunID = l.runID
	}
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
