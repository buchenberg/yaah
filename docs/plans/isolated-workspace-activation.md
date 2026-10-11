# Development Plan: Isolated Workspace Activation (yaah side)

**Status:** decisions settled 2026-10-05; kernel dependencies refreshed 2026-10-10 (core v0.4.1 → v0.10.0) · **Author:** buchenberg
**Kernel dependency:** `shepherd-kernel-go` (see §7 for the exact release requirement)
**Related:** `supervised-task-plan.md` (the supervision/rollback substrate this sits on),
`.agents/plans/best-of-breed-gap-backlog/plan.md` (G13/G14 — worktree isolation, done)

---

## 1. Why this plan exists

`yaah` has a complete, tested `Workspace` implementation that runs every tool
inside a `shepherd.Sandbox` instead of on the host. It is not wired to anything.

> `// Wiring status: nothing selects this in production yet.` …
> `// Selecting a containerd backend is the remaining step; until it lands, treat isolation as`
> `// plumbed but not active.`
> — `internal/tools/workspace_sandbox.go:24-29`

That comment is accurate as to *symptom* and misleading as to *cause*. The
remaining step is **not** "select a backend". A containerd `Sandbox` alone would
still fail to activate, because activation is refused by a policy check that a
default registry can never satisfy. This plan states the real blocker (§2),
**records the decisions it forces (§3, §10)**, and sequences the work
(§4–§6, §11). All decisions are settled; nothing here is awaiting a choice.

**What already exists and works** (verified by reading, not inferred):

| Component | Location | State |
|---|---|---|
| `Workspace` interface (10 methods) | `internal/tools/workspace.go:105-115` | Complete |
| `sandboxWorkspace` — full in-band impl | `internal/tools/workspace_sandbox.go` (348 lines) | Complete, test-only |
| `localWorkspace` — the host default | `internal/tools/workspace.go:116` | In production |
| `FilesystemTool` / `HostOnlyTool` markers | `internal/tools/filesystem_tools.go` (55 lines) | Complete |
| Migration diagnostics | `tools.go:314-344` (`UnmigratedFilesystemTools`, `HostOnlyFilesystemTools`) | Complete |
| Isolation refusal | `tools.go:355-371` (`Registry.SetWorkspace`) | Complete, unconditional |
| Test coverage of the sandbox path | `internal/tools/workspace_test.go` (871 lines) | Substantial |

`UnmigratedFilesystemTools()` returns **empty** today: all 21 filesystem tools
carry both `filesystemTool()` and a `SetWorkspace` setter. The migration debt
the refusal was built to track is paid off.

---

## 2. The real blocker, precisely

`Registry.SetWorkspace` refuses any non-local workspace when *either* list is
non-empty:

```go
if ws != nil && !ws.Local() {
    unmigrated := r.UnmigratedFilesystemTools()
    hostOnly  := r.HostOnlyFilesystemTools()
    if len(unmigrated) > 0 || len(hostOnly) > 0 { /* refuse */ }
}
```

`HostOnlyFilesystemTools()` is non-empty on **any production registry**, because
four tools are host-bound by design and are always registered:

| Tool | Why host-only (`filesystem_tools.go:37-51`) |
|---|---|
| `role` | manages sub-agent role files — harness config the host reads, not workspace content |
| `background_process` | drives the in-memory host process manager; a container needs its own |
| `supervised_task` | provisions sandboxes, so it cannot run inside the isolation it creates |
| `go_refactor` | reads the FS through `golang.org/x/tools` (`imports.Process`, `packages.Load`) — a path `Workspace` cannot intercept |

So on a default registry, `SetWorkspace(newSandboxWorkspace(sb, "/workspace"))`
returns:

```
cannot select an isolated workspace: 4 host-only tool(s) that cannot be isolated
[background_process, go_refactor, role, supervised_task]
```

**This is a policy problem, not a backend problem.** No amount of containerd
work changes it. It has to be decided, and the decision is load-bearing enough
that the current refusal is arguably correct behaviour — see §3.

There is a *second*, independent gap: **there is no configuration surface for
backend selection at all.** `internal/config` has no workspace keys; the
workspace is derived from the path validator:

```go
func (r *Registry) SetPathValidator(pv *PathValidator) {
    r.Workspace = newLocalWorkspace(pv)   // tools.go:262 — always local
}
```

and the two production call sites both take that path:

- `cmd/yaah/wiring.go:103` — `toolReg.SetPathValidator(pathValidator)`
- `internal/agent/runner/runner.go:581` — sub-agent registries, same call

`newSandboxWorkspace` has **zero non-test callers**.

---

## 3. Host-only tool policy — DECIDED (Option A)

The refusal exists because *"Isolating only some tools would be worse than not
isolating at all, because the caller would believe the whole run was contained"*
(`tools.go:348-353`). That reasoning is sound and should not be silently
discarded. Three ways forward:

### Option A — Curated registry (recommended)

Build the isolated session's registry from `leafTools` + the runtime-wired tools
**minus** the four host-only tools, then `SetWorkspace` succeeds because both
diagnostic lists are empty. `NewEmptyRegistry()` + `NewLeafTool(name)` already
exist for exactly this pattern (`tools.go:212-224`) and are used by role profiles.

- **For:** smallest change; preserves the refusal's meaning (an isolated registry
  genuinely contains no host-bound tool); reuses existing machinery; role
  profiles already prove the pattern.
- **Against:** the isolated session has a *smaller* tool surface than the local
  one. The model must be told, or it will try `background_process` and get
  "tool not found" with no explanation. Requires a system-prompt change.
