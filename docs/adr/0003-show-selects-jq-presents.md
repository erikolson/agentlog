# ADR 0003 — show selects, jq presents

## Status

Accepted. Amends the refusal list in [the README](../../README.md).

## Context

The README refused "a query language" and answered the question with *"Querying
is `grep` and `jq`."* That refusal is right about the danger and wrong about
where the danger is.

Two facts undercut it.

**The CLI already reads.** `doctor` opens today's file, parses every line, and
reports events, distinct run ids and malformed lines. That is a query — a canned
one, shipped in v0.3.0. The line "the CLI never reads the log" was already not
true when it was written down.

**The refusal's own reason does not reach the CLI.** It says: *the moment this
**package** grows an opinion, it stops being safe substrate*. That is a claim
about the thing ratchet imports. A command in `cmd/` touches neither the
package's API, the wire contract, nor the dependency arrow.

And the documented idioms have three measured defects, all found while writing
the querying docs they appear in:

- `grep '"run_id":"sess-9"'` is exact **only** because of the closing quote.
  Delete one character and `sess-90`'s events join the answer: 6 lines instead of
  4, no error, wrong data.
- `seq` counts within one process, and the hook is one process per tool call, so
  a whole day of events can read `seq: 1`. The field named "sequence" does not
  sequence.
- A run crossing midnight UTC is in two files, so today's filename is the wrong
  default.

The headline recipe in our own README is one deleted character from silently
returning a different run. "Read the docs more carefully" is a poor answer when
the tool knows its own schema and the user's shell does not.

## Decision

Add `agentlog show --run ID`. It reads every `*.jsonl` in the log dir,
exact-matches `run_id`, sorts by `ts`, and prints **the raw lines unchanged**.

The line moves, and this is where it now sits:

> **Selection yes. Presentation no.**

Selection needs to know the schema — which files, which id, what order — and
getting any of those wrong returns wrong data quietly. Presentation needs to know
nothing about agentlog, and jq is better at it than anything shipped here will
be. So `show` does the first and refuses the second, which is what keeps it
composable rather than competitive:

```sh
agentlog show --run sess-9 | jq 'select(.kind=="verdict")'
```

Printing raw is not a stylistic choice; it is the mechanism that holds the line.
A command that formatted its output would need `--format`, then `--limit`, then
`--fields`, and the slope is the whole thing the refusal exists to prevent. With
raw output there is nothing to add, because jq already added it.

`show` decodes into `map[string]any`, never into `Observation` or `Verdict`.
[ADR 0001](0001-observation-verdict-as-separate-methods.md) names exactly this
case as its reversal condition — *"if a Go caller ever needs to fold a mixed
stream without knowing the kind at compile time — a reader"* — so keeping the
reader on bare maps, in `cmd/`, is what lets that decision stand unchanged.

## Alternatives

**Docs only.** Ship the verified recipes and disclose the footguns; that is the
status quo as of the previous commit. Rejected: it leaves a silent-wrong-answer
in the documented happy path. A footgun you have documented is still a footgun,
and the failure mode is not an error — it is plausible data from the wrong run.

**`show` with predicates** — `--since`, `--kind`, `--status`, `--format`.
Rejected: this is the accretion the refusal names. Each flag is individually
reasonable, which is precisely how a query language arrives. Every one of them is
a jq expression that already works and already composes.

## Consequences

- One canned query, like `doctor` is one canned report. Neither is a language.
- The three selection defects are fixed by construction rather than by the
  reader's memory of a jq incantation.
- `grep` and `jq` remain the documented, first-class way to read the log, and
  still work with no agentlog on the machine at all. `show` is a convenience
  over a format that remains the API; it must never become the only way in.
- The refusal list in the README is amended to say what is actually refused: a
  query language, not a read command.

## Reversal

The line holds only as long as `show` stays one selector with no presentation.
**If `show` grows a second predicate or any output flag, this ADR has failed** —
that is the signal that the slope is real and the honest move is to revert to
docs-only and let jq have it back. Adding `--since` because it is "just one more"
is the exact failure this records in advance.
