# Shepherd parity: dispatch worlds

Status: design + ledger. Applies to `yaah-ihfh` (EPIC: Shepherd parity
closure). Mechanism port: `goatherd-kernel` + `goatherd-runtime`; reference:
Shepherd (Python) `v0.3.1`.

This page is the in-repo parity ledger and the design of record for
`yaah-ihfh.3`. The external `yaah-shepherd-gap-report.md` is a snapshot; when
the two disagree, this page and the drift guard win.

## The four concerns

The reference separates four things, and every parity question is really a
question about which one is missing:

| Concern | Shepherd (Python) | yaah |
|---|---|---|
| Machine | `workspace.run` + consume-once settlement | `delegate` + `proposal` verbs |
| World | vcs-core carriers (overlayfs / FUSE / clonefile / copy) | git worktree; croft container |
| Authority | Landlock / Seatbelt over the agent process | Landlock / winacl over child processes (macOS Seatbelt pending) |
| Record | `shepherd2` abi.v0 | `goatherd-kernel` (byte-identical) |

The container tier is a **world**, never an authority. A container substitutes
for the jail; it does not compose with one.

## Dispatch worlds

### Current lanes

| Session | World per dispatch | Authority | Settlement |
|---|---|---|---|
| local (default) | linked git worktree (`ForkIsolated`); report lanes may use a per-dispatch container when `workspace.containerd.image` is set on Linux | Landlock / winacl around the dispatch's shell children; the container boundary for a container world | sealed proposal (worktree); none (report) |
| containerd | the session's one container (shared) | the container boundary | none (`Retain` off) |

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
  declared requirement. Degradations are recorded and surfaced.
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
| `containment: paddock` | the fallback is actually jailed | fail |
| `network: deny_all` / `broker` | the fallback can enforce it (Landlock ABI ≥ 4, or netns) | fail |
| `ReadOnly` grants | the fallback can enforce them | fail |
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
| copy-in/bootstrap refuses (not a repo, `.git` file, no commits) | preflight | fail — there is no faithful world to fall back to |
| create / exec fails | in-run | fail the dispatch; keep the world |
| capture fails at seal | end | fail the seal; keep the world |
| teardown fails | end | warn; record the world for `yaah shepherd`; never block settlement |
| capacity exhausted | preflight | bound/queue, or step down with a warning |

### Observation

Three channels, same vocabulary as confinement:

1. one typed trace fact per dispatch:
   `yaah.subagent.world.v1 { requested, resolved, backend, reason, enforced }`
2. a warning line in the proposal envelope when the resolution differs from the
   request — the envelope shape and the four verbs never change;
3. `yaah doctor` preflight, cached once per session (never per dispatch).

### Recovery

A crash mid-dispatch leaves a container and a snapshot. The world's identity
(snapshot/lease name) is recorded with the scope so `yaah shepherd` can list
and prune it. Unlike a worktree, a croft world is not addressable by path.

### Cost

The economical path is a session-held base snapshot forked per dispatch. When
the session itself is not a container, each dispatch pays a copy-in. That
choice is part of the implementation, not a later optimisation: at
`max_concurrency: 12` it decides whether the lane is practical.

## Blockers and acceptance

### Report-lane slice — shipped

A host session can now give a **report** dispatch its own container world. The
lane reuses the `workspace.containerd` stanza (image, namespace, address,
snapshotter, `fifo_dir`) and the session container's copy-in contract: the
repository is copied in at dispatch time, the body runs with the sandbox
workspace as its substrate, and the scope and container are torn down
afterwards. Nothing is sealed, so croft's sequence-keyed capture cannot reach a
settlement.

- Offered only in a host session (no session container) on Linux with
  `workspace.containerd.image` set.
- `world: auto` prefers the container for report lanes; a **preflight** failure
  (open, probe, create, bootstrap) steps down to the worktree with
  `Degraded=true` and a reason naming the failure.
- `world: container` fails closed on the same failure — a dispatch is never
  transparently re-run in another world.
- The resolution is recorded as `yaah.subagent.world.v1`, and a degraded report
  carries the warning in its result. **In-run** failures still fail the dispatch.
- Proposal lanes are unchanged: `world: container` refuses and `auto` degrades
  to the worktree, because a container cannot seal until `croft-dlb`.

### Retained container worlds — gated on `croft-dlb`

`croft-dlb` is the gate for **retained** container worlds. `Capture` currently
returns a sequence-keyed revision (`croft-seq-N`), so identical content
captures differently and settlement's content-equality checks cannot hold.

Until it lands:

- `world: auto` resolves `container` → `worktree` for a **proposal** dispatch,
  with a recorded warning;
- `world: container` fails closed for a proposal dispatch;
- report lanes already use a container world (above), because they never seal.

Acceptance for flipping `container` to available for **retained** dispatches:

1. `Capture` is content-stable (identical content ⇒ identical digest).
2. A dispatch container can be seeded, sealed, and settled `select` / `apply`.
3. A crash mid-dispatch is recoverable or prunable by `yaah shepherd`.
4. `yaah doctor` reports the resolution and the reason before the first run.

## Decision records

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

## Ledger

Pinned inputs (drift guard: `internal/parity`):

| Input | Pin |
|---|---|
| reference | Shepherd (Python) `v0.3.1` |
| trace ABI | `shepherd2` abi.v0, golden vectors |
| kernel | `goatherd-kernel v0.18.0` |
| runtime | `goatherd-runtime v0.20.0` |
| container world | `goatherd-runtime/sandbox/croft v0.1.3` / `goatherd-croft v0.8.0` |
| containment | landlock, winacl; seatbelt pending upstream |
| grants | world-relative RW/RO roots, disjoint (config, not tools) |

Remaining divergences are tracked only in `yaah-ihfh` and its children. A
divergence not named there is a bug in this ledger.