- **Sub-decision:** is `go_refactor` worth reimplementing on in-sandbox
  `gofmt`/`goimports` calls? `filesystem_tools.go:48-51` notes that is the only
  honest route. Recommend **deferring** — it is a Go-specific nicety, and
  `go_outline`/`go_test`/`go_mod` are already migrated.

### Option B — Disable instead of refuse

Change `SetWorkspace` to *unregister* host-only tools for isolated workspaces
rather than returning an error, keeping the refusal only for genuinely
unmigrated tools.

- **For:** activation becomes a one-line wiring change; the tool surface adapts
  automatically.
- **Against:** mutating the registry on workspace selection makes availability
  depend on call order, and it converts a loud, diagnosable refusal into a
  quiet surface change. It also weakens the invariant that made the refusal
  trustworthy.
- **Verdict:** reject unless Option A proves impractical.

### Option C — Keep the refusal; isolation stays impossible

Document that `yaah` does not support isolated execution, and remove or clearly
mark `sandboxWorkspace` as an unused extension point.

- **For:** zero risk; honest.
- **Against:** forfeits the main safety story the kernel dependency was adopted
  for, and contradicts `docs/architecture.md`'s framing of the workspace layer.
- **Verdict:** only if A and B both fail.

**DECIDED — 2026-10-05: Option A.** The isolated session is built from a curated
registry: `NewEmptyRegistry()` + `NewLeafTool(name)` for the migrated set + the
runtime-wired tools that are **not** host-only. `role`, `background_process`,
`supervised_task` and `go_refactor` are omitted, and `SetWorkspace` then succeeds
because both diagnostic lists are empty.

The tool-surface delta **must be surfaced to the model** (§2.2) as an explicit,
named limitation rather than left to be discovered — that is the condition that
makes Option A acceptable rather than merely convenient.

**Sub-decision: `go_refactor` is not reimplemented for isolated mode.** The only
honest route is reimplementing it on in-sandbox `gofmt`/`goimports` calls
(`filesystem_tools.go:48-51`), which is not worth it: `go_outline`, `go_test` and
`go_mod` are already migrated and cover the Go tooling need. Recorded as a
non-goal (§9).

Options B and C are rejected as written: B would trade a loud, diagnosable
refusal for a quiet surface change; C forfeits the safety story the kernel
dependency exists for.

---

## 4. Phase 0 — Prerequisites and unblockers

Nothing below can be validated until these land. Both are kernel-side.

### 0.1 Pin a kernel release that contains the Phase 0 bug batch

**Verified problem.** `v0.4.0` points at `0c62788` (merge of `feat/containerd`,
2026-09-16) and does **not** contain the bug batch:

```
$ git merge-base --is-ancestor 4203993 v0.4.0   # → exit 1 (NOT contained)
$ git tag --contains 4203993                     # → sandbox/containerd/v0.1.0 only
core tags containing the bug batch: NONE
```

The bug batch (`4203993`) touched `sandbox_git.go`, `checkpoint.go`, `scope.go`,
`store.go`, `supervisor.go`, `types.go` and both containerd files. HEAD is 11
commits past `v0.4.0` and unreleased.

**DECIDED — 2026-10-05: the kernel cuts `v0.4.1`** at/after HEAD (kernel plan 00
**T0.8**), and `yaah` pins that tag. Pinning a pseudo-version is worse for
consumers than a tag, and the bug batch is exactly what a patch release is for.
The nested module's repin is part of T0.8.

> Note: the nested module pinned `github.com/buchenberg/shepherd-kernel-go v0.4.0`
> while its own tree contained the bug batch, so `sandbox/containerd@v0.1.0`
> depended on a core version older than its own fixes.
>
> **Resolved, then overtaken — resolved.** `v0.1.2` shipped (kernel
> T0.8b), the nested module advanced through `v0.1.4` (durable workspace
> states; cold-namespace live suite 9/9 green), and
> **`sandbox/containerd/v0.1.5` is tagged (kernel PR #33, merged at
> `974bc56`)**: repinned to core `v0.10.0`, implementing `DeltaApplier`
> (live-verified 11/11, including merge-onto-moved-parent and
> conflict-refuses-untouched), and self-registering with the sandbox
> registry on import. The step-5 blocker is cleared.

### 0.2 Establish that the containerd backend actually works — ✅ GATE CLEARED

**Result (2026-10-07): the gate fired, the cause was found, and the fix is
verified.** Run against containerd v2.3.5 (overlayfs snapshotter).

**First pass — the harness found nine defects** (kernel `603fdcf`). Then the soak
**failed 3 of 5 runs, with a different test failing each time** — the signature of
a shared-resource race, not per-test bugs.

**Cause:** the adapter held **no containerd lease**, and this daemon's GC is
aggressive (`mutation_threshold = 100`, `schedule_delay = '0s'`,
`startup_delay = '100ms'`). `Capture` stops the task and then prepares a successor
whose parent is the just-committed snapshot; with the task stopped that snapshot is
**unreferenced**, so the GC could reap it in that window:

```
Capture: containerd sandbox: prepare successor snapshot:
  parent snapshot shepherd/<id>/committed/1 does not exist: not found
Destroy during cleanup: remove snapshot shepherd/<id>/active/0: ... does not exist
```

**Fixed** by kernel **T0.9** (`8e9b867`): the sandbox now holds a labelled lease for
its lifetime, released on `Destroy`. **Re-soak: 12 of 12 consecutive green runs**,
with zero leaked containers, tasks, or snapshots. The live suite now asserts the
mechanism directly rather than relying on a green run —
`TestLive_LeaseHoldsSnapshots` proves the lease lists the sandbox's own snapshot
keys as resources, and `TestLive_DestroyReleasesTheLease` proves it is dropped.

