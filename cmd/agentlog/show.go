package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// runShow prints one run, oldest first.
//
// It selects; it does not present. The lines come back exactly as they were
// written, so jq stays the thing that formats, filters and folds them:
//
//	agentlog show --run sess-9 | jq 'select(.kind=="verdict")'
//
// That split is the whole justification for the command existing at all.
// Selection needs to know the schema — which files, which id, what order — and
// getting any of those three wrong returns the wrong data silently. Presentation
// needs to know nothing, and jq is better at it. See
// docs/adr/0003-show-selects-jq-presents.md.
func runShow(args []string) {
	fs := flag.NewFlagSet("show", flag.ExitOnError)
	run := fs.String("run", "", "run id to select; matched exactly")
	global := fs.Bool("global", false, "read $HOME/.agentlog")
	project := fs.Bool("project", false, "read ./.agentlog (default)")
	_ = fs.Parse(args)

	if *run == "" {
		fmt.Fprintln(os.Stderr, "agentlog: --run is required")
		os.Exit(1)
	}

	t, err := resolveTarget(*global, *project, "", false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentlog:", err)
		os.Exit(1)
	}
	dir, err := t.logDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentlog:", err)
		os.Exit(1)
	}

	lines, err := selectRun(dir, *run)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentlog:", err)
		os.Exit(1)
	}
	if len(lines) == 0 {
		// Absence is a finding, not a failure — the same stance doctor takes.
		// It goes to stderr so an empty stdout stays pipeable.
		fmt.Fprintf(os.Stderr, "agentlog: no events for run %q in %s\n", *run, dir)
		return
	}
	for _, l := range lines {
		fmt.Println(l)
	}
}

// selectRun returns every line in dir belonging to run, oldest first, byte for
// byte as written.
//
// Three things to get right, and the documented grep/jq idioms get each of them
// wrong by default:
//
//   - Every file, not today's. A run that crosses midnight UTC lands in two.
//   - An exact run_id. `grep '"run_id":"sess-9"'` is exact only because of the
//     closing quote; drop it and sess-90's events join the answer silently.
//   - ts order, not seq and not file order. seq counts within one process, and
//     the hook is one process per tool call, so a whole day of events can read
//     seq: 1.
func selectRun(dir, run string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files) // dated names, so this is chronological; ts still decides

	type entry struct {
		ts   time.Time
		line string
	}
	var found []entry

	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			// Decoded as a bare map, never into Observation or Verdict. A reader
			// that folded a mixed stream into the sealed types is precisely the
			// case ADR 0001 names as its reversal condition; keeping this on
			// map[string]any is what lets that decision stand.
			var e map[string]any
			if json.Unmarshal([]byte(line), &e) != nil {
				continue // a torn line is doctor's to report, not ours to guess at
			}
			if id, _ := e["run_id"].(string); id != run {
				continue
			}
			// Parsed rather than string-compared: RFC 3339 trims trailing zeros
			// from the fraction, so a whole-second stamp ("…:52Z") sorts *after*
			// ("…:52.1Z") lexically. Zero time if absent or unparseable, and the
			// sort below is stable, so such a line keeps its file position.
			ts, _ := e["ts"].(string)
			parsed, _ := time.Parse(time.RFC3339, ts)
			found = append(found, entry{ts: parsed, line: line})
		}
	}

	sort.SliceStable(found, func(i, j int) bool { return found[i].ts.Before(found[j].ts) })

	out := make([]string, len(found))
	for i, e := range found {
		out[i] = e.line
	}
	return out, nil
}
