# agentlog

A black box for agent sessions. One JSONL file per day, one structured line per
event, so you can reconstruct what an agent actually did when something breaks.

It does exactly one thing — own the event schema and the append path — and
refuses to do anything else. That refusal is the point.

## Why

A flight recorder that only records when the pilot remembers to flip it isn't a
flight recorder. Determinism is what lets you *not* log: if you can hold the
whole computation in your head and re-run it, the black box is redundant. Agents
delete that affordance. You delegated the work to a non-deterministic actor and
weren't watching each step, so the event stream is the only thing standing
between you and archaeology. `agentlog` is that stream, in its simplest binding
form.

## What it is (and isn't)

`agentlog` sits at **enforced observation**. When wired as a session hook, the
harness runs it on every tool call whether the agent cooperates or not — it's
procedural code, not a suggestion to a model. But observation is record-only:
the strongest a logger can ever be is "the event is guaranteed recorded when it
fires." The power to *block* is a property of verdicts, not observations, and it
lives one layer up. So this is already at the top of its own column, not falling
short of some other one.

Two levels of the same stream:

| level          | who writes it     | binding? |
| -------------- | ----------------- | -------- |
| observation    | a session hook    | recorded-when-fired |
| verdict        | an enforcement layer (e.g. ratchet) | the action it describes was already enforced |

Both are the same stream and the same wire format, written through one package —
`EmitObservation` and `EmitVerdict`. In Go they are two distinct types, so an
observation carrying a verdict's `witness` is not a value you can construct
([ADR 0001](docs/adr/0001-observation-verdict-as-separate-methods.md)); on disk
they are one line each in one file. A consumer that only ever writes
observations still speaks the full contract, so the enforcement layer never has
to fork it.

## The one rule: dependency direction

```
                 imports
  ratchet  ───────────────▶  agentlog        (binding layer depends on substrate)
  hook     ───── shells ───▶  agentlog        (basic use depends on substrate)

  agentlog ──▶  (nothing)                     (substrate depends on nobody)
```

The arrow never inverts. `agentlog` knows nothing about gates, enforcement, or
any specific tool. Keep it that way and it stays portable into any repo; invert
it and you've welded your general black box to one enforcement tool and lost the
basic-use story.

## Layout

agentlog is a service domain; its layers map to directories where Go's layout
allows and to a clear mapping where it doesn't.

| layer | what | where |
| ----- | ---- | ----- |
| 1 — contract | the event spec, system of record | [`spec/`](spec/) — `SPEC.md`, `event.schema.json`, golden examples |
| 2 — standards | invariants as conformance tests | [`standards/`](standards/) |
| 3 — platform | the paved road that emits conforming events | root package + [`cmd/`](cmd/) |
| 4 — implementations | adapters that speak the contract | the session hook, ratchet, `examples/settings.json` |

The contract is canonical; the code derives from it. The JSON Schema is
language-agnostic — validate any producer's output in CI with any tool (e.g.
`check-jsonschema spec/examples/*.json`), and run `go test ./standards/...` for
the relational invariants the schema can't express. Drift between an emitted
event and the contract is a failing check, not a code review comment.

## The schema

One line per event. Observation and verdict share one flat wire shape; empty
fields are omitted. They are separate types in Go and a single object on disk —
the split is a Go concern, the line is the contract.

```json
{"run_id":"sess-9","seq":7,"ts":"2026-07-15T18:04:22.114Z","kind":"observation","stage":"tool_call","actor":"agent","summary":"Bash: go test ./...","status":"error"}
{"run_id":"sess-9","seq":8,"ts":"2026-07-15T18:04:23.902Z","kind":"verdict","gate":"tests","verdict":"fail","witness":"sha256:9f2c…","adjudicator":"ratchet-check","enforce":"block","summary":"3 failing in ./auth"}
```

`witness` binds a verdict to a content hash, not a filename: a pass authorizes
*that* artifact and no other, so if the file changes the pass silently stops
applying — drift-as-build-failure with no extra machinery. `adjudicator` is the
ratifier, and keeping it distinct from `actor` lets `proposer ≠ ratifier` be a
schema invariant a reader can check over the stream.

`attrs` is the one extension point: an object of string→string where a domain
hangs its own data. Everything else is sealed — `additionalProperties: false`
makes a new top-level field a breaking change, while a new `attrs` key breaks
nobody. That asymmetry is the point: annotations get a bounded place to live so
the core never has to grow to accommodate them.

The design discipline is **summary, not payload**: store enough to reproduce and
verify, never the full request/response. That keeps the log greppable and cheap
(a heavy day gzips to tens of KB), and it keeps field extraction *mechanical* —
structured in, known fields out, no model in the loop.

## Use it — basic (standalone black box)

Build the binary and put it on your PATH:

```sh
go install github.com/erikolson/agentlog/cmd/agentlog@latest
```