**Consequence for this plan: step 2's stop-gate is satisfied, and Phases 1–3 are
unblocked.** This is worth emphasising because the gate very nearly passed by
accident: the first run was green, and only a repeated soak exposed the race. A
single smoke test — which is what the kernel plan originally called for — would
have declared victory on a backend that loses its workspace roughly 60% of the
time. **Prefer a soak to a smoke for anything that gates on a daemon.**

The blast radius was bounded: the runs leaked **no** containers, tasks, or
sandbox-owned snapshots — `Destroy` cleaned up correctly even when it also
reported a missing-snapshot error.

### 0.3 Promote the existing `fakeSandbox` into a reusable in-memory `Sandbox`

A test double already exists — `fakeSandbox` at `internal/tools/workspace_test.go:369-391`,
registered through `newSandboxWS` (`:422`) and used by the two `SetWorkspace`
tests (`:250`, `:271`). So this is **not** greenfield work; the question is
whether it is adequate for the activation path.

It currently is not, in two respects worth noting:

- `Capabilities()` returns an **empty `SandboxCapabilities{}`** (`:382-383`), so
  every capability including `FileIO` and `Exec` reads false. Any
  capability-gated logic on the activation path would take the "unsupported"
  branch under test, which is the opposite of what activation tests need.
- All methods return `nil`/zero values and merely record calls (`:381-391`), so
  there is no simulated filesystem to round-trip a write against.

**Action:** extend it into a reusable in-memory filesystem fake — a map-backed
tree honouring `ReadFile`/`WriteFile`/`Exec` for the small shell surface
`sandboxWorkspace` actually uses (`stat -c`, `find -printf`, `mktemp`/`mv`,
`rm`, `mkdir`) — and move it out of `_test.go` into an exported test-support
package if the activation path's tests live elsewhere. Report realistic
`Capabilities()` (at minimum `FileIO`, `Exec`, `Lifecycle`) so the gated paths
are exercised.

This is the single highest-leverage test asset for the whole plan: it makes the
config → construction → `SetWorkspace` → tool-dispatch path testable on any OS
with no containerd dependency, which in turn means most of Phases 1–2 can land
before §0.2's live-daemon gate clears.

**Exit criteria:** a tagged kernel release ≥ the bug batch; a recorded
live-daemon smoke result; a reusable in-memory `Sandbox` with realistic
capabilities.

---

## 5. Phase 1 — Backend selection and lifecycle

### 1.1 Config surface

**Status: landed (the config keys, validation, and D5's probe; wiring is
step 5).** `internal/config/workspace.go` holds the stanza and its
normalization (`workspace.*` top-level key, backend/root/containerd.{address,
namespace, image, snapshotter}, `${VAR}` substitution on the address);
`config.Validate` enforces the backend enum, the Linux-only rule for
`containerd` (surfaced at load, not at first tool call), the image
requirement, and the absolute-non-`/` root. `internal/tools/workspace_probe.go`
carries `OpenContainerdWorkspaceSandbox` (registry resolution — both backends
self-register, no host factory) and `ProbeImageContract`, the D5 startup
probe: one shell pass over sh/stat/find/git/rg/go plus the GNU `stat -c` /
`find -printf` checks, with the probe owning its sandbox's full lifecycle.
The proposed shape, as landed:

```yaml
workspace:
  backend: local          # local | containerd   (default: local)
  root: /workspace        # containment root inside the sandbox
  containerd:
    address: ""           # e.g. /run/containerd/containerd.sock
    namespace: default
    image: ""             # must contain go, git, rg, sh, find(1), stat(1)
    snapshotter: ""       # default: containerd's configured snapshotter
```

Validation obligations:

- `backend: containerd` on Windows / non-Linux must fail at load with a clear
  message, not at first tool call. `sandboxWorkspace` is POSIX by construction
  (`Shell() → ("sh","-c")`, `Join` uses `path.Join`) — `workspace_sandbox.go:79-86`.
- `root` must be absolute and non-empty; `newSandboxWorkspace` already guards the
  `"/"`-collapse case (`:43-48`) — keep that, and surface it as a config error
  rather than a silent whole-container scope.
- Image requirements are real: `Stat` uses `stat -c`, `ReadDir` uses
  `find -printf` (both GNU), and tools need `git`, `rg`, and a Go toolchain.
  Validate by probing the image at startup, or document the contract and fail
  loudly on first use.

### 1.2 Sandbox construction and lifecycle

- Construct the `Sandbox` once per session (not per tool call).
- `Create` before the first tool dispatch; `Destroy` on session teardown and on
  every error path. A leaked container is the most likely operational bug here.
- Respect the kernel's documented contract that `Destroy` must never destroy
  resources it did not create (`shepherd-kernel-go/sandbox.go:143-146`).
- Wire through `cmd/yaah/wiring.go` **in place of** the unconditional
  `SetPathValidator` at `:103` when the backend is isolated. Both must not run:
  `SetPathValidator` overwrites `r.Workspace` with `newLocalWorkspace(pv)`
  (`tools.go:262`).

### 1.3 Sub-agent path

`internal/agent/runner/runner.go:581` calls `SetPathValidator(pv)` per
sub-agent. Decide and document:

- Does an isolated session's sub-agents inherit the sandbox, a *new* sandbox, or
  fall back to a host worktree?
