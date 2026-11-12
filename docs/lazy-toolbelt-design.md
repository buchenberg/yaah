# Lazy toolbelt design (yaah-6mi9.4)

Status: proposed design, input to yaah-6mi9.5. The conformance matrix
comes first, per the bead; the mechanism follows from it.

## The problem, in floor units

Every admitted tool ships its schema on every call. The measured per-schema
cost is ~780 tokens; the curated default belt (yaah-6mi9.1) carries 29
always-on names plus the gate-admitted lanes, and the floor sits at
~13.5K prompt-in tokens per call. Yet across every recorded bench family
the orchestrator's actual usage concentrates brutally: the 2026-11-12
five-task run called exactly four tools (bash 64, write 4, read 4, ls 2),
and the long-horizon and orchestration suites are the same shape. The
gated lanes (memory, Go toolchain, proposal, apply_out, wake pair) are
already lazy at the registry level since 6mi9.1/.2 — they cost nothing
when their gate is closed. The floor that remains is the always-on core:
names the model may want but almost never calls, each paying the
per-call tariff on every turn.

The sub-agent pattern already solved this UX: `list_subagents` advertises
capability cheaply, `delegate` instantiates it when wanted. `skill`
does the same for guidance (`action: load`). This design applies the
sub-agent UX to tools themselves: the belt advertises compact stubs for
mountable families; mounting a family registers it for the rest of the
session.

## Conformance matrix (tool class × admission row)

The acceptance dimensions against every tool class: surfaced by default,
mountable by the model, then each existing admission stage's interaction
with a mount. Evaluation order is `nameAdmission`'s (persona, then tool
level) at advertise/synthesize time, and the authorize gate axis at call
time. The mount flows through all of them — none is bypassed.

| Tool class (names) | Surfaced by default | Mountable | Persona list | Mode floor (read-only) | Ruleset / danger axis | Sub-agent flag |
|---|---|---|---|---|---|---|
| Always-on core (bash, read, write, edit, replace, delete, ls, grep, glob) | yes | no — it IS the floor | narrows as today (OriginPersona) | mutating members withheld at call time (OriginToolLevel/ModeFloor today) | per-call gate unchanged | dispatch registries from role profiles, unchanged |
| Orchestration spine (delegate, list_subagents, subagent_jobs, toolbelt) | yes | no — loading the loader is recursion | narrows as today | delegate allowed in read-only level as today | per-call gate unchanged | n/a — these are the parent surface |
| Rarely-called core (calculate, json_query, file_info, diff, bisect, patch, http, webfetch, plan, todowrite, role, skill, git, background_process, supervisor) | candidate: fold to stubs (the .5 cut decides the line) | yes, by family | mount still narrowed: a persona list is an allowlist, a mount cannot add a name it excludes (OriginPersona) | mutating members still withheld in read-only after mount | per-call gate unchanged; a mount adds a schema, never a permission | no propagation |
| Withheld file cluster (append, bulk_files, checksum, copy, copy_tree, find, mkdir, move, move_tree, rmdir, tail) | no (developer role owns them) | yes, by family — mount restores the direct path when delegation is overkill; sticky per session | same as above | mutating members withheld in read-only after mount | per-call gate unchanged (write-family danger rules apply as they do for write) | no propagation: sub-agents keep the role-profile belt |
| Gated lanes: memory cluster, Go quintet, apply_out, proposal | no — registration-gated (6mi9.1) | no — a mount must not open a gate: memory writers in one-shot, go tools outside a module, apply_out in local mode stay off; mount could only advertise work the runtime would refuse | narrow as today | floor unchanged | per-call gate unchanged | sub-agent registries carry their own |
| Wake pair (wait_for, watch) | no — session-shape gated (6mi9.2) | no in one-shot (nothing to wake); surfaced normally in interactive sessions | narrow as today | read-only level admits wait_for, withholds watch as today | per-call gate unchanged | sub-agents: not in role profiles today |
| MCP-served tools | yes at registration (config is the opt-in) | no — config owns admission; the model-mountable surface is the built-in registry | narrow as today (persona MCP omission warnings as today) | posture (OriginMCPPosture) unchanged | mcp_approval unchanged | untouched |
| Platform shell pair (bash/powershell) | one, by platform rule | no — platform is a fact, not a preference | narrows as today | withheld like other mutating tools in read-only | per-call gate unchanged | untouched |

Matrix rows that change nothing are the point: the mount is admission-
compatible with every existing stage. A mount that any stage refuses
answers with that stage's origin, in the stable snake_case vocabulary
(`persona`, `tool_level`, `mcp_posture`, `mode_floor`, `ruleset`,
`danger`) — the same words config and provenance already use.

## Stub format

The stub is prompt text, not JSON schemas — the entire point is not
shipping 780-token schemas. The belt guidance block grows a
family table, one line per mountable family:

