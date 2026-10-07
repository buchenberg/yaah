# Development Plan: Isolated Workspace Activation (yaah side)

**Status:** decisions settled 2026-10-05; ready to implement · **Author:** buchenberg
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

> Note: the nested module pins `github.com/buchenberg/shepherd-kernel-go v0.4.0`
> while its own tree contains the bug batch, so `sandbox/containerd@v0.1.0`
> currently depends on a core version older than its own fixes. Worth fixing in
> the same patch release.

### 0.2 Establish that the containerd backend actually works — ❌ GATE NOT CLEARED

**Result (2026-10-07): the harness works; the backend does not yet.** Run against
containerd v2.3.5 (overlayfs snapshotter). The harness is committed (kernel
`603fdcf`) and **nine defects were found and fixed** by it. But **3 of 5
consecutive runs failed, with a different test failing each time** — the signature
of a shared-resource race, not per-test bugs.

**Cause established:** the adapter holds **no containerd lease** (the only "lease"
matches in `client.go` are the word *release* in a comment and an error string),
and this daemon's GC is aggressive — `mutation_threshold = 100`,
`schedule_delay = '0s'`, `startup_delay = '100ms'`. `Capture` stops the task and
then prepares a successor whose parent is the just-committed snapshot; with the task
stopped, that snapshot is **unreferenced**, so the GC can reap it in that window.
Observed directly:

```
Capture: containerd sandbox: prepare successor snapshot:
  parent snapshot shepherd/<id>/committed/1 does not exist: not found
Destroy during cleanup: remove snapshot shepherd/<id>/active/0: ... does not exist
```

**Consequence for this plan: step 2 is a real stop-gate and it has fired.** Per
§11 — **stop here.** Phases 1–3 must not start until kernel **T0.9** (hold a lease
for the sandbox's lifetime; accept on ≥10 consecutive green soak runs) lands and
the live suite is reliably green.

This is not pedantry. A workspace whose snapshots can be garbage-collected
mid-operation is *worse* than one that fails loudly: over a long agent run it would
lose the workspace intermittently, and the symptom would look like anything but a
containerd GC. Building isolated mode on it would produce exactly the
nondeterministic data-loss bug that is hardest to diagnose.

Good hygiene to note, since it bounds the blast radius: the runs leaked **no**
containers, tasks, or sandbox-owned snapshots — `Destroy` cleans up correctly even
when it also reports a missing-snapshot error.

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

Add workspace config keys (no workspace keys exist today). Proposed shape,
following existing config conventions in `internal/config/load.go`:

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
`WorkspaceSubstrate` (kernel plan 03, v0.7.0) remains the better long-term answer
— trace-mediated, auditable, idempotent — but it is gated on kernel Phase 1 and
must **not** be a prerequisite for activation.

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

Kernel work referenced, with status as of 2026-10-07 (see the kernel's
`plans/00-execution-plan.md`):

| Kernel item | Plan ref | Status |
|---|---|---|
| Bug batch | T0.6 | ✅ landed, ❌ unreleased |
| **`v0.4.1` release** | **T0.8** | ⬜ **decided — this plan's pin target**; not blocked by T0.9 |
| containerd publish (no `replace`) | T0.4 | ✅ landed |
| CI 3-OS + containerd job | T0.7 | ✅ landed |
| Live-daemon harness | T0.5 | ✅ committed (`603fdcf`); found and fixed 9 defects |
| **Live-daemon VERDICT** | **T0.5** | ❌ **FAILED — 3/5 runs, different test each time** |
| **containerd lease (GC safety)** | **T0.9** | ⬜ **BLOCKER for this plan** — accept on ≥10 green soak runs |
| Digest/canonical parity | T1.1–T1.2 | ⬜ not started |
| `ReadPathPrefix` | T1.6 | ⬜ not started |
| `WorkspaceSubstrate` | T2b.4, T2b.7 | ⬜ not started |
| Checkpoint durability | T4.1–T4.2 | ⬜ not started |

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
- No `WorkspaceSubstrate` adoption (kernel v0.7.0; separate plan).
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
1. ⬜ **READY** — kernel **`v0.4.1`** containing the Phase 0 bug batch (kernel plan
   00 T0.8). Not blocked by step 2: the core bug batch is independent of the
   containerd lease, and `yaah` needs a pinnable tag rather than a pseudo-version.
2. ❌ **FAILED — gate fired** — kernel live-daemon smoke, T0.5. The harness works
   and found nine defects, but the suite is flaky (3/5 runs, different test each
   time). **Per this plan's own stop-gate instruction: stop.**
2b. ⬜ **NEW BLOCKER** — kernel **T0.9**: hold a containerd lease for the sandbox's
   lifetime so its snapshots cannot be garbage-collected. Accept on **≥10
   consecutive green soak runs**. Steps 3–9 are all downstream of this.
3. Promote `fakeSandbox` into a reusable in-memory `Sandbox` (§0.3) — *this one is
   independent of T0.9 and can proceed now*, since it is pure test-support work
   with no daemon involvement.
4. Config surface (§1.1), including D5's startup probe.
5. Sandbox construction + lifecycle (§1.2). *First clean stopping point.*
6. Bootstrap implementation per **D1** (§2.3).
7. Tool surface per **§3 Option A** + **D3** (§2.1), prompt delta per §2.2, and
   **D4**'s trace metadata.
8. Activation wiring + `doctor` (§2.4). *First genuinely useful release.*
9. Test matrix (§8), docs, and D2's inheritance behaviour for sub-agents (§1.3).

**Stopping points:** step 2 fired and is a legitimate "not viable yet" exit. Step 5
leaves config without activation — also clean. Step 8 is shippable.

**What can proceed despite the failed gate:** steps 1 and 3. Step 1 unblocks
`yaah`'s pin; step 3 is daemon-free test-support work. **Everything from step 4
onward requires a green soak** — building the config surface and lifecycle on a
backend whose snapshots can vanish would mean debugging the wrong layer.

**Hard dependencies, not reorderable:** step 1 before step 2 (the smoke needs the
bug batch), step 3 before step 7 (the activation tests need the fake), and
**step 2b before step 4** (a green soak before anything depends on the backend).