- `runner.go:510-529` (`subAgentSandbox`) already chooses `WorktreeSandbox` vs
  `LocalGitSandbox` vs `nil`. Extending it to choose a sandbox-backed workspace
  is the natural seam.
- Note the existing rationale at `runner.go:349-355`: the turn checkpointer lives
  on the sub-agent's own scope so rewinds never touch the supervised tool's
  attempt-level checkpoints. Any sandbox-per-sub-agent decision must not break it.

**Exit criteria:** `workspace.backend: containerd` produces a functioning
isolated session on Linux; `local` is byte-for-byte unchanged; no container
leaks across clean and error-path teardown.

---

## 6. Phase 2 — Tool surface, bootstrap, activation

### 2.1 Implement the §3 decision

Apply Option A: build the isolated registry from `NewEmptyRegistry()` +
`NewLeafTool(name)` for the migrated set + the runtime-wired tools that are not
host-only, omitting `role`, `background_process`, `supervised_task`,
`go_refactor`.

Then assert the invariant at startup rather than trusting it:

```go
if err := reg.SetWorkspace(sandboxWS); err != nil { /* fail the session loudly */ }
```

`SetWorkspace` is already the enforcement point. Do not bypass it.

### 2.2 Tell the model what is missing

The system-prompt assembly (`cmd/yaah/wiring_prompt.go`) must state, in isolated
mode, which tools are unavailable and why. Without this the model attempts
`background_process` and receives an unexplained "tool not found" — the failure
mode Option A is most exposed to.

### 2.3 Workspace bootstrap — the under-specified part

`sandboxWorkspace` documents that *"there is no host path to open: the workspace
lives in a snapshot or container"* (`:18-22`). That is the design, but it leaves
open **how the repository gets in and results get out**. This is a real decision
with different reversibility properties, and it should be made explicitly:

| Approach | Mutations | Reversibility | Notes |
|---|---|---|---|
| **Bind-mount the repo into the container** | land on the host immediately | none — no fork/rollback | simplest; processes are contained but *effects are not*. Contradicts the supervised-workflow story. |
| **Copy in at start, capture/diff during, apply-out on accept** | held in the container | full — aligns with `WorkspaceState` capture/apply/diff | needs an explicit sync-out step and a settle verb; matches `supervised_session`'s fork-and-choose |
| **`WorkspaceSubstrate` (kernel plan 03, v0.7.0)** | recorded as declared intents; materialized | full, and *trace-mediated* | the declare→capture rhythm; kernel Phase 2b |

**DECIDED — 2026-10-05: copy-in, capture/diff, apply-out.** The repository is
copied into the container at session start; mutations are held there; results are
captured and applied out on acceptance. This delivers isolation **of effects**,
which is the property `supervised_task`'s rollback depends on.

Bind-mount is rejected: it contains processes but not effects, so every write
lands on the host immediately and no fork/rollback is possible — it would leave
`supervised_task` unable to honour its contract in isolated mode.
`WorkspaceSubstrate` (kernel plan 03, landed in `v0.7.0`) remains the better
long-term answer — trace-mediated, auditable, idempotent — and the machinery
D1's apply-out needs has since landed in the kernel: `Seal`/`Settle` with
`DeltaApplier` (`v0.8.0`) for apply-out-onto-a-moved-parent, and
`RecoverScopes` (`v0.9.0`) for a restart story. None of it is a prerequisite
for activation — copy-in/capture/apply-out stands on `Capture`/`Diff`/`Apply`
alone — but step 6 should reach for the landed verbs rather than reinvent
them, and settlement adoption remains the "separate plan" the non-goals
anticipated (now with a concrete basis to write against).

**Acceptance criterion for this decision:** in isolated mode, `supervised_task`
must still be able to roll back a failed attempt. If copy-in/apply-out cannot
deliver that, this decision is wrong and must be revisited before Phase 2
completes.

This is the part of the plan most likely to change under contact with the code —
validate it first.

### 2.4 Activation call site

Wire the choice at `cmd/yaah/wiring.go`, and make the mode visible in
`yaah doctor` (which should report the active workspace backend, the sandbox
image, and whether isolation is active — today that fact is only discoverable
by reading a source comment).

**Exit criteria:** an isolated session runs a real tool-driven task end to end;
`yaah doctor` reports isolation state; local mode unchanged.

---

## 7. Kernel dependency: what yaah needs, and when

Two distinct paths to isolation, with different prerequisites. This distinction
matters because it determines whether this plan blocks on kernel Phase 1.

**Path 1 — direct `Sandbox` (this plan).** `sandboxWorkspace` talks to the
kernel's `Sandbox` interface directly. Requires only: a kernel release with the
Phase 0 bug batch (§0.1) and a working containerd daemon adapter (§0.2). **Does
not require** kernel plan 03 or the digest fix. This is the shorter path and the
one this plan takes.

**Path 2 — trace-mediated `WorkspaceSubstrate` (kernel plan 03, v0.7.0).** Runs
declaration→capture materialization through the trace. Better properties
(auditable, idempotent via ledger, cross-readable with Python) but gated on
kernel Phase 1 — plan 00 §2 makes digest stability a **hard gate** before Phase 2:
*"Do not build new schema layers on a canonicalizer with byte-identity risk."*

So: **activation is not blocked on the digest fix; trace-mediated materialization
is.** Worth stating plainly, because it inverts the naive reading of the kernel's
phase order.

Kernel work referenced, with status refreshed 2026-10-10 (see the kernel's
`plans/00-execution-plan.md`; all four parity phases are complete, core at
**v0.10.0**):

