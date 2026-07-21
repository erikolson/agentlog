# ADR 0004 — Agent identity as core fields; the tree is reconstructed downstream

## Status

Accepted. Breaks the contract (v1 → v2); logged in
[`spec/SPEC.md`](../../spec/SPEC.md).

## Context

The recorder wires in as a `PostToolUse` hook, which fires on every leaf tool
call — including those made *inside* a subagent, since spawning and entering a
subagent are themselves tool calls. Subagents do not run one-at-a-time: the
harness launches them concurrently and keeps background ones running while the
main loop continues. So a single run's stream is not one sequential actor; it is
several actors appending to one daily file, interleaved and ordered only by
`ts`. `seq` cannot order them — it is per-producer-process, and each hook
invocation is its own process.

Contract v1 had no way to say *which* agent produced an event. `actor` was a
hardcoded `"agent"`. That made any concurrent fan-out unattributable: two
subagents' tool calls landed as indistinguishable lines. For a black box whose
entire purpose is "reconstruct what an agent actually did," that is a hole, not
a nicety — and it is a hole *today*, the first time two agents run at once, not
a future-proofing concern.

What the harness gives us per event was the deciding constraint. Confirmed
against the current hook docs: a tool call inside a subagent carries exactly two
extra fields — `agent_id` (the subagent's own instance) and `agent_type` (e.g.
`"Explore"`). It carries **no** parent, ancestry, or depth. Multi-level nesting
is real (a subagent inherits the `Agent` tool and can spawn its own), but every
level still sees only its own id. The parent is observable **only** at the spawn
moment, on the `Agent`-tool event, whose response names the child.

## Options considered

**A — attrs only.** Put `agent_id`/`agent_type` in `attrs`; no schema break,
following the repo's "new data goes in `attrs`" default.

**B — `agent_id` + `agent_type` as core fields.** Promote both to first-class
optional fields on either kind; `actor` becomes the kind (`agent`/`subagent`).
Breaking, because `additionalProperties: false` rejects unknown keys.

**C — B plus per-event `parent_id` (and `top_agent_id`).** Denormalize the full
ancestry onto every line so a subtree is a one-line `grep`.

## Decision

**B.** `agent_id` and `agent_type` are core; `actor` carries the kind. The
spawn `parent → child` edge and per-subagent cost telemetry ride in `attrs` on
the spawn event (`spawned_agent_id`, `spawned_tokens`, `spawned_dur_ms`,
`spawned_tool_uses`). The **tree is reconstructed by the reader**, never stamped
by the recorder.

Identity is structural, not annotation. The domain is agent-harness workflows;
"which agent" sits at the same altitude as "which run," and `actor` already
occupies the "who" slot as core — `agent_id` is its instance refinement. That is
what pushes it over the bar A reserves for genuine domain annotation, and the
pre-1.0 freeze (nothing depends on the contract yet) makes the break cheap. So
A's "keep it out of core" loses here even though it is the usual default.

C was rejected on the confirmed constraint, not taste. `parent_id` is not in a
per-event payload, so populating it on every line forces the recorder to hold
cross-call state — read a sidecar index of child→parent — which is exactly the
statelessness this package exists to keep. `top_agent_id` is worse: the root of
every tree is the session's main loop, which `agent_id == ""` under a shared
`run_id` already identifies, so it only restates `run_id`.

The edge-on-spawn design gives C's power without its cost. Every subagent is born
from exactly one spawn event; that event runs in the spawner's context (its own
`agent_id` is on the line) and names the child in its response. Recording the
edge there yields a complete edge set for arbitrary depth, from which a reader
computes parent, depth, and subtree membership — while the recorder stays a pure
mechanical projection.

## Consequences

- **Contract v1 → v2.** A v1 validator rejects a v2 line only because
  `additionalProperties: false` forbids the new keys. Both fields are
  `omitempty`, so any event that names no subagent is byte-identical to v1 — the
  wire-order test pins this.
- **The projection derives identity mechanically.** `actor` is `subagent` iff
  `agent_id` is set; on the spawn tool (`Agent`/`Task`) it reads the response's
  `agentId` and cost counters into `attrs`. No interpretation, no model — same
  discipline as `summary` and `status`.
- **The CLI merges `--attr` over the projection.** `hook` used to overwrite the
  observation's `attrs` with the `--attr` map; it now merges, so a user
  annotation cannot silently drop the spawn edge. `agent_id`/`agent_type` join
  the reserved-key set, and `emit` gains `--agent-id`/`--agent-type`.
- **A new relational invariant, I4:** `agent_id` present ⇒ `actor == "subagent"`.
  Instance and kind must agree. Like I3 it is a conformance test, not a schema
  rule.

## Deferred: per-event denormalized ancestry

Left explicitly undone: `parent_id`/depth on every line for `grep`-only subtree
queries. It is safe to defer because reconstruction is a superset — from the v2
log a reader can always *derive* a denormalized view, but a recorder made
stateful to populate `parent_id` cannot get its purity back.

**Reversal condition.** If raw-`grep` ancestry (no reader in the loop) becomes a
real need, add it as a **derived view** — a reader/`enrich` step that stamps
`parent_id`/depth onto a computed copy — not by teaching the recorder to hold
state. The day such a reader is added to this repo is also the trigger flagged
in [ADR 0001](0001-observation-verdict-as-separate-methods.md) to revisit the
type split.