```
## Mountable tool families

These tools are not loaded. Mount a family with the `toolbelt` tool
(action: mount) to load it for the rest of the session; mounting costs a
turn and widens every later call.

- file_ops: one-shot file moves/copies/listing (copy, move, mkdir, rmdir, tail, ...)
- analysis: checksum, diff, bisect, json_query, calculate, file_info
- net: http, webfetch
```

A line costs ~25 tokens against ~780 per schema: nine stubbed families
save ~7K/call at today's belt shape, more at the .5 cut. The stub names
the family, its purpose, and enough member names for the model to
recognize the need — the same economy `list_subagents` practices.

## Mount semantics

- One new tool, `toolbelt`, mirroring `skill`'s shape:
  `action: list | mount`, `family: <name>`. Registered always (with the
  spine), cost ~780 tokens as a single schema.
- Mount is `registry.Admit(names...)` + `Register` per member — the
  exact seam yaah-6mi9 built for MCP (wiring_mcp.go) and the wake gate:
  the belt stays the single enforcement point; a mounted tool is
  admitted through it, never alongside it.
- Sticky per session: the mounted set is session state. A mount is
  idempotent (remount = no-op with "already mounted"), and there is no
  unmount — the schema that already shipped cannot be unsent.
- The mount response lists what registered and, per matrix row, what
  did not and why (origin-named).  A read-only persona/mode mount of a
  mutating family therefore still mounts the family — admission is
  per call, not per schema — but this is a design decision for .5:
  the alternative (refuse the mount citing `mode_floor`) costs less
  noise in read-only sessions. The matrix records both readings; .5
  picks with a test either way.
- Mid-session switches: the per-loop seam (narrowToPersonaTools) is
  unchanged; a mounted name still narrows away under a persona that
  excludes it.

## Cost model

- Floor: the belt shrinks by the folded families (~780 tokens per
  folded schema per call), against ~25 tokens per stub line. At the
  current shape the projection is ~13.5K → ~10K/call; the .5 cut
  decides how far the always-on set folds.
- First use: one extra turn (the mount call) — one floor's worth of
  prompt-in, ~10K at the projected floor.
- Cache: the tools array is part of the cached prefix. A mount rewrites
  it once; every later call re-caches against the wider belt. The cost
  is one re-warm plus the family's schemas for the session's
  remainder — the bead's "one extra turn on first use, cache carries
  it after," made exact: mounting is prefix-destabilizing exactly
  once, exactly like the Go-module tool resolution already documented
  for the same reason (wiring_stages.go).
- Bench expectation: zero mounts across the five-task suite (usage
  evidence), so suite prompts fall by the folded schemas with no
  mount calls at all. The tripwire (yaah-6mi9.3) gets a new budget row
  only after .5 measures the new floor.

## Audit trail

- A mount appends `yaah.tool.mounted.v1` on the session's root scope
  (the `AppendFact` family the proposal settlement already uses):
  `{family, tools: [...], refused: {name: origin}}`.
- The per-turn `yaah.session.context.v1` records already carry the
  effective belt, so the diff between consecutive records is the
  mount's effect — the bead's requirement that the session.context
  belt diff become the audit trail, satisfied without a new record
  kind on the turn path.
- Startup recovery needs no new work: the mounted set is not
  persisted across sessions (sticky per session, not durable) — a
  resumed session re-advertises stubs. Durable mounts would be config's
  `agents.default.tools`, which already exists.

## What .5 implements, in order

1. Family map: name → member tool names, with the matrix row each
   family carries (three families to start, per the bead's "first gate
   set": `file_ops`, `analysis`, `net`).
2. The `toolbelt` tool (list/mount), registered beside the spine.
3. Mount = Admit + Register through the existing seam; refusal
   answers carry origins.
4. Stub guidance block (replaces/augments `ToolBeltGuidance`).
5. The `yaah.tool.mounted.v1` fact.
6. Tests: mount registers through the belt (registry-level boundary
   pattern), persona/mode floors survive a mount (nameAdmission
   unchanged), idempotence, one-shot tail shape unchanged, trace fact
   written, floor re-measured for the tripwire row.

## Open questions for the reviewer

1. Where is the always-on line? (The matrix's "core" row is
   deliberately conservative; the .5 cut can fold more.)
2. Does the file cluster becoming mountable soften 6mi9.1's
   delegate-only policy? Reading: no — curation kept it out of the
   *default*; mount makes it *on-demand*, which is the same bias with a
   direct path for cases delegation over-serves.
3. Read-only sessions: mount-then-per-call-withhold, or refuse the
   mount citing `mode_floor` up front?
4. Do sub-agents get the mount tool? Matrix says no (role profiles are
   authoritative); revisit if a dispatch pattern shows a need.
