# Lazy toolbelt design (yaah-6mi9.4)

Status: reviewed and resolved (2026-11-12), input to yaah-6mi9.5. The
conformance matrix comes first, per the bead; the mechanism follows
from it. The #1587 review's four open questions resolved in
"Decisions" below.

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
| Working-set core (bash, read, write, edit, replace, delete, ls, grep, glob) | yes — one part of today's 29 always-on names; the .5 cut narrows the always-on core toward this row | no — it IS the floor | narrows as today (OriginPersona) | mutating members withheld at call time (OriginToolLevel/ModeFloor today) | per-call gate unchanged | dispatch registries from role profiles, unchanged |
| Orchestration spine (delegate, list_subagents, subagent_jobs, supervisor, toolbelt) | yes — supervisor rides the spine: it is the supervision surface, and a sub-agent run that cannot reach it is a safety question, not a token one | no — loading the loader is recursion | narrows as today | delegate allowed in read-only level as today | per-call gate unchanged | n/a — these are the parent surface |
| Rarely-called core — the analysis family (calculate, json_query, file_info, diff, bisect) and the net family (http, webfetch) fold in .5; patch, plan, todowrite, role, skill, git, background_process stay always-on (later gates may fold them, with tripwire evidence) | the .5 cut is decided: analysis and net fold to stubs (7 schemas); the rest stay always-on | yes, by family | mount still narrowed: a persona list is an allowlist, a mount cannot add a name it excludes (OriginPersona) | mutating members still withheld in read-only after mount | per-call gate unchanged; a mount adds a schema, never a permission | no propagation |
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

