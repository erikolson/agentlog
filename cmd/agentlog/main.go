// Command agentlog appends structured events to a daily JSONL black box.
//
//	agentlog hook            read a PostToolUse payload on stdin, append it, exit 0
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

	"github.com/erikolson/agentlog"
)

var version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "hook":
		runHook()
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
func runHook() {
	raw, _ := io.ReadAll(os.Stdin)
	e := agentlog.ProjectHookPayload(raw)
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
	_ = fs.Parse(args)

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
	}
	if err := log.Emit(e); err != nil {
		fmt.Fprintln(os.Stderr, "agentlog:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `agentlog - a black box for agent sessions

usage:
  agentlog hook            read a PostToolUse payload on stdin, append it, exit 0
  agentlog emit [flags]    append one explicit event
  agentlog version

env:
  AGENTLOG_DIR   log directory (default: .agentlog)
  AGENTLOG_RUN   run id for 'emit' when --run is not given
`)
}
