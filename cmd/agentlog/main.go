// Command agentlog appends structured events to a daily JSONL black box.
//
//	agentlog hook [flags]    read a PostToolUse payload on stdin, append it, exit 0
//	agentlog emit [flags]    append one explicit event
//	agentlog version
//
// The two entry points share one appender. `hook` is the standalone, pure
// substrate path: a session hook shells out to it on every tool call. `emit`
// is the explicit path used for manual notes and by anything that prefers to
// shell out rather than import the package.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/erikolson/agentlog"
)

var version = "0.1.1"

// attrFlag collects repeatable --attr key=value flags in the order given.
type attrFlag []string

func (a *attrFlag) String() string     { return strings.Join(*a, " ") }
func (a *attrFlag) Set(v string) error { *a = append(*a, v); return nil }

// reservedAttrKey names every top-level field the contract defines. attrs is a
// separate namespace by design, so an annotation may never shadow a core or
// projected field; a collision is dropped rather than recorded.
var reservedAttrKey = map[string]bool{
	"run_id": true, "seq": true, "ts": true, "kind": true,
	"stage": true, "actor": true, "summary": true, "dur_ms": true, "status": true,
	"gate": true, "verdict": true, "witness": true, "adjudicator": true, "enforce": true,
	"attrs": true,
}

// parseAttrs turns repeatable key=value flags into the contract's attrs map. It
// splits on the first "=" only, so a value may itself contain "="; on duplicate
// keys the last wins.
//
// The two return paths exist because the callers need different postures from
// one parser. dropped holds keys that collide with a top-level field: never
// fatal, since ignoring one still leaves the event correct. errs holds
// malformed entries, which emit treats as fatal and hook merely notes.
func parseAttrs(raw []string) (attrs map[string]string, dropped []string, errs []error) {
	for _, s := range raw {
		k, v, ok := strings.Cut(s, "=")
		if !ok || k == "" {
			errs = append(errs, fmt.Errorf("malformed --attr %q: want key=value", s))
			continue
		}
		if reservedAttrKey[k] {
			dropped = append(dropped, k)
			continue
		}
		if attrs == nil {
			attrs = make(map[string]string)
		}
		attrs[k] = v
	}
	return attrs, dropped, errs
}

// reportDropped notes reserved keys on stderr. Both callers treat these the
// same way: say so, write the event anyway.
func reportDropped(dropped []string) {
	for _, k := range dropped {
		fmt.Fprintf(os.Stderr, "agentlog: --attr %q ignored: reserved field name\n", k)
	}
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "hook":
		runHook(os.Args[2:])
	case "emit":
		runEmit(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Println("agentlog", version)
	default:
		usage()
		os.Exit(2)
	}
}

func dir() string {
	if d := os.Getenv("AGENTLOG_DIR"); d != "" {
		return d
	}
	return ".agentlog"
}

// runHook never blocks the session: any error is swallowed to stderr and we
// still exit 0. It writes nothing to stdout so the harness cannot misread its
// output as instructions. Record-only: it observes, it never gates.
func runHook(args []string) {
	var attrList attrFlag
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(os.Stderr) // never stdout: the harness must not read us as instructions
	fs.Var(&attrList, "attr", "repeatable key=value annotation, recorded under attrs")
	// ContinueOnError, not ExitOnError: a bad flag must not take the session down.
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "agentlog:", err)
	}
	attrs, dropped, errs := parseAttrs(attrList)
	reportDropped(dropped)
	for _, err := range errs {
		// Same parser as emit, opposite posture: note it and still record.
		fmt.Fprintln(os.Stderr, "agentlog:", err)
	}

	raw, _ := io.ReadAll(os.Stdin)
	e := agentlog.ProjectHookPayload(raw) // projection first...
	e.Attrs = attrs                       // ...then annotations, over no projected field
	run := e.RunID
	if run == "" {
		run = agentlog.NewRunID()
	}
	log, closeFn, err := agentlog.Open(dir(), run)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentlog:", err)
		os.Exit(0)
	}
	if err := log.Emit(e); err != nil {
		fmt.Fprintln(os.Stderr, "agentlog:", err)
	}
	// Closed explicitly, not deferred: os.Exit does not run deferred functions.
	if err := closeFn(); err != nil {
		fmt.Fprintln(os.Stderr, "agentlog:", err)
	}
	os.Exit(0)
}

func runEmit(args []string) {
	fs := flag.NewFlagSet("emit", flag.ExitOnError)
	kind := fs.String("kind", agentlog.KindObservation, "observation|verdict")
	stage := fs.String("stage", "", "collection|llm_call|tool_call|safety|delivery")
	actor := fs.String("actor", "", "proposer that produced the event")
	summary := fs.String("summary", "", "short summary; enough to reproduce/verify, not the payload")
	status := fs.String("status", "", "success|error|timeout|fallback|ok")
	dur := fs.Int64("dur-ms", 0, "duration in milliseconds")
	gate := fs.String("gate", "", "verdict: gate name")
	verdict := fs.String("verdict", "", "verdict: pass|fail|waived|error")
	witness := fs.String("witness", "", "verdict: content hash of adjudicated artifact")
	adj := fs.String("adjudicator", "", "verdict: ratifier (must differ from actor)")
	enforce := fs.String("enforce", "", "verdict: block|warn|record")
	run := fs.String("run", "", "run id; defaults to $AGENTLOG_RUN or a generated id")
	var attrList attrFlag
	fs.Var(&attrList, "attr", "repeatable key=value annotation, recorded under attrs")
	_ = fs.Parse(args)

	attrs, dropped, errs := parseAttrs(attrList)
	reportDropped(dropped)
	// Fail loud for an interactive caller, and before anything is written: a
	// half-understood annotation should not reach the log at all.
	if len(errs) > 0 {
		for _, err := range errs {
			fmt.Fprintln(os.Stderr, "agentlog:", err)
		}
		os.Exit(1)
	}

	runID := *run
	if runID == "" {
		runID = os.Getenv("AGENTLOG_RUN")
	}
	if runID == "" {
		runID = agentlog.NewRunID()
	}

	log, closeFn, err := agentlog.Open(dir(), runID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentlog:", err)
		os.Exit(1)
	}
	defer closeFn()

	e := agentlog.Event{
		Kind: *kind, Stage: *stage, Actor: *actor, Summary: *summary,
		Status: *status, DurMS: *dur, Gate: *gate, Verdict: *verdict,
		Witness: *witness, Adjudicator: *adj, Enforce: *enforce,
		Attrs: attrs,
	}
	if err := log.Emit(e); err != nil {
		fmt.Fprintln(os.Stderr, "agentlog:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `agentlog - a black box for agent sessions

usage:
  agentlog hook [flags]    read a PostToolUse payload on stdin, append it, exit 0
  agentlog emit [flags]    append one explicit event
  agentlog version

flags (both hook and emit):
  --attr key=value   domain annotation, recorded under attrs; repeatable

env:
  AGENTLOG_DIR   log directory (default: .agentlog)
  AGENTLOG_RUN   run id for 'emit' when --run is not given
`)
}
