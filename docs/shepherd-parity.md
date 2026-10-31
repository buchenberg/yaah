# Shepherd parity: dispatch worlds and framework scope

Status: in-repo ledger. `yaah-ihfh` (Shepherd parity closure) is closed;
the remaining useful Python-framework surface is owned by `yaah-hxk3`.
Mechanism port: `goatherd-kernel` + `goatherd-runtime`; reference: Shepherd
(Python) `v0.3.1`.

This page is the in-repo parity ledger and the design of record for
`yaah-ihfh.3`. It is deliberately **not** a claim of byte-for-byte Python SDK
parity: yaah adopts the execution, world, authority, and record mechanics that
fit an interactive coding agent, and explicitly declines the rest. The external
`yaah-shepherd-gap-report.md` is a historical snapshot; when the two disagree,
this page and the drift guard win.

## The four concerns

The reference separates four things, and every parity question is really a
question about which one is missing:

| Concern | Shepherd (Python) | yaah |
|---|---|---|
| Machine | `workspace.run` + consume-once settlement | `delegate` + `proposal` verbs |
| World | vcs-core carriers (overlayfs / FUSE / clonefile / copy) | git worktree; croft container |
| Authority | Landlock / Seatbelt over the agent process | Landlock / winacl / Seatbelt over child processes |
| Record | `shepherd2` abi.v0 | `goatherd-kernel` (byte-identical) |

The container tier is a **world**, never an authority. A container substitutes
for the jail; it does not compose with one.

## Scope and scorecard

Parity is assessed at two levels. The first is the execution-mechanics layer
yaah actually uses. The second is the wider Python framework surface; yaah does
not claim it wholesale.

| Layer | Shepherd (Python) | yaah | Status |
|---|---|---|---|
| Kernel / record | `shepherd2` `abi.v0` | `goatherd-kernel v0.18.0`, golden vectors | **Shipped, exact** |
| Retained runs / settlement | `workspace.run` + four settlement verbs | `delegate` + `proposal` verbs | **Shipped** |
| Dispatch worlds | `vcs-core` carriers | git worktree + croft container | **Shipped**; live container verification in `yaah-kit` |
| Authority | Landlock / Seatbelt | Landlock / winacl / Seatbelt | **Shipped** |
| Per-binding grants | `May[GitRepo, ReadOnly/ReadWrite]` | world-relative RW/RO config grants | **Shipped**, different surface |
| Egress postures | not modeled in the Python reference | `allow_all` / `deny_all` / `broker` | **Shipped, beyond reference** |
| Supervision | `JudgePredicate`, gates, permission plans | rule-based supervisor + blocking guard | **Partial**; `yaah-hxk3.1` |
| Task / step / combinators | `task`, `step`, gate/retry/parallel/race | own streaming loop + delegate | **Not ported**; decision `yaah-hxk3.5` |
| Contexts / effect fold | workspace, session, KV, DB, MCP, appstore | own state/memory/MCP + kernel effect bus | **Partial**; decision `yaah-hxk3.4` |
| Citation checking | Shepherd 0.3.1 citation checker | none | **Not ported**; `yaah-hxk3.2` |
| Export / transform | dedicated packages | none | **Declined** for the coding-agent scope |
| Devices / remote carriers | device and remote backends | git worktree + croft container only | **Declined** beyond current carriers |
| Provider SDK shape | Python provider protocol/adapters | yaah provider layer | **Different surface, not a parity target** |

The useful missing surface is scoped in `yaah-hxk3`; that epic owns the
adopt/decline decisions so this table does not become a wish list. Scorecard
maintenance and the decline register are themselves tracked as `yaah-hxk3.3`.

## Dispatch worlds

### Current lanes

