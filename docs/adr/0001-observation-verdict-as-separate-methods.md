# ADR 0001 — Observation and verdict as separate types and methods

## Status

Accepted.

## Context

[`spec/SPEC.md`](../../spec/SPEC.md) states invariant **I2 — no illegal field
mixing**: an observation cannot carry `witness`, a verdict cannot carry `stage`.
It further claimed the type system enforced this, which is why no runtime check
or schema rule exists for it — JSON Schema can require fields per branch but not
cleanly forbid the other branch's, so I2 was delegated to Layer 3 code.

Layer 3 did not do it. A single flat `Event` struct carried both variants'
fields, so `Event{Kind: KindObservation, Witness: "sha256:…"}` compiled, emitted,
and violated I2 with nothing to stop it. The contract asserted a guarantee the
code never made — the worst kind of drift, because every reader downstream is
entitled to rely on a stated invariant.

The fix has to make illegal states unrepresentable rather than merely detected.

## Options considered

**A — sealed `Body` interface.** One `Emit(Body)` method; `Observation` and
`Verdict` implement an interface sealed by an unexported method. Preserves a
single appender and permits heterogeneous handling in Go.

**B — two typed methods.** `EmitObservation(Observation)` and
`EmitVerdict(Verdict)`, with an unexported `wire` struct doing the marshalling.
The two payload types share only `Actor`, `Summary` and `Attrs`; each variant's
own fields exist on exactly one of them.

**C — runtime-validating constructor.** Keep the flat struct; add
`NewObservation(...)`/`Validate()` that rejects illegal combinations at run time.

## Decision

**B — two typed methods.**

Every call site knows its kind at compile time. The hook always writes
observations; an enforcement layer like ratchet always writes verdicts. There is
no caller that holds an event of unknown kind and needs to emit it
polymorphically, so A's interface would buy dispatch nobody performs while
adding a type, a sealing method, and an indirection to every call.

The heterogeneous stream is real, but it lives *on disk* — one file per day,
observations and verdicts interleaved, keyed by `run_id` — and it is read back
with `grep` and `jq`, never through this package. Go never holds a mixed
sequence, so Go needs no type that represents one.

C was rejected outright: it detects illegal states rather than preventing them.
`Validate()` returning an error still means the illegal value existed, still
means a caller can ignore it, and leaves I2 as a claim about diligence rather
than a claim about types. That is precisely the drift being repaired.

## Consequences

- **The Go API breaks.** `Event`, `Emit`, `KindObservation` and `KindVerdict`
  are gone. Callers move to `EmitObservation`/`EmitVerdict`. Signalled by the
  v0.2.0 minor bump; pre-1.0, so no `/v2` module path.
- **The wire format does not change.** Both kinds marshal through an unexported
  `wire` struct that preserves the exact field names, order and `omitempty`
  behavior of the old `Event`. Emitted JSON is byte-identical, so every existing
  log, consumer and `jq` filter keeps working. A test pins the key order.
- **I2 is now true by construction.** `Observation` has no `Witness` field to
  set. The invariant needs no test, because the code that would violate it does
  not compile.
- **The CLI enforces the same line by hand.** `emit` routes on `--kind` and
  rejects the other kind's flags before writing, so shelling out is not a
  backdoor around a guarantee the library makes structurally.
- **`ProjectHookPayload` returns a `Projection`.** An `Observation` carries no
  `run_id` — the logger stamps that — so the harness-supplied session id comes
  back beside it, in a named field rather than a bare positional string.
- **A `Verdict` names both `Actor` and `Adjudicator`.** I2 forbids a verdict a
  `stage`, never an actor; naming the proposer as well as the ratifier is what
  keeps I3 (`actor != adjudicator`) checkable over the stream instead of vacuous
  for everything this package emits.

## Reversal condition

If a Go caller ever legitimately needs to fold a mixed observation/verdict
stream without knowing the kind at compile time — a reader, a replayer, an
in-process fan-out — then B's premise is dead and this should be revisited
toward **A**, whose sealed interface exists precisely to type that case. Adding
a reader to this package would be the trigger to look again.