| Kernel item | Plan ref | Status (2026-10-10) |
|---|---|---|
| Bug batch | T0.6 | ✅ released in `v0.4.1` |
| **Core releases through v0.10.0** | T0.8+ | ✅ `v0.5.0` (ABI byte identity), `v0.6.0` (schemas/handles), `v0.7.0` (substrates/materialization), `v0.8.0` (merge gate + settlement), `v0.9.0` (**breaking: ctx-first store APIs**, restart recovery), `v0.10.0` (sandbox registry) — all resolve from a clean module, tag-verified in CI |
| **nested `sandbox/containerd`** | T0.8b | ✅ `v0.1.3` (repin), `v0.1.4` (durable workspace states, states-lease pins, cold-namespace fix — live suite 9/9 green), ✅ **`v0.1.5` repinned to core `v0.10.0`** (the step-5 blocker, cleared): `DeltaApplier` implemented with live acceptance, registry self-registration on import |
| containerd publish (no `replace`) | T0.4 | ✅ landed; tag-time release-verify job guards it now |
| CI 3-OS + containerd job | T0.7 | ✅ landed |
| Live-daemon harness + VERDICT | T0.5 | ✅ 12/12 green; the cold-namespace re-run on `v0.1.4` was 9/9 |
| **containerd lease (GC safety)** | **T0.9** | ✅ cleared (this plan's old blocker) |
| Digest/canonical parity | T1.1–T1.2 | ✅ `v0.5.0`, corpus + drift-gated goldens |
| `ReadPathPrefix` | T1.6 | ✅ `v0.5.0` |
| `WorkspaceSubstrate` | T2b.4, T2b.7 | ✅ `v0.7.0`, live round trip green (write → capture → destroy → apply → verify) |
| Checkpoint/scope durability | T4.1–T4.2 | ✅ `v0.9.0` — `RecoverScopes` rebuilds the registry from the trace; checkpoint blobs persist with staleness detection |
| **ctx sweep (breaking)** | T4.5 | ✅ step 3c: pin bumped to `v0.10.0` and every store call site swept ctx-first (this PR) |
| Sandbox registry (`OpenSandbox`) | — | ✅ `v0.10.0` — the construction seam step 4/5 should use |

---

## 8. Phase 3 — Tests

- **Activation matrix:** `local` vs `containerd`; assert the tool sets differ by
  exactly the four host-only tools and nothing else.
- **Refusal still works:** an isolated workspace with a deliberately unmigrated
  tool registered must still be refused (guard the guard).
- **Path containment:** `ResolvePath` rejects escapes; `/` root is rejected as a
  config error. Note the documented weaker guarantee — containment is lexical
  (`path.Clean`), so an in-sandbox symlink can still escape the root, bounded by
  the container (`workspace_sandbox.go:55-58`). Test that it is *bounded*, not
  that it is impossible, and do not claim otherwise in docs.
- **Parity vs `localWorkspace`:** the sandbox path must reproduce host semantics
  for the cases already encoded in `workspace_test.go` (871 lines) — crash-safe
  write, `Stat` vs `Lstat` on dangling symlinks, `Exec` exit-code/-1/-non-zero
  error shapes, `ReadDir` with newline-containing filenames.
- **Lifecycle:** container created once per session; destroyed on clean exit and
  on each error path; no orphans after a forced failure.
- **OS gating:** non-Linux hosts fail at config load, with a test asserting the
  message names the platform.

## 9. Non-goals

- **No OS-level enforcement in the Go path.** `yaah` reuses the kernel's
  `Sandbox`, which is a *materialization/reversibility* seam, not a containment
  boundary. `shepherd`'s real jail (Seatbelt / Landlock, `vcs-core`) is Python
  and is deliberately unported — kernel plan 00 R6 fences this, and the kernel's
  `sandboxWorkspace.ResolvePath` comment already concedes the weaker guarantee.
  If OS-level enforcement is wanted, that is a separate decision requiring a
  written note against the kernel's `GAP-REPORT.md` rejection.
- No `go_refactor` reimplementation for isolated mode.
- No multi-tenant / per-sub-agent sandbox pooling.
- No `WorkspaceSubstrate` adoption (kernel `v0.7.0`+, landed; separate plan — Phase 3's merge gate/settlement, `v0.8.0`+, are its basis).
- No Windows or macOS isolation.

## 10. Decisions

All five questions raised during planning are now resolved (2026-10-05). Each
records a revisit trigger, because several are only correct under current
assumptions.

**D1. Bootstrap — DECIDED: copy-in / capture / apply-out.** See §2.3 for the
reasoning and the acceptance criterion that would invalidate it.
*Revisit if:* `supervised_task` cannot roll back in isolated mode.

**D2. Sandbox scope — DECIDED: one sandbox per session; sub-agents inherit it.**
No per-sub-agent sandbox, no pooling. Rationale: a sub-agent already runs inside
the session's blast radius, so a second container buys little containment and
costs a container lifecycle per child; and per-sub-agent sandboxes interact badly
with the existing scope-isolation rationale at `runner.go:349-355`, where the turn
checkpointer deliberately lives on the child's own scope so rewinds never touch
the supervised tool's attempt-level checkpoints.
*Revisit if:* isolation becomes multi-tenant, or a role must be restricted more
tightly than its parent.

**D3. `role` tool — DECIDED: stays host-only.** Role files are harness
configuration the host reads, not workspace content (`filesystem_tools.go:42-43`).
An isolated session reads the host's roles. This is also why `role` is not
"migration debt" but a deliberate classification.
*Revisit if:* D2 ever permits per-sub-agent sandboxes with distinct role sets, or
if isolation becomes multi-tenant.

**D4. Tool-surface delta in the record — DECIDED: yes, record it.** The active
workspace backend (`local` / `containerd`) and the omitted tool names are written
into session/trace metadata. Rationale: a run's available tool set differs by
mode, so a trace reader comparing two runs would otherwise misattribute
behaviour — "the model didn't use `background_process`" and "the model *couldn't*
use `background_process`" must be distinguishable. Cheap now; expensive to
retrofit once traces exist.
*Revisit if:* kernel `WorkspaceSubstrate` lands and makes the tool set part of
the materialized request.

**D5. Image contract — DECIDED: probe at startup, and document the contract.**
Both, not either. A startup probe (presence of `sh`, `stat`, `find`, `git`, `rg`,
and a Go toolchain) fails fast, before the session consumes a task; documenting
the contract alone would surface the gap mid-task at first tool call, wasting the
run. The probe is a few seconds against a container that must already exist.
Required GNU userland: `stat -c` and `find -printf`
(`workspace_sandbox.go:142`, `:221`).
*Revisit if:* a distroless or non-GNU image is ever wanted, in which case the
in-band shell surface must be reworked rather than probed.

## 11. Execution order

Decisions are settled (§3, §10), so this is an implementation sequence. Steps 0–2
have been **executed**; the verdict is recorded against each.

0. ✅ **DONE** — committed the kernel containerd work as `603fdcf` (6 files,
   +1202/−48), including the previously untracked `live_test.go`.
1. ✅ **Core release DONE** — kernel **`v0.4.1`** is published, so `yaah` can pin
   `v0.4.1` rather than a pseudo-version. Both tags point at the merge commit
   `bee4ca5`, and resolution is verified from a clean module context:

   ```
   github.com/buchenberg/shepherd-kernel-go                     v0.4.1
   github.com/buchenberg/shepherd-kernel-go/sandbox/containerd  v0.1.1
   ```

   **But the nested release does not carry the repin (T0.8b).** Resolving the two
   tags separately does *not* show that the nested module depends on the fixed
   core, and it does not. The published tag's own `go.mod` still says:

   ```
   $ git show sandbox/containerd/v0.1.1:sandbox/containerd/go.mod | grep shepherd
   github.com/buchenberg/shepherd-kernel-go v0.4.0     # <- pre-fix core
   ```

   The repin to `v0.4.1` exists only on kernel PR #11, and **merging that PR will
   not update the published tag** — the repin lands on `main` while the tag keeps
   pointing at the old tree. `v0.1.1` also **cannot be repaired in place**: Go
   module versions are immutable once the proxy and `sum.golang.org` record them.
   So the nested module needs a new version, **`sandbox/containerd/v0.1.2`**, cut
   from `main` after #11 merges.

   This does not block step 4 (or anything else here), because nothing imports the
   nested module yet — our sandbox construction is step 5. But it **must** be fixed
   before that step consumes it, or `yaah` silently builds against pre-fix core.

   Also worth recording, because it will recur: the first `v0.4.1` cut pointed at a
   commit that predated the review fixes, so publishing it unchecked would have
   pinned a core release **missing the lease race fix**. Check
   `git merge-base --is-ancestor <last-fix-commit> <tag>` before trusting a tag.
2. ✅ **DONE — gate cleared** — kernel live-daemon smoke, T0.5. The harness found
   nine defects, the first soak then **failed 3 of 5 runs**, and the cause (no
   containerd lease) was found and fixed. **Re-soak: 12/12 green.**
2b. ✅ **DONE (`8e9b867`)** — kernel **T0.9**: the sandbox holds a labelled
   containerd lease for its lifetime, so its snapshots cannot be garbage-collected.
   Accept met at 12/12 consecutive green runs, and the mechanism is now asserted
   in-test rather than inferred. **Steps 3–9 are unblocked.**
3. ✅ **DONE** — promoted into `internal/sandboxfake` (was `fakeSandbox` in
   `workspace_test.go`): an in-memory `shepherd.Sandbox` with a real lifecycle
   state machine (Create gates Exec/FileIO and provisions the workdir; Destroy
   ends the sandbox; double-create/destroy are errors, so Phase 2's leak tests
   have something to detect), a map-backed tree, realistic `Capabilities()`
   (all true; `Containment` matches the containerd backend's `contained`), and
   an `sh -c` interpreter for the exact script surface `sandboxWorkspace`
   emits (stat/find/mktemp/trap/cat/chmod/mv/rm/mkdir/test), so the config →
   construction → `SetWorkspace` → tool-dispatch path is testable on any OS
   with no daemon — validated on a Windows host. `Capture`/`Apply`/`Diff` are
   real (snapshot/reset + unified diff) for the D1 rollback tests; `ExecHook`
   keeps wire-level pinning (transport errors, canned exits, malformed output).
   Workspace tests migrated: they now round-trip real writes instead of
   asserting canned stdout. Gates: build, vet, staticcheck, gofmt, full
   `go test ./...` (48 pkgs), and 5 consecutive green runs of the previously
   flaky `internal/tools` suite.
3c. **Pin bump + ctx sweep (added 2026-10-10 — the actual next step).**
   `yaah` pins core **`v0.4.0`**; the kernel is at `v0.10.0`, and `v0.9.0`
   made `context.Context` the first parameter on every store API (the
   trace-authority context moved to second). The bump is mechanical — ~29
   call sites across ~20 files (`internal/agent/pipeline/trace.go`,
   `internal/tools/subagent_trace.go`, `internal/agent/runner/runner.go`,
   `internal/tools/supervisor*.go`, checkpoints, ...) go from
   `store.Append(shepherd.TrustedAppendContext, …)` to
   `store.Append(ctx, shepherd.TrustedAppendContext, …)`. `Sandbox`
   interface call sites are untouched (those methods always took ctx);
   `internal/sandboxfake` needs no changes. Land as its own PR **before**
   step 4, so the workspace work starts on the modern API surface and
   never mixes mechanical churn with feature changes. Optional but cheap
   in the same PR: adopt `OpenSandbox`-era semantics nowhere yet — just
   the pin (`v0.10.0`) + sweep + `go test ./...`.
4. ✅ Config surface (§1.1), including D5's startup probe (landed; wiring
   is step 5). The kernel's
   `v0.10.0` sandbox registry is the natural seam: `workspace.backend`
   maps to a registry name — both backends now self-register on import
   (`git` in-core, `containerd` in its nested package since `v0.1.5`), so
   the config surface resolves names through `shepherd.OpenSandbox` with no
   host-side factory wiring (and must not add one: duplicate registration
   panics by design). `shepherd.SandboxBackends()` powers `doctor`'s
   backend listing. Hand-rolled switches remain acceptable; the registry is
   the seam, not a requirement.
5. ✅ Sandbox construction + lifecycle (§1.2) — **landed**. `tools.SessionSandbox`
   owns the session's sandbox end to end: `StartSessionSandbox` probes the image
   on a throwaway sandbox first (the probe keeps its own lifecycle), then opens,
   creates (spec workdir = the containment root, so test backends provision it
   the way the containerd adapter provisions its `Config.Workdir`), and binds
   the session `sandboxWorkspace`. Every error path destroys — including a
   failed `Create`, whose cleanup runs on `context.WithoutCancel`; the wiring's
   failed-start guard covers every `return nil, err` between construction and
   hand-off, so no path leaks a container. `Destroy` is once-only
   (`sync.Once`), so a failed startup that already destroyed followed by
   session close cannot double-destroy. `cmd/yaah/wiring.go` selects the
   substrate by backend: containerd constructs the session sandbox and skips
   `SetPathValidator` entirely (both must not run), `--workspace` with the
   containerd backend warns that it is inert; local keeps the legacy path
   byte-for-byte. `SetWorkspace` runs only after every registration — a refusal
   fails the session loudly with the destroy joined in — so until step 7
   replaces the registry, selecting `backend: containerd` fails at startup
   with the gate's own diagnostic (the host-only tools — three in a default
   session; `supervised_task` registers only when `shepherd_trace_dir` is
   set), which is the honest stopping point: constructed and probed, never
   half-isolated. `agentSession.close()` destroys after
   `backgroundJobs.CloseAndWait` — cancel, then a bounded wait for the job
   goroutines, because `Close` alone never joins them and a mid-`Exec`
   sub-agent would race the teardown — and a teardown failure warns
   rather than aborting the rest of teardown. Gates: build, vet, staticcheck,
   gofmt, full `go test ./...` (one pre-existing environmental failure:
   `TestPowerShellTool_runsSimpleCommand` needs a `powershell` binary that
   Linux hosts do not have; verified failing on main without this change),
   Windows + darwin cross-compile.
6. ✅ Bootstrap implementation per **D1** (§2.3) — **landed**.
   `StartSessionSandbox` gained the session's source tree: when non-empty,
   the bootstrap runs inside the startup bound — verify the source is a git
   work tree (the isolated story is git-anchored end to end: the
   containerd backend's Diff runs against the in-container HEAD, and
   apply-out is a git patch), copy the tree in file-by-file through the
   kernel's `WriteFile` (parents auto-created, perm bits carried, symlinks
   skipped loudly, empty dirs dropped), capture the baseline, then **Apply
   the baseline once**. That closing Apply is the non-obvious piece: the
   containerd backend records an applied state's git HEAD as its delta
   base, and only then does every later capture carry an outward binary
   patch — without it, no state ever would. `DiffSinceBaseline` is the
   diff-during verb; `ApplyOut(hostDir)` is the single door changes leave
   the container through: the backend's base64 git binary patch applied
   with `git apply --binary` when present (containerd), a clean
   delta-base-with-no-patch is a no-op, and backends without delta
   machinery (the fake) fall back to copying the changed paths out through
   the kernel's `ReadFile` (content- and existence-accurate, blind to
   mode-only changes). The D1 acceptance criterion — rollback still works
   in isolated mode — is pinned by
   `TestBootstrap_RollbackRestoresBaseline`: mutate in the sandbox, apply
   the baseline, the diff goes empty. Apply-out is not yet invoked by any
   session flow — the accept seam arrives with the step-7 tool surface;
   the verbs are landed and round-trip-tested (modify/add/delete, binary
   payloads, backend-patch preference).
   **Post-review hardening (CI + PR review):** the source check is now
   context-bound and refuses the layouts the copy-in cannot reproduce
   faithfully — repository subdirectories, linked worktrees and
   submodules (a `.git` file), and repositories with no commits. Tracked
   symlinks and tracked deny-pattern matches fail the bootstrap (the
   container would otherwise record them as deletions and apply-out would
   delete them from the host); untracked deny-pattern matches — the
   usual case, local secrets — are excluded from the copy-in, so the
   agent can never read them inside the container either. A dirty source
   gets one synthetic in-container baseline commit of exactly the copied
   tree, so apply-out patches carry only session changes and apply
   cleanly back onto the dirty host. Copy-out writes are atomic, carry
   the sandbox entry's mode (new executables arrive executable), stay
   contained under the target root, and propagate read errors instead of
   misreading them as directories.
   **Known costs, deliberate for the correctness-first landing:** the
   copy-in costs one backend exec per file (a bundle-based fast path is
   the anticipated follow-up once D1 meets real sessions); a moved host
   parent at apply-out fails loudly rather than merging — that is
   settlement's problem (kernel `Seal`/`Settle` + `DeltaApplier`),
   deliberately still out of scope.