| Session | World per dispatch | Authority | Settlement |
|---|---|---|---|
| local (default) | linked git worktree (`ForkIsolated`); report **and** proposal lanes may use a per-dispatch container under `world: auto` when `workspace.containerd.image` is set on Linux | Landlock / winacl around the dispatch's shell children; the container boundary for a container world | sealed proposal (worktree); GitDelta manifest (container proposal); none (report) |
| containerd | report **and** proposal lanes may use a per-dispatch sibling container (seeded from the session's captured state); a proposal settles into the session container | the container boundary | GitDelta manifest applied to the session container (proposals); none (reports) |

The reference's grain is one world per **run**. yaah's run is a dispatch, so
parity means one world per **dispatch**.

### Target

A dispatch resolves to a **dispatch world**: an isolated substrate seeded from
the repository at dispatch time. Two world kinds are legal:

- `worktree` — linked git worktree, the existing host lane.
- `container` — a per-dispatch container, sibling to (never nested inside) the
  session's world; seeded by the same bootstrap contract as the session
  container.

### Configuration

```yaml
agents:
  subagent:
    world: auto            # auto | worktree | container
    roles:
      reviewer:
        world: worktree   # per-role override; optional
```

- `auto` (default) — try `container`, step down to `worktree` when the
  container world is unavailable **and** the fallback still satisfies every
  declared requirement. Degradations are recorded and surfaced. This applies to
  report and proposal lanes alike; a container proposal is rehydrated from its
  durable artifact after a restart.
- `container` — fail closed. A dispatch that cannot get a container world does
  not run.
- `worktree` — never container. The existing behaviour, made explicit.

`world` is orthogonal to `mode` (`retained` / `shared`) and to `containment` /
`network`: `mode` says whether the dispatch is settled, `world` says where it
runs, `containment` / `network` say what bounds it. A `shared` dispatch has no
world at all, so `world` is recorded as `shared` and never resolved.

### The ladder

Resolution depends on the session world because the fallback must be reachable.

Host session:

1. per-dispatch container
2. per-dispatch worktree + syscall jail
3. fail (never "shared, unretained" unless explicitly configured)

Container session:

1. per-dispatch sibling container
2. the session container, unretained — only when retention was not required
3. fail

### Requirement-preserving degradation

A fallback is legal only if it still enforces everything the dispatch
declared. This generalises the reference's placement-honesty rule
(`placement="auto"` refuses to resolve to `advisory` when a binding is
`ReadOnly`).

| Declared | Fallback runs only if | Otherwise |
|---|---|---|
| `containment: auto` | the fallback has some enforcement | advisory, recorded |
| `containment: paddock` | the fallback is jailed, or is a container boundary (tier `contained` — a substitute, not a composition) | fail |
| `network: deny_all` / `broker` | the fallback can enforce it for shell children (Landlock ABI ≥ 4, or a container netns) and omits host-run `http`/`webfetch` | fail |
| `ReadOnly` grants | the fallback can enforce them (a jail, or a grant-shaped container) | fail |
| settlement (`mode: retained`) | the fallback is a retained world | fail, or explicitly opt out of retention |

Rule of thumb: **step down only along axes that were not required.**

### Preflight vs in-run

- **Preflight** failures (open, probe, create, bootstrap) step down the ladder,
  record the resolution, and warn.
- **In-run** failures (task died, exec broken, capture failed) fail the
  dispatch. The world is kept for inspection; the proposal is marked failed.
  A dispatch is never transparently re-run in a different world — the basis of
  its sealed output would change.

### Failure taxonomy

| Failure | Phase | Action |
|---|---|---|
| daemon unreachable / socket perms | preflight | one retry on transient; then step down or fail |
| image missing / contract probe fails | preflight | step down; report as configuration error |
| snapshotter / rootless / netns unsupported | preflight | step down if requirements allow, else fail |
| copy-in/bootstrap refuses (not a repo, subdirectory, `.git` file, tracked symlink, tracked denied file) | preflight | fail — there is no faithful world to fall back to |
| create / exec fails | in-run | fail the dispatch; keep the world |
| capture fails at seal | end | fail the seal; keep the world |
| teardown fails | end | warn; record the world for `yaah shepherd`; never block settlement |
| capacity exhausted | preflight | bound/queue, or step down with a warning |

### Observation

Three channels, same vocabulary as confinement:

1. one typed trace fact per dispatch:
   `yaah.subagent.world.v1 { requested, resolved, degraded, reason }`
2. a warning line in the proposal envelope when the resolution differs from the
   request — the envelope shape and the four verbs never change;
3. `yaah doctor` preflight, cached once per session (never per dispatch).

### Recovery

A crash mid-dispatch leaves a container and a snapshot. The world's identity
labels the container so `yaah shepherd containers` can list it and
`yaah shepherd prune-container <id>` can delete one exact id. Unlike a
worktree, a croft world is not addressable by path; the exact container id is
the handle.

### Cost

The economical path is a session-held base snapshot forked per dispatch. When
the session itself is not a container, each dispatch pays a copy-in. That
choice is part of the implementation, not a later optimisation: at
`max_concurrency: 12` it decides whether the lane is practical.

## Blockers and acceptance

### Container dispatch worlds — shipped

A host session can give a dispatch its own container world. The lane reuses the
`workspace.containerd` stanza (image, namespace, address, snapshotter,
`fifo_dir`) and the session container's copy-in contract: the repository is
copied in at dispatch time, the body runs with the sandbox workspace as its
substrate, and the scope and container are torn down afterwards. A report never
seals; a proposal seals a `GitDelta` manifest before teardown.

- Offered in a host session and in a container session (both lanes) on Linux
  with `workspace.containerd.image` set. A container-session sibling is seeded
  from the session container's captured state, not a stale host copy.
- `world: auto` prefers the container for both lanes; a **preflight** failure
  (open, probe, create, bootstrap) steps down only where the fallback still
  enforces the declaration: to a jail-capable worktree in a host session, or to
  the session container in a container session when it preserves the posture
  (no grants, no broker). Otherwise the dispatch fails rather than widening
  authority.
- `world: container` fails closed on the same failure — a dispatch is never
  transparently re-run in another world.
- The resolution is recorded as `yaah.subagent.world.v1`, and a degraded report
  carries the warning in its result. **In-run** failures still fail the dispatch.
- A proposal seals a `GitDelta` manifest before the container is destroyed. In
  a host session the manifest settles onto the host git tree and is rehydrated
  after a restart; in a container session it settles into the **session
  container** and is session-lifetime (see below).
- `agents.subagent.network` is lowered onto a per-dispatch container:
  `allow_all` shares the host network namespace, `deny_all` uses the
  container's unshared loopback-only namespace, and `broker` starts a
  netns-local proxy admitting `network_hosts`. Under `deny_all`/`broker`,
  `http`/`webfetch` are omitted because they execute in the yaah process
  outside the container; the shell children see only the container's netns.
- Grants are lowered by mount shaping: the container workspace is masked
  read-only and each read-write grant is remounted over its own subtree. A
  declared all-read-only set sets the explicit mask with no writable roots.
  croft's launch probe refuses if a write outside the grants succeeds or a
  write inside a grant fails.
- A host-session copy-in runs in an unshaped staging container, then the
  captured state is applied to the shaped container; the shaping init would
  otherwise reject the bootstrap writes to non-granted paths. The shaping init
  creates absent grant roots before masking the workspace, so the applied state
  can seed them even when they did not exist in the image.
- **Container-session proposals settle into the session container.** The host
  tree is untouched; `apply_out` remains the explicit host bridge. The
  manifest and basis are persisted for audit, but a session-target proposal is
  not rehydrated into a new session container (the parent world is gone).

### Retained container worlds — shipped as a change manifest

`croft-dlb` shipped (the pinned versions are in the ledger table below):
unchanged captures return the previous state verbatim, so the kernel's
digest-stability contract holds without a per-capture content diff. The
`sandbox/croft` adapter deliberately does **not** carry croft's
`ContentDigest`: a capture's digest must include `Revision`, and settlement
compares the host basis separately, so the field would not buy cross-timeline
content equality here. The underlying croft option remains available to a host
that wants it; yaah does not read it.

yaah's settlement path does not use a croft state across backends. In a host
session the parent root is a **git** sandbox while the dispatch world is croft,
so container proposals settle as a `GitDelta` change manifest (below) rather
than as a kernel state. The manifest is produced from the container world's own
change set (`Changes`/`ReadFile`), not from git plumbing. The container is
destroyed at seal; the manifest is the proposal, so `select`, `apply`,
`release`, and `discard` all work with no live world:

- `world: auto` uses a container for a proposal when `DispatchWorlds` is wired;
  a preflight failure degrades to the worktree (host session) or fails closed
  when there is no worktree (container session).
- report lanes use a container world under `auto`, because they never seal.
- host-target manifests, their host basis, and settlement markers survive a
  restart; `LoadProposalRegistry` rebuilds those proposals.
- session-target manifests are file-durable but **not** rehydrated after a
  restart: their parent session container is gone. The artifact is left for
  audit with a warning.

Acceptance for retained container dispatches:

1. ~~`Capture` is content-stable~~ — done (croft-dlb).
2. ~~A dispatch container can be settled onto the host~~ — done: `select`
   applies onto the dispatch basis, `apply` writes whole paths when disjoint,
   `release` exports the manifest, `discard` removes it.
3. ~~Post-seal recovery~~ — done: `.yaah/proposals/<id>/` is scanned at startup,
   scoped by durable session id; an `applying` marker quarantines an interrupted
   settlement, and a `settled` marker consumes the artifact instead of
   resurrecting it. A container orphaned **before** the seal is listed and
   pruned by `yaah shepherd containers` / `prune-container <id>` (`yaah-vmje`).
4. `yaah doctor` reports the resolution and the reason before the first run.
5. ~~Container-session proposals settle into the session container~~ — done:
   `select`/`apply` write through the session workspace, `select` requires the
   session digest to equal the dispatch basis, `apply` is path-disjoint since
   that basis, and the host tree is untouched until `apply_out`.

## Container settlement: `GitDelta`

A proposal from a container world cannot use the kernel's state-based
settlement: the parent root is a git sandbox and the dispatch world is croft,
so there is no shared state language to compare or merge. The cross-backend
artifact is a **change manifest**, not a text patch:

```
GitDelta = []Change{ Path, Kind(add|modify|delete), Mode, Content }
```

It is Shepherd's `Changeset`: whole-path post-images, produced from the
container's snapshot change set (`Changes`/`EffectChanges` plus `ReadFile`)
against the baseline state the bootstrap captures. No git runs inside the
container: the manifest is the same snapshot change set `apply_out` consumes,
with repository/proposal bookkeeping and the copy-in deny policy excluded, so a
container proposal works in an image that is not a repository. The `.git`/index
churn a git command causes is never mistaken for workspace content, and a denied
path created inside the container cannot overwrite its host counterpart. A text
patch is deliberately rejected — its context/fuzz and binary handling are the
fidelity traps; the manifest is exact, and applying it is a writer rather than a
patch parser.

### Semantics (reference-faithful)

- **Basis** — the container baseline state's revision: the captured baseline
  after a host copy-in, or the applied session state for a sibling container,
  before the body. The host's git state at dispatch is captured separately for
  the `select` precondition.
- **`select`** — capture the host state again; equal ⇒ apply the manifest
  (fast-forward). A moved host fails closed.
- **`apply`** — only when the manifest's paths are disjoint from the host's
  changes since the basis. Whole-path replacement/deletion; no 3-way, no hunk
  merge, no content synthesis.
- **`release`** — persist the manifest under the proposal's retention path and
  drop the world.
- **`discard`** — drop the manifest and the world.
- Consume-once, same envelope and verbs as the worktree lane.

### Lifecycle

The container is destroyed at seal: the proposal is the manifest, not a live
snapshot, so there is no pinned croft state, no lease, and no container to
recover after a successful seal. A container orphaned before the seal is listed
by `yaah shepherd containers` and pruned by exact id. The manifest is written
durably (`.yaah/proposals/<id>/`) at seal, which is what makes a crash-surviving
proposal possible without keeping the world alive. Total manifest size is
capped; exceeding it fails the seal with a clear message rather than truncating.

### Acceptance

1. A container proposal shows a diff and settles `select`/`apply`/`release`/
   `discard` onto the host tree.
2. `apply` refuses overlapping paths; `select` refuses a moved host.
3. The container is gone after seal; the manifest, host basis, and settlement
   markers survive on disk and rehydrate into the next session's registry.
4. Report lanes are unchanged.
5. Tests run without a daemon (sandbox fake + temp repos).

## Decision records

### Closed — apply-out mode fidelity (`yaah-vlwm`)

Copy-out deliberately keeps the sandbox file's `.Perm()`; container-authored
setuid/setgid/sticky bits never reach the host. Host-side tool writes keep
`atomicWriteFile`'s existing-mode behavior. This is the recorded mode-fidelity
decision, not a remaining parity gap.

### D6 — TaskControl is not adopted

The Python reference exposes Ring 2 `TaskControl` (`spawn`, `adopt`,
`abandon`, `publish`) as its programmatic execution surface. yaah keeps its
own streaming, provider-agnostic agent loop and its own `TaskRunner`; it does
not pretend the Go runtime's `task` package is the production driver. The
runtime primitives remain available and tested upstream, but wiring them into
the interactive session is a deliberate non-goal, not a missing implementation.

### D7 — materialize/substrate receipts are bypassed

Shepherd's vNext `materialize` orchestrator records substrate receipts and
witness-stamped dispatch. yaah instead routes every filesystem and process
operation through the `Workspace` interface and records typed workspace effects
where the backend supports them. The receipt protocol is not reproduced; the
authority and effect semantics it was protecting are enforced at the
`Workspace`/sandbox seam.

### D8 — the orchestrator is not a retained run (yaah-ihfh.4)

The top-level session stays unconfined on the host and is not retained. The
orchestrator is the driver, not a run: it holds the human interaction, the
approval decisions, and the settlement verdicts. Retaining it would require a
world around the thing that decides what to do with worlds, and would make the
settlement decision part of what gets settled.

This is the same split the reference makes between the Python driver and
`workspace.run(...)`. It is a decision, not debt.

### D9 — one world per dispatch (yaah-ihfh.3)

A dispatch is yaah's run. When containers are available, a proposal dispatch
gets its own container world; a report or shared dispatch gets one only where
it adds value (report: yes; shared: no, by definition). The session world is
never the dispatch world.

### D11 — container settlement is a change manifest (yaah-ihfh.3)

A container proposal settles as a `GitDelta` manifest (whole-path
post-images), not a text patch and not a croft state. The reasons: the parent
and child worlds do not share a state language; a manifest is exact where a
patch is contextual; path-disjoint whole-path application is the reference's
"no content synthesis" rule; and destroying the container at seal removes the
pinning/recovery problem entirely.

The manifest is the world's own change set (`Changes`/`ReadFile`), not a
second opinion computed by git. That is the reference's division of labour: the
world owns the diff; the machine consumes it. A container image does not have
to be a repository, and repository bookkeeping (`.git`, yaah's proposal trees)
is not proposable content.

### D12 — container-session proposals settle into the session (yaah-a7ks.1)

A container-session proposal targets the **session container**, not the host
git tree. The sibling is seeded from the session capture; the manifest applies
through the session workspace; `select` requires the session digest to equal
the dispatch basis and `apply` is path-disjoint since that basis. The host stays
untouched until `apply_out`. This artifact is session-lifetime: after the
session container is gone, the manifest is audit material, not a rehydratable
proposal. That is the deliberate weaker tier, not an accident.

### D10 — grants are world-relative (yaah-ihfh.2)

Authority is declared in config, never by tool registration: `agents.subagent.grants` (and per-role overrides) is a list of `{path, access}` roots relative
to the dispatch world. Roots must be disjoint — the jail's writable-root rules
are additive, so a read-only root nested in a read-write one would be silently
writable. Read-write roots lower to `ConfinementSpec.WritableRoots`; read-only
roots are declared but never listed, so writes under them are refused. An
advisory placement cannot enforce a restrictive set: it refuses rather than
granting the whole world.

Roots outside the dispatch world (the motivating case was a read-only parent
repo) are deliberately deferred: they would change the world seeding and
validator contract, and read-only git already works inside a worktree without
a grant.

Both worlds lower grants. The worktree lane lowers read-write roots to
`ConfinementSpec.WritableRoots`; the container lane masks the workspace
read-only and remounts each read-write root. Read-only roots are never listed as
writable, so writes under them are refused. A declared all-read-only set sets
`MaskWorkspace` (an empty root list alone would mean the historical unshaped
container). A host-session copy-in uses an unshaped staging container because
the shaped view cannot accept bootstrap writes; the captured state is applied
to the shaped container before the body starts.

## Review provenance

A worktree proposal records the runtime's explicit review pair before it is
settled: `goatherd.merge.proposed.v1` on the child owner path (changes, count,
baseline digest, diff digest) and `goatherd.supervisor.decision.v1` on the
parent owner path (`approved`, reason, action, child scope). The decision is
written when the sealed output settles, or as `seal_failed` if sealing fails
(so the proposal is not left without a terminal verdict); its action names
what happened (`selected`, `applied`, `discarded`, `released`, `seal_failed`).
This is the
settlement-shaped review the reference records; it does not adopt
`CommitMerge`, which only applies to shared-sandbox children and has been
removed upstream (`shepherd-runtime-go-s3u`).

Container proposals have no live child scope, so their review evidence is the
`yaah.proposal.sealed.v1` / `yaah.proposal.settled.v1` pair plus the manifest
and envelope; the runtime merge-proposal pair does not apply.

## Ledger

Pinned inputs (drift guard: `internal/parity`):

| Input | Pin |
|---|---|
| reference | Shepherd (Python) `v0.3.1` |
| trace ABI | `shepherd2` abi.v0, golden vectors |
| kernel | `goatherd-kernel v0.18.0` |
| runtime | `goatherd-runtime v0.21.0` |
| container world | `goatherd-runtime/sandbox/croft v0.2.1-0.20261031192038-da4e06c59318` / `goatherd-croft v0.9.2-0.20261031191238-a6d5f72a9b04` |
| containment | landlock v0.1.2, winacl v0.1.1, seatbelt v0.1.0 |
| grants | world-relative RW/RO roots, disjoint (config, not tools) |

Remaining useful framework work is owned by `yaah-hxk3`. Open
containment-adjacent work stays in `yaah-1tkp`: `yaah-kit` (live container
verification), `yaah-1tkp.1` (powershell read-only command policy), and
`yaah-bp4` (retire the gitsandbox decorator). `yaah-ihfh` is the closed
parity-closure epic and is historical; a divergence not named in an active
owner is a bug in this ledger.
