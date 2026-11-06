---
name: modularization-wave2
description: "Modularization epic (yaah-xpxz) wave 2: finish the internal/tools split (substrate port + retained-dispatch lane) and the assemble-stage concern split."
status: approved
---

# Modularization wave 2 — internal/tools trifurcation + assemble split

Wave 1 (PR #791) landed: `buildLoopInfra` decomposition into ~9 constructors,
`runner.go` split into 8 concern files, and substrate slice 1 (`belt.go` +
`grants.go` → `internal/tools/substrate/` behind `substrate_aliases.go`).
Wave 2 finishes the P1 beads: `yaah-5d85` (slices 2–3 of the tools split) and
`yaah-mp04` (the remaining assemble-stage concern split).

## Workflow (proven in wave 1)

- One bead per wave slice; each slice is a separate commit (rename detection
  keeps pure moves honest).
- **Host-side `git mv` is mandatory for byte-exact moves** — sub-agent sandbox
  edits would turn moves into delete+add. The orchestrator does the `git mv`
  and dispatching; sub-agents do import rewiring + gates via the sealed-proposal
  path (`apply` with `strategy: path_disjoint`).
- Gates on the **combined tree** are the real verification (sub-agents are
  test-blind in their sandboxes): `gofmt -l` (tracked files only — `.scratch/`
  is noise), `go vet ./...`, `go build ./...`, `go test ./...`, `staticcheck ./...`.
- One branch `refactor/xpxz-wave3`, one PR at the end.

## Slice A — substrate port move (`yaah-5d85`, slice 2/3)

Move the workspace/path-validator port into `internal/tools/substrate/`:
`workspace.go` (Workspace, ExecRequest/ExecResult, WorkspaceSetter,
localWorkspace, confinement downgrade), `workspace_sandbox.go` (sandboxWorkspace
container port), `path_validator.go` (PathValidator, PathRefusalError,
PathValidatorSetter). Their tests move with them
(`workspace_test.go`, `workspace_sandbox_test.go` in whatever form, `path_validator_test.go`,
`workspace_*_test.go` support files as applicable).

- Keep the wave-1 seam: extend `internal/tools/substrate_aliases.go` with
  type aliases so `internal/tools` leaf code and the ~34 external references
  (`tools.Workspace`, `tools.PathValidator`, `tools.Registry`) compile
  unchanged in this slice. External call sites are rewired in a follow-up or
  left on aliases per wave-1 precedent.
- Acceptance: pure `git mv` + alias/imports only — no behavior change; all
  gates green; `git diff --stat` shows renames, not delete+add.

## Slice B — retained-dispatch lane (`yaah-5d85`, slice 3/3)

Move the retained-dispatch runtime out of `internal/tools` into
`internal/dispatch`: `retained.go` (1712L), `proposal.go`,
`proposal_conflict.go`, `proposal_merge.go`, `proposal_rehydrate.go`,
`git_delta.go` (1236L), plus their tests.

- This is the slice that breaks the engine's layering most (14 internal
  imports). After the move, `internal/tools` should import `internal/dispatch`
  (for the proposal tool registration) and not the reverse — verify the
  dependency direction and report any residual cycle blockers.
- Same alias-seam discipline: the `proposal` tool and `delegate` wiring in
  `internal/tools`/`cmd/yaah` compile through aliases or thin re-exports
  within this slice; broader cleanup can trail.
- Acceptance: pure moves + import rewiring; dependency direction
  `tools → dispatch → (substrate | …)` holds; all gates green.

## Slice C — assemble-stage concern split (`yaah-mp04` remainder)

`assemble()` in `cmd/yaah/wiring_stages.go` (from line ~1276) still mixes three
concerns: the SetWorkspace enforcement gate + sandbox teardown join, prompt
assembly (isolated vs. retained vs. sub-agent guidance), and session recording /
event-hook wiring. Split into named steps (e.g. `sealWorkspace`,
`finishPrompts`, `recordSession`) — same file or sibling files under `cmd/yaah`,
keeping the rollback/ownership guard contract exactly as wave 1 preserved it.

- Acceptance: `assemble` reads as an ordered list of named steps; error/teardown
  semantics byte-for-byte equivalent (the SetWorkspace failure path and
  `teardownHandled` once-guard behavior must not change); gates green.

## Order and commits

1. Slice A (substrate port) — commit scoped to the moved paths.
2. Slice B (dispatch lane) — depends on A being settled (proposal code touches
   the port).
3. Slice C (assemble split) — independent of A/B; can be dispatched in parallel
   with B once A is in.

One commit per bead (`yaah-5d85` gets two commits, A and B; `yaah-mp04` gets
one). Final: run the full gate suite on the combined tree, push
`refactor/xpxz-wave3`, open one PR referencing `yaah-xpxz`, `yaah-5d85`,
`yaah-mp04`.

## Out of scope (wave 3 candidates)

- `yaah-9y5` / `yaah-koud` — memory.go (1312L) split by concern.
- `yaah-1cc`, `yaah-1kn`, `yaah-26d` — P2 epic children (package promotion,
  web/serve extraction, dependency-chain cut).
- External call-site rewiring off the `substrate_aliases.go` seam (can be its
  own small bead if it grows).