- file_ops: one-shot file moves/copies/listing (append, bulk_files, checksum, copy, copy_tree, find, mkdir, move, move_tree, rmdir, tail)
- analysis: calculate, json_query, file_info, diff, bisect
- net: http, webfetch
```

A line costs ~25–50 tokens against ~780 per schema, and every stub
names its full membership — an ellipsis on a five- or six-member
family buys nothing and hides names the model needs for recognition.
`toolbelt action: list` still exists as the authoritative membership
surface, but recognition must not depend on a second call. At the
decided .5 cut the arithmetic pins exactly: seven schemas fold
(analysis five, net two), ~5.5K off the floor, against three stub
lines (~0.1K) and the toolbelt schema (~0.8K) — net ~4.6K per call.
The file_ops line is pure stub cost: the cluster is already withheld
from the default (yaah-6mi9.1), so the line buys the restore path at
~0.05K and folds nothing. The stub names the family, its purpose, and
enough member names for the model to recognize the need — the same
economy `list_subagents` practices.

## Mount semantics

- One new tool, `toolbelt`, mirroring `skill`'s shape:
  `action: list | mount`, `family: <name>`. Registered always (with the
  spine), cost ~780 tokens as a single schema.
- Mount is `registry.Admit(name)` + `Register` per member — `Admit`
  takes one name (the variadic widening primitive is
  `substrate.Belt.Admit`). This is the seam yaah-6mi9 built for MCP
  (wiring_mcp.go admits before every Register precisely because
  curation must not silently drop a configured server); mount is a
  second user of that seam, widening admission when the model asks
  instead of when a server starts. It is *not* the wake gate's seam:
  registerWakeTools registers directly — its names are belt-admitted
  already, and its gate is registration-time session shape, not
  admission. Either way the belt stays the single enforcement point: a
  mounted tool is admitted through it, never alongside it.
- Sticky per session: the mounted set is session state. A mount is
  idempotent (remount = no-op with "already mounted"), and there is no
  unmount — the schema that already shipped cannot be unsent.
- The mount response lists what registered and, per matrix row, what
  did not and why (origin-named). A mount admits its members
  individually through nameAdmission, and the family mount is atomic
  against that result: if every member is refused, nothing registers
  and the mount fails citing the refusing origin. So a read-only
  session mounting a wholly mutating family fails with `mode_floor`
  (nothing mounted — the failed mount answer is that turn's whole
  cost), while the same session
  mounting `analysis` succeeds — every member is a legal read-only
  call. The invariant a success carries: at least one callable
  member. The per-call floors are untouched — a mounted mutating name
  is still withheld at call time in read-only; what the atomic rule
  fails is only the mount that could have mounted nothing. The flag
  costs nothing in the per-member loop the mount already is.
- A direct call to a stub-named but unmounted tool answers
  instructively — `not mounted: mount the file_ops family with the
  toolbelt tool` — not with the generic unknown-tool refusal. The
  stub table created the recognition; the error path must not waste
  it. The dead end recovers in one turn, which is the miscall's only
  cost.
- Mid-session switches: the per-loop seam (narrowToPersonaTools) is
  unchanged; a mounted name still narrows away under a persona that
  excludes it.

## Cost model

- Floor: seven schemas fold at the decided cut (analysis five, net
  two), ~780 tokens each — ~5.5K per call — against three stub lines
  (~0.1K) and the toolbelt schema (~0.8K): net ~4.6K, projecting
  ~13.5K → ~8.9K/call. The .5 measurement pins it against the
  tripwire (yaah-6mi9.3) rather than trusting the projection.
- First use: one extra turn (the mount call) — one floor's worth of
  prompt-in, ~8.9K at the projected floor, plus the one prefix
  destabilization the cache item below makes exact.
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

1. Family map: name → full member tool names, with the matrix row
   each family carries. Three families to start, per the bead's
   "first gate set": `file_ops` (11 members — checksum here only),
   `analysis` (5), `net` (2).
2. The `toolbelt` tool (list/mount), registered beside the spine.
3. Mount = per-member `registry.Admit(name)` + `Register` through the
   MCP seam; the family mount is atomic (a refusal of every member
   mounts nothing and answers with the refusing origin); refusal
   answers carry origins.
4. Stub guidance block (replaces/augments `ToolBeltGuidance`), full
   membership per line — no ellipses.
5. The instructive not-mounted error for direct calls to stub-named
   tools.
6. The `yaah.tool.mounted.v1` fact.
7. Tests: mount registers through the belt (registry-level boundary
   pattern), persona/mode floors survive a mount (nameAdmission
   unchanged), atomic family failure in read-only, the instructive
   not-mounted error, idempotence, one-shot tail shape unchanged,
   trace fact written, floor re-measured for the tripwire row.

## Decisions (2026-11-12)

The four questions the #1587 review left open, resolved:

1. **Always-on cut line.** Fold a name when it is outside the observed
   working set, its absence costs one turn (not an irreversible act
   or a protocol break), and it clusters with coherent siblings. So
   .5 folds exactly the bead's three families: analysis and net off
   the always-on core, file_ops as the restore path (folds nothing —
   .1 already withheld it). The working set, the spine (supervisor
   included), and the loaders (skill, role — folding a loader behind
   toolbelt is the recursion the spine row forbids) stay always-on.
   Deeper cuts (git, background_process, plan) argue under the
   tripwire with usage evidence, not speculatively.
2. **Does mountability soften 6mi9.1's delegate-only reading? No.**
   Curation's decision was "out of the default belt," and mount
   preserves it — the cluster still ships zero schemas by default,
   exactly as .1 left it. Mount adds the direct path for the cases
   delegation over-serves (a one-line `mkdir` paid a whole sub-agent
   registry spin-up), and `yaah.tool.mounted.v1` keeps the widening
   audited, preserving the provenance story .1 cared about.
3. **Read-only sessions: the atomic per-member rule** (Mount
   semantics above) — mount admits per member through nameAdmission;
   a family whose every member is refused fails citing the refusing
   origin. Real mounts stay useful in read-only (analysis is fully
   non-mutating); dead mounts spend only the turn that called them —
   no schema is added, so no prefix re-warm, and no "success" that
   silently withholds everything.
4. **Sub-agents get the mount tool? No.** The dispatch guarantee is
   that a sub-agent's surface is exactly its role profile;
   self-widening breaks it in both directions: the parent can no
   longer reason about the child's reachable capability set when
   framing the task, and "which belt did this" gains a moving
   answer. A dispatch pattern that needs a family is a role profile
   change in config — the authoritative, reviewable mechanism. The
   matrix's revisit clause stands: only a demonstrated dispatch
   pattern reopens it.
