// Command agentlog appends structured events to a daily JSONL black box.
//
//	agentlog hook [flags]          read a PostToolUse payload on stdin, append it, exit 0
//	agentlog emit [flags]          append one explicit event
//	agentlog install-hook [flags]  wire the hook into a Claude Code settings.json
//	agentlog doctor [flags]        report whether the black box is recording
//	agentlog show --run ID         print one run, oldest first, as raw JSONL
//	agentlog version
//
// The two appending entry points share one appender. `hook` is the standalone,
// pure substrate path: a session hook shells out to it on every tool call.
// `emit` is the explicit path used for manual notes and by anything that
// prefers to shell out rather than import the package.
//
// `install-hook` and `doctor` touch no events. They exist so that wiring the
// recorder up, and checking that it is running, do not require hand-editing
// JSON or trusting that it worked. See
// docs/adr/0002-install-hook-edits-user-config.md.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/erikolson/agentlog"
)

var version = "0.5.0"

// defaultLogDir is where events land when AGENTLOG_DIR says nothing: beside the
// work, in the current directory.
const defaultLogDir = ".agentlog"

// The kinds emit routes on. These are CLI vocabulary — the flag value a user
// types — which happens to match the wire's; the library keeps its own copy
// unexported because there kind is implied by the method, never chosen.
const (
	kindObservation = "observation"
	kindVerdict     = "verdict"
)

// In Go an illegal event cannot be constructed: an Observation has no witness
// field to set. The CLI would be a way around that guarantee if it let one kind
// carry the other's flags, so it refuses them — before anything is written.
//
// --actor is on neither list: both kinds name a proposer. On a verdict it is
// what makes invariant I3 (actor != adjudicator) checkable by a reader.
var (
	observationOnlyFlags = []string{"stage", "status", "dur-ms"}
	verdictOnlyFlags     = []string{"gate", "verdict", "witness", "adjudicator", "enforce"}
)

// flagsPassed names the flags actually given on the command line, as opposed to
// sitting at their zero default. fs.Visit walks only what was set, which is the
// distinction the cross-check needs: --status "" is still --status.
func flagsPassed(fs *flag.FlagSet) map[string]bool {
	passed := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { passed[f.Name] = true })
	return passed
}

// forbidden returns the named flags that were passed, formatted for a message.
func forbidden(passed map[string]bool, names []string) []string {
	var got []string
	for _, n := range names {
		if passed[n] {
			got = append(got, "--"+n)
		}
	}
	return got
}

// attrFlag collects repeatable --attr key=value flags in the order given.
type attrFlag []string

func (a *attrFlag) String() string     { return strings.Join(*a, " ") }
func (a *attrFlag) Set(v string) error { *a = append(*a, v); return nil }

// reservedAttrKey names every top-level field the contract defines. attrs is a
// separate namespace by design, so an annotation may never shadow a core or
// projected field; a collision is dropped rather than recorded.
var reservedAttrKey = map[string]bool{
	"run_id": true, "seq": true, "ts": true, "kind": true,
	"stage": true, "actor": true, "agent_id": true, "agent_type": true,
	"summary": true, "dur_ms": true, "status": true,
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
	case "install-hook":
		runInstallHook(os.Args[2:])
	case "doctor":
		runDoctor(os.Args[2:])
	case "show":
		runShow(os.Args[2:])
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
	return defaultLogDir
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
	p := agentlog.ProjectHookPayload(raw) // projection first...
	o := p.Observation
	// ...then --attr annotations merged on top. The projection may have set
	// attrs itself (a spawn edge, per-subagent telemetry), so merge rather than
	// replace; a user --attr wins on the rare key collision.
	for k, v := range attrs {
		if o.Attrs == nil {
			o.Attrs = make(map[string]string)
		}
		o.Attrs[k] = v
	}
	run := p.RunID
	if run == "" {
		run = agentlog.NewRunID()
	}
	log, closeFn, err := agentlog.Open(dir(), run)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentlog:", err)
		os.Exit(0)
	}
	// A hook observes; it never adjudicates. EmitObservation is the only
	// appender it can reach, and the type makes that structural.
	if err := log.EmitObservation(o); err != nil {
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
	kind := fs.String("kind", kindObservation, "observation|verdict")
	stage := fs.String("stage", "", "collection|llm_call|tool_call|safety|delivery")
	actor := fs.String("actor", "", "proposer that produced the event (agent|subagent)")
	agentID := fs.String("agent-id", "", "instance id of the acting agent; empty for the top-level loop")
	agentType := fs.String("agent-type", "", "type of the acting agent, e.g. Explore")
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

	// Route on kind, refusing the other kind's flags. This runs before Open so a
	// rejected command leaves no file and no line behind: the log should never
	// record that someone tried to build an event that cannot exist.
	passed := flagsPassed(fs)
	var wrong []string
	switch *kind {
	case kindObservation:
		wrong = forbidden(passed, verdictOnlyFlags)
	case kindVerdict:
		wrong = forbidden(passed, observationOnlyFlags)
	default:
		fmt.Fprintf(os.Stderr, "agentlog: unknown --kind %q: want %s or %s\n",
			*kind, kindObservation, kindVerdict)
		os.Exit(1)
	}
	if len(wrong) > 0 {
		fmt.Fprintf(os.Stderr, "agentlog: --kind %s cannot carry %s\n",
			*kind, strings.Join(wrong, ", "))
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

	switch *kind {
	case kindObservation:
		err = log.EmitObservation(agentlog.Observation{
			Stage: *stage, Actor: *actor, AgentID: *agentID, AgentType: *agentType,
			Summary: *summary, DurMS: *dur, Status: *status, Attrs: attrs,
		})
	case kindVerdict:
		err = log.EmitVerdict(agentlog.Verdict{
			Gate: *gate, Verdict: *verdict, Witness: *witness, Actor: *actor,
			AgentID: *agentID, AgentType: *agentType,
			Adjudicator: *adj, Enforce: *enforce, Summary: *summary, Attrs: attrs,
		})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentlog:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `agentlog - a black box for agent sessions

usage:
  agentlog hook [flags]          read a PostToolUse payload on stdin, append it, exit 0
  agentlog emit [flags]          append one explicit event
  agentlog install-hook [flags]  wire the hook into a Claude Code settings.json
  agentlog doctor [flags]        report whether the black box is recording
  agentlog show --run ID         print one run, oldest first, as raw JSONL
  agentlog version

flags (both hook and emit):
  --attr key=value   domain annotation, recorded under attrs; repeatable

show — selects, never presents. Every file, exact --run, ts order; the lines
come out exactly as written, so jq does the rest:
  agentlog show --run sess-9 | jq 'select(.kind=="verdict")'
There is no --since, --kind or --format on purpose. Those are jq's.

install-hook — pick exactly one target; it never guesses which file to edit:
  --global           ~/.claude/settings.json, logging to $HOME/.agentlog
  --project          ./.claude/settings.json, logging to ./.agentlog
  --settings PATH    an explicit settings.json
  --dry-run          print the result, write nothing
It merges into an existing file, adds nothing if an agentlog hook is already
there, backs up to settings.json.bak before changing anything, and refuses to
touch a settings.json it cannot parse.

doctor — same target flags; defaults to --project. Reads, never writes.

env:
  AGENTLOG_DIR   log directory (default: .agentlog)
  AGENTLOG_RUN   run id for 'emit' when --run is not given
`)
}
