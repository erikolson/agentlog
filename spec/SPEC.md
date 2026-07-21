# agentlog event contract

**Contract version: 2**

This is the published contract for the agentlog service domain. It is the system
of record. The Go types, the CLI, and every downstream service (ratchet, session
hooks) *derive* from this document — when code and contract disagree, the code is
wrong. [`event.schema.json`](event.schema.json) is the machine-checkable form;
this file is the narrative.

A conforming producer emits one JSON object per line (JSONL). A conforming
consumer may rely on every guarantee stated here and nothing more.

## Design stance

The contract names **shape**, never a producer. It says `run_id`, `witness`,
`kind` — it never says "Claude Code" or "Anthropic" or any model. A projection
layer may be producer-aware (it maps a specific harness's payload onto this
shape); the contract itself stays producer-agnostic. This is Positive
Indifference at the domain boundary: any service that can emit the shape is
speaking agentlog, regardless of what generated the event.

## Core fields (every event)

| field    | type     | required | notes |
| -------- | -------- | -------- | ----- |
| `run_id` | string   | yes      | correlates all events of one run; non-empty |
| `seq`    | integer  | yes      | per-producer-process sequence; ordering across processes is by `ts` |
| `ts`     | string   | yes      | RFC 3339 / ISO 8601 UTC timestamp |
| `kind`   | string   | yes      | `observation` \| `verdict` |

## Kinds

`kind` selects a closed variant. A conforming event is exactly one.

### observation — something happened (advisory)

| field     | type    | notes |
| --------- | ------- | ----- |
| `stage`   | enum    | `collection` \| `llm_call` \| `tool_call` \| `safety` \| `delivery` |
| `actor`   | string  | the proposer that produced the event |
| `summary` | string  | enough to reproduce/verify — never the full payload |
| `dur_ms`  | integer | duration in milliseconds |
| `status`  | enum    | `success` \| `error` \| `timeout` \| `fallback` \| `ok` |

## Actor identity (either kind)

Both kinds may carry these; they qualify whoever produced the event.

| field        | type   | notes |
| ------------ | ------ | ----- |
| `agent_id`   | string | instance id of the acting agent; empty (absent) means the top-level agent |
| `agent_type` | string | type/name of the acting agent, e.g. `Explore`; empty for the top-level agent |

`actor` names the *kind* of proposer — `agent` for the top-level loop,
`subagent` for a nested one. `agent_id` and `agent_type` name the *instance* and
its type. They exist because agents fan out concurrently, so a single run's
stream interleaves several actors ordered only by `ts` (`seq` is per-process);
`agent_id` is the key that demultiplexes them.

The contract records **identity, not hierarchy**. It carries which agent
produced each event, but never a `parent_id` — the parent is not present in a
per-event payload, and stamping it would force the recorder to hold state it is
built not to hold. Instead, a **spawn event** (the tool call that launches a
subagent) records the `parent → child` edge as `attrs`, by convention
`spawned_agent_id` (plus `spawned_tokens`, `spawned_dur_ms`,
`spawned_tool_uses`). Every subagent has exactly one spawn, and each spawn event
is tagged with the spawner's own `agent_id`, so a reader reconstructs the whole
tree at any depth from the edge set. Tree structure is a consumer concern; see
[ADR 0004](../docs/adr/0004-agent-identity-as-core-fields.md).

### verdict — a gate adjudicated an artifact (binding)

| field         | type   | required | notes |
| ------------- | ------ | -------- | ----- |
| `gate`        | string | yes      | gate name |
| `verdict`     | enum   | yes      | `pass` \| `fail` \| `waived` \| `error` |
| `witness`     | string | yes      | content hash of the adjudicated artifact; non-empty |
| `adjudicator` | string | yes      | the ratifier |
| `enforce`     | enum   | yes      | `block` \| `warn` \| `record` |

`summary` may also appear on a verdict.

## Extension point

`attrs` is an object of string→string. It is the **only** place a domain adds
its own data. Domains annotate through `attrs`; they never add top-level fields
and never redefine core fields. `additionalProperties: false` enforces this —
an unknown top-level key is a contract violation, not a private extension.

## Invariants

Three tiers, and which layer enforces each — this is where JSON Schema's limits
become a feature rather than a gap:

- **I1 — required shape** (schema): core fields present; a verdict carries all
  five verdict fields; enums hold; no unknown top-level keys.
- **I2 — no illegal field mixing** (types): an observation cannot carry
  `witness`; a verdict cannot carry `stage`. Enforced in Layer 3 by distinct
  `Observation` and `Verdict` types: neither declares the other's variant-specific
  fields, so an illegal combination is not a value the code can construct. (They
  do share `actor`, `summary` and `attrs`, which I2 never separated.) (Standard JSON
  Schema can require fields per branch but not cleanly forbid the other
  branch's fields; the type system does it for free.)
- **I3 — relational** (conformance tests): `adjudicator != actor` when both are
  present (proposer ≠ ratifier). No cross-field inequality exists in draft
  2020-12, so this is a test, not a schema rule.

## Versioning (ossification discipline)

The contract is append-only within a major version. Adding a new **optional**
field or a new enum value that consumers may ignore is compatible. Removing a
field, retyping one, tightening `required`, or removing an enum value is
breaking and bumps the major version. `additionalProperties: false` means new
core fields are deliberately breaking — new *data* goes in `attrs`, which never
breaks anyone. Freeze is the default; a thaw (breaking change) is an explicit,
logged decision.

Pre-1.0, the freeze is looser: while nothing depends on this contract, a thaw
that gets the model right is worth the break. Each one is still logged here and
bumps the contract version. Once a real consumer ships, freeze hardens to the
default above.

**Thaw log**

- **v1 → v2** — added `agent_id` and `agent_type` (both optional, either kind).
  Breaking only because `additionalProperties: false` rejects unknown keys, so a
  v1 validator fails a v2 line. On the wire both are `omitempty`, so any event
  that names no subagent is byte-identical to v1. See
  [ADR 0004](../docs/adr/0004-agent-identity-as-core-fields.md).

## Non-goals

The contract describes one event. It says nothing about transport, storage,
rotation, retention, or query — those are consumer concerns. A file per day,
gzip, and grep are conventions, not contract.