Wire it into Claude Code so it fires on every tool call. Add this to
`~/.claude/settings.json` for a global recorder, or to a project's
`.claude/settings.json` to scope it to one repo:

```json
{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "AGENTLOG_DIR=$HOME/.agentlog agentlog hook --attr project=$CLAUDE_PROJECT_DIR"
          }
        ]
      }
    ]
  }
}
```

That is the whole contract, and it is the same one anything else speaks — another
harness, a CI step, a config manager. `AGENTLOG_DIR` picks the log directory
(default `.agentlog`, beside the work); `--attr` stamps every event with an
annotation. The hook reads the PostToolUse payload on stdin, appends an
observation, writes nothing to stdout, and always exits 0 — it can never break a
session. The same block lives in
[`examples/settings.json`](examples/settings.json).

Or, one command:

```sh
agentlog install-hook --global     # ~/.claude/settings.json
agentlog install-hook --project    # ./.claude/settings.json
```

It writes exactly the stanza above. What it buys you is care, not brevity: it
**merges** into your existing settings rather than overwriting them, **adds
nothing** if an agentlog hook is already there, writes a **`settings.json.bak`**
with your original bytes before changing anything, and **refuses** to touch a
settings file it cannot parse. `--dry-run` prints the result and writes nothing.
It picks no target by default — a command that edits your config does not guess
which file. The contract it holds itself to is
[ADR 0002](docs/adr/0002-install-hook-edits-user-config.md).

One caveat worth knowing: a merge round-trips through a JSON encoder, so your
file comes back re-indented and its keys reordered. The data is preserved
exactly; the formatting is not. The `.bak` keeps your original.

Then ask whether it is actually recording:

```sh
agentlog doctor
```

It reports the version, whether a hook is wired and in which file, the log
directory it resolved and whether it is writable, and how many events and runs
landed today. It only reads, and it only reports — no hook installed is a
finding, not an error, so it exits 0 either way. Interpretation is yours.

Read a run back:

```sh
grep '"run_id":"sess-9"' ~/.agentlog/2026-07-15.jsonl | jq .
```

Drop a manual milestone marker mid-session for the *why* a hook can't infer:

```sh
agentlog emit --stage delivery --summary "starting auth refactor"
```

Annotate any event with `--attr key=value`, repeatable. It is the only way to
add your own data, and it lands under `attrs`:

```sh
agentlog emit --stage delivery --summary "starting auth refactor" \
  --attr repo=auth --attr ticket=PROJ-1234
```

`--attr` works on `hook` too — that's how the wiring above stamps every event
with the project directory. A value splits on the first `=` only, so it may
itself contain `=`; on a repeated key the last wins. A key that names a
top-level field (`status`, `run_id`, …) is ignored rather than allowed to shadow
the core: `attrs` is a separate namespace by design. A malformed `--attr` is
fatal for `emit`, which fails before writing anything, and skipped by `hook`,
which still records the event and still exits 0 — the recorder never breaks a
session.

## Use it — as a dependency (enforcement layer)

Import the package and write verdicts through the same appender:

```go
log, closeFn, _ := agentlog.Open(".agentlog", runID)
defer closeFn()

log.EmitVerdict(agentlog.Verdict{
    Gate:        "tests",
    Verdict:     "fail",
    Witness:     "sha256:" + hash,
    Adjudicator: "ratchet-check",
    Enforce:     "block",
    Summary:     "3 failing in ./auth",
    Attrs:       map[string]string{"pkg": "./auth"},
})
```

There is no `Kind` field to set: the method you call *is* the kind. A
`Verdict` has no `Stage` and an `Observation` has no `Witness`, so the contract's
I2 (no illegal field mixing) is held by the compiler rather than by a check you
could forget to run.

Observations and verdicts land in the same daily file, keyed by `run_id`, so the
enforcement layer's Feedback face reads exactly the stream its Verify face wrote.

## What this refuses to become

Knowing what to leave out is the whole design. `agentlog` will not grow: log
levels, a query language, rotation daemons, remote sinks, dashboards, config
files, or any interpretation of unstructured output. Rotation is the date in the
filename. Compression is a cron job. Querying is `grep` and `jq`. Interpretation
is someone else's face. Every one of those omissions is deliberate — the moment
this package grows an opinion, it stops being safe substrate for the things
built on top of it.

## Limitations (honest)

- `seq` increments within a single process. With one hook invocation per tool
  call (separate processes), ordering within a run is carried by `ts`, not
  `seq`.
- The hook only sees events the harness mediates. "Every tool call is logged" is
  guaranteed; "everything the agent thought" is not — reasoning without a tool
  call produces no tool-call event to hook.
- Payload projection is best-effort across harnesses. Field names track the
  Claude Code convention and degrade gracefully elsewhere.

## License

MIT. See [LICENSE](LICENSE).
