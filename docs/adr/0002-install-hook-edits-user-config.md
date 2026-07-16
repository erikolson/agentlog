# ADR 0002 — install-hook edits the user's config, carefully

## Status

Accepted.

## Context

The recorder only records what it is wired to see, and wiring it means
hand-editing `settings.json`: find the file, get the nesting right, restart.
That is the whole adoption barrier for a tool whose entire value is being on
before you need it. A black box you meant to install is not a black box.

But the fix has a blast radius the rest of agentlog does not. `emit` and `hook`
only ever append to a file this project owns; `install-hook` writes to a file
the *user* owns, that other tools also write to, and that a broken write can
take a whole session down with. The interesting design here is not the JSON —
it is everything the command refuses to do.

## Decision

Add `install-hook`, bound by a six-point safety contract. Each point exists
because the obvious implementation gets it wrong.

1. **Merge, never clobber.** Read the existing file, preserve every key and
   every hook, add only the agentlog stanza. A missing file is created — that
   is not clobbering.
2. **Idempotent.** If any PostToolUse hook already runs `agentlog hook`, report
   it and exit 0 without touching the file. Running it twice must cost nothing,
   because people will.
3. **Append, don't replace.** An existing non-agentlog hook gets a sibling
   group, never an overwrite. We are a guest in this file.
4. **Refuse on malformed input.** If the file exists and does not parse, do not
   write. The only safe thing to do with JSON we cannot read is leave it alone;
   a "repair" would be a guess at what someone meant.
5. **Back up before modifying.** When a real change is about to happen, write
   `settings.json.bak` holding the exact prior bytes first. Undo is then one
   `mv`, and it does not depend on us having understood the file.
6. **`--dry-run`.** Print the result, write nothing. For a tool that edits
   config, show-before-write is the feature, not a nicety.

`install-hook` also refuses a file that parses but whose `hooks` or
`hooks.PostToolUse` holds an unexpected type. Such a file never reaches (4), and
merging into it would mean overwriting something we cannot interpret — the same
clobbering (1) forbids, arriving by a different road.

Targeting is explicit: `--global`, `--project`, or `--settings PATH`, and the
command errors if given none. A file-mutating command does not get a silent
default about *which file*.

## Alternatives

**Overwrite from a template.** Write our known-good `settings.json` over
whatever is there. Trivial to implement, and it destroys every unrelated setting
and every other tool's hooks. Rejected: clobbering.

**Require a manual edit.** Document the stanza and let people paste it. This is
the status quo, and it stays supported and documented — the manual path is the
honest baseline, and it is the only path for setups this command does not cover
(other harnesses, CI, config managers). But leaving it as the *only* path keeps
the adoption barrier that motivated this. Rejected as a sole option, kept as a
first-class one.

## Consequences

- Installing is one command, and safe to re-run, dry-run, and undo.
- **Formatting is normalized.** A merge round-trips through `encoding/json`, so
  the file comes back 2-space indented with keys reordered and comments — if any
  crept in — gone. Data and semantics are preserved; the user's exact byte
  layout is not. The `.bak` holds the original formatting, which is the answer
  for anyone who cares.
- **Symlinked config is followed, not replaced.** A `~/.claude/settings.json`
  that symlinks into a dotfiles repo stays a symlink, and the merge lands in the
  real target — usually what a dotfiles user wants. The `.bak` is written beside
  the link, not beside the target, so it falls outside that repo.
- **`doctor` is a report, not a gate.** It exits 0 with no hook installed,
  because "not wired" is a diagnosis, not a failure. It exits nonzero only when
  it cannot perform its checks at all. This mirrors the logger's own stance: it
  records, it does not block. A doctor that failed the build would be a gate,
  and gates live one layer up.
- `doctor` never writes, which is why it reads permission bits rather than
  probing with a temp file — an approximation it reports as a warning, not a
  verdict.

## Non-goal and reversal

**No `uninstall-hook` in v0.3.0.** Removal is deleting one stanza, and the
`.bak` already covers undo for the install that created it. Shipping an
uninstall would mean another config-mutating path to make safe for a job a text
editor does in ten seconds.

The reversal condition is demand: if people are removing hooks often enough to
ask, or if the stanza grows complex enough that hand-removal starts getting it
wrong, add `uninstall-hook` under the same six-point contract. Until then the
removal path is: edit the file, or `mv settings.json.bak settings.json`.