7. ✅ Tool surface per **§3 Option A** + **D3** (§2.1), prompt delta per §2.2,
   and **D4**'s trace metadata — **landed**.
   `tools.NewIsolatedRegistry()` builds the curated leaf set — same
   platform shell rule as `NewRegistry`, minus the host-only leaves, with
   the filter reading the same `hostOnly()` marker `SetWorkspace`'s gate
   uses, so curation and enforcement cannot classify a tool differently.
   The wiring omits `role`, `background_process`, and `supervised_task` at
   their registration sites, collecting the omitted names as it goes, so
   `SetWorkspace` finds both diagnostic lists empty and an isolated
   session is finally selectable — the gate stays the enforcement point
   and still fails the session loudly if the invariant ever breaks.
   The accept seam step 6 anticipated is here: the `apply_out` tool,
   registered only in isolated mode, previews the diff since the baseline
   (`dry_run`) or invokes `ApplyOut` onto the host. The real apply is
   gated behind user approval through `DangerClassifier` — approval mode
   "ask" prompts, "deny" strips the call; a dry run is never dangerous,
   and unparseable arguments fail closed at the gate. The prompt delta
   (§2.2) is `prompts.IsolatedWorkspaceGuidance(omitted)`, injected in
   place of the sub-agent guidance (which would promise a
   `supervised_task` that cannot exist here): it names every omitted tool
   with its reason, explains the spawn_subagent-only story, documents the
   apply_out workflow, and warns that sub-agents do not run inside the
   container yet. Decision D4 lands in the session record: the sessions
   table gains `workspace_backend` and `workspace_omitted_tools` columns
   (migration-safe), written after the registry is final; a resume that
   changed mode overwrites, so the columns describe the most recent run.
   Tests: the leaf-level activation matrix (local vs isolated differ by
   exactly `go_refactor`), the curated registry's selectability (both
   diagnostic lists empty, `SetWorkspace` accepts), the host-only leaf
   classification pinned, `apply_out` end to end against the fake
   (dry-run/apply/no-op/danger table), the guidance content, and the
   session-metadata round-trip plus migration. Gates: build, vet,
   staticcheck, gofmt, full `go test ./...`, windows + darwin
   cross-compile, `-race` on internal/tools.
   **Known gap, deliberate:** sub-agents still do not inherit the sandbox
   (step 9, D2) — `spawn_subagent` in an isolated session dispatches
   host-side sub-agents, and the prompt says so. `doctor` visibility of
   the active substrate is step 8.
   **Post-review hardening (PR review):** the two remaining host-mutation
   paths around apply_out are closed — MCP tools are omitted by policy
   in isolated mode (they execute on the host through stdio/HTTP clients
   no Workspace can intercept; the servers still start, the omission is
   named in the prompt and the D4 record, and the approval gate is not
   told about tools that cannot dispatch), and `skill`/`plan` become
   read-only there (their create/edit/delete actions write host harness
   configuration in `.agents/`, tracked in git). The omitted-leaf list is
   derived from the marker (`tools.HostOnlyLeafTools()`), not
   hand-maintained. Supervised roles fold back into `spawn_subagent` in
   isolated mode — omitting `supervised_task` would otherwise leave them
   with no dispatch tool at all. `apply_out` now refuses an apply that
   was not preceded by a dry run in the session, and refuses one whose
   preview went stale (the workspace changed since): the approval prompt
   shows only the arguments, so the user approves exactly the diff they
   were shown, or nothing. The D4 write moved after the SetWorkspace
   gate, so a refused run never overwrites the record.
