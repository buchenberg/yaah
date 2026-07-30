# Remove the default sub-agent role

## Context

`RoleDefault` (`SubAgentRole = ""`) is an empty-string sentinel, not a real
role. It activates when the model omits `role` in a `spawn_subagent` call, or
when the `RoleRegistry` is empty (tests only — production always has the four
embedded roles). It grants the legacy full built-in tool set (including
`spawn_subagent` itself), the display name "Pat", and a hardcoded guidance
paragraph. It bypasses the role model entirely: no contract, no specialty, no
per-role limits, no per-role directives.

The schema enum never lists it — it exists only as the omission fallback.
Removing it makes every sub-agent a first-class, configured role.

## Decision

**Option A — hard removal.** `role` becomes a required task-tool parameter.
Empty or unknown roles return a tool error that points the model at
`list_subagents`. No implicit fallback tool set.

Rejected alternative: mapping omission to a named `worker.md` role. That
preserves today's behavior but keeps a de-facto default under a new name and
duplicates a role profile that no one has asked for.

## Behavior change

| Scenario | Before | After |
|---|---|---|
| `spawn_subagent` with valid role | role profile | unchanged |
| `spawn_subagent` with no `role` | full-access legacy sub-agent ("Pat") | tool error: role is required, use `list_subagents` |
| `spawn_subagent` with unknown role | zero-value profile → full legacy tool set | tool error: unknown role, lists valid names |
| Empty `RoleRegistry` (tests) | legacy profile fallback | N/A — callers use explicit roles; legacy helpers deleted |

The model self-corrects within one turn: the schema marks `role` required,
the enum lists valid roles with descriptions, and the error message repeats
them.

## Changes

### 1. `internal/tools/task.go` — enforce role at the tool boundary

- `Schema()` fallback (raw JSON, used when `RoleNames` is empty): add
  `"role"` to `required`; update its description (drop "Omit for the default
  full-access role").
- `BuildTaskSchema`: add `"role"` to the schema's `required` list; replace
  the "Omit for the legacy default tool set" line in `roleDesc` with "Use
  list_subagents for full details."
- `Execute` (or `parseSubAgentParams` — wherever `params.Role` is first
  read): validate before dispatch:
  - empty role → error `role is required — pick one of: <names> (see list_subagents)`
  - non-empty role absent from `t.RoleNames` (when `RoleNames` is non-empty)
    → error `unknown role %q — valid roles: <names>`
  Validation belongs here, not in the runner, so the model gets the error as
  a tool result and can retry.

### 2. `internal/agent/subagent/role.go` — delete the sentinel

- Remove the `RoleDefault` const and its doc block.
- `RoleDisplayName`: drop the `"Pat"` branch; plain profile lookup with
  fallback to `string(role)`.
- `RoleSpecialty`: drop the `RoleDefault` branch.
- Delete `legacyGuidance` and `legacyProfileFor` entirely.
- `RoleProfileFor` / `RoleGuidance`: when no registry is set, return
  zero-value profile / `""` (callers must not depend on legacy fallbacks).
  Keep the registry-present path unchanged.
- `platformShell()` becomes unused → delete.

### 3. `internal/agent/subagent/role_def.go` — drop sentinel branches

- `ProfileFor`: remove the `role == RoleDefault` short-circuit (the unknown-role
  path already returns the zero value, which is now the only fallback).
- `Guidance`: same removal.
- Update both doc comments (no more "RoleDefault" references).

### 4. `cmd/yaah/subagent_runner.go` — remove the legacy tool-set fallback

- `buildSubAgentRegistry`: the `len(profile.Tools) == 0` branch (full legacy
  registry with write/edit/delete + nested task tool) is now unreachable for
  valid roles — every embedded role declares tools, and invalid roles are
  rejected by the task tool before the runner executes. Delete the branch;
  the empty-registry construction path (`NewRegistry` + tracker wrapping)
  goes with it.
- Update the function doc comment ("The RoleDefault profile (empty Tools)
  falls back to the full built-in tool set..." paragraph).
- Defensive guard in `makeTaskRunner`: if `profile.Tools` is somehow empty,
  return an error rather than spawning an unconfigured loop (belt and
  suspenders; should never fire).

### 5. Tests

- `internal/agent/subagent/role_test.go`: delete the `RoleProfileFor(RoleDefault)`
  full-profile assertions and the `RoleGuidance(RoleDefault)` non-empty
  assertion. Replace with: no-registry → zero-value profile and empty
  guidance for any role.
- `internal/agent/subagent/role_def_test.go`: the
  `ProfileFor(RoleDefault)` zero-value test becomes an unknown-role test
  (`ProfileFor("nope")`); the "fallback when no registry is set" test loses
  its legacy-default expectations.
- `internal/tools/task_test.go` (create if absent): table-driven tests for
  the new validation — empty role rejected, unknown role rejected with the
  valid names in the message, valid role dispatches; schema `required`
  includes `role` in both the enum and fallback variants.
- `cmd/yaah/subagent_runner_test.go`: add a test that every embedded role
  (analyst, developer, reviewer, tester) produces a non-empty
  `profile.Tools` via `buildSubAgentRegistry` — guards against a future
  role file silently shipping without tools and hitting the defensive
  guard.

### 6. Docs and prompt copy

- Grep `README.md`, `AGENTS.md`, `docs/`, `internal/prompts/identity.md`,
  and `internal/prompts/tools/task.md` (if present) for "default role",
  "full-access", "Pat", "omit the role" and update.
- `list_subagents` output needs no change (it never listed default).

## Verification

1. `gofmt -l cmd/ internal/` empty; `go build .`; `go vet ./...`;
   `staticcheck` on touched packages.
2. `go test ./internal/agent/subagent/ ./internal/tools/ ./cmd/yaah/`.
3. Live smoke (dev-loop discipline): rebuild, run yaah with otel verbose,
   prompt "demonstrate your subagent capabilities" —
   - model dispatches with explicit roles (check `subagent.role` attrs in
     Jaeger; no empty roles),
   - if the model tries to omit role, the trace shows one failed
     `spawn_subagent` tool span with the required-role error, followed by a
     successful retry with a real role.
4. `bd` — file the implementation issue before starting; close on completion.

## Risks

- **Model friction:** models trained on the old schema may omit `role`
  initially. Mitigated three ways: schema `required`, enum descriptions,
  and a self-correcting error message. One wasted turn at worst.
- **User-defined roles without tools:** a user role file with an empty
  `tools:` list previously got the full legacy set; now it hits the
  defensive guard. Acceptable — that config is a mistake, and the error
  says so. Note it in the role-file docs if any exist.
- **Nested task tool:** the legacy branch registered `spawn_subagent` in
  fallback sub-registries. Real roles opt into nesting via
  `spawn_subagent` in their `tools:` list — verify each embedded role that
  needs it still declares it (analyst/developer/reviewer/tester tool lists
  must be checked during implementation, not assumed).