8. Activation wiring + `doctor` (§2.4). *First genuinely useful release.*
9. Test matrix (§8), docs, and D2's inheritance behaviour for sub-agents (§1.3).

**Stopping points:** step 5 leaves config without activation — a clean stop. Step 8
is shippable. Step 2's gate fired once and is now cleared, so nothing here is
waiting on a decision or a fix.

**Status (refreshed 2026-10-11): steps 0–6 are done, and the kernel has since
completed its entire parity plan (core `v0.4.1` → `v0.10.0`). The next step is
7 — the isolated tool surface (Option A registry, prompt delta, D4 trace
metadata) — which is also what makes the step-6 verbs reachable from a real
session. After 7: steps 8–9 as sequenced.**

**The two release tasks that rode alongside are done:**
- **Step 3c (yaah-side):** pin `v0.10.0` + the mechanical ctx sweep — this PR.
- **Step 5's kernel-side blocker:** `sandbox/containerd/v0.1.5` is tagged
  (repinned to core `v0.10.0`, `DeltaApplier` implemented and live-verified,
  registry self-registration). Step 5 may start.

**Hard dependencies, not reorderable:** step 1 before step 2 (the smoke needs the
bug batch), step 3 before step 7 (the activation tests need the fake), step 2b
before step 4 (a green soak before anything depends on the backend — now
satisfied), and **step 3c before step 4** (no feature work on the pre-ctx
API surface).
