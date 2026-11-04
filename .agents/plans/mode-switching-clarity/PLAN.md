---
name: mode-switching-clarity
description: Plan/build mode clarity: enforced-state directives, expanded plan-mode toolset, mode persistence, in-band switch signals
status: draft
---

# Mode-switching clarity

Goal: make plan/build mode state observable and honest for the model — one source
of truth for enforcement, symmetric generated prompts, in-band switch signals,
persisted mode, real switch-out mechanism.

## Resolved decisions
1. **Directive describes the enforced state** — wording is generated from the actual enforcement level (`tools_withheld` vs `policy_only`), not a parallel policy statement.
2. **Plan-mode allowlist grows** — admit `plan`, `staticcheck`, and `git` (read-only subcommands only, via the existing git danger tables).
3. **Mode is persisted** — survives load/resume.

## Steps (branch: feat/mode-clarity off origin/main)

1. **PlanModeSpec single source of truth** (`cmd/yaah/session.go`, `internal/agent/tools.go`)
   - `PlanModeSpec{Tools, Enforcement (tools_withheld | policy_only), Mode}`.
   - `toolsLevelForMode` builds the tool set from it; `systemPromptForMode` generates the directive from the same struct.
2. **Expand the plan-mode allowlist** (`internal/agent/tools.go` readOnlyToolNames)
   - Add `plan`, `staticcheck`; add `git` subcommand-aware (read-only pass, mutating refused by `allowsTool` / agent_dispatch gate, reusing internal/tools git safe/mutating tables).
   - Fix "read-only tools" wording in `/plan` help and docs/features.md to name the actual set.
3. **Generated symmetric directive text, both modes**
   - Plan + tools_withheld: state tools are withheld, list the inspection set, name exit mechanisms (`/build` TUI/REPL, web palette, ACP session/set_mode), ask path (question tool / reply), and that a mode-change notice will appear.
   - Plan + policy_only: same but honestly states mutating tools are present but must not be used.
   - Build mode mirror: full toolset, edits/commands permitted subject to approval gates.
4. **In-band switch signal**
   - `ModeChangedEvent` in internal/agent/events (+ allEvents exhaustive test), emitted by SetMode; rendered by all views (TUI info pane, REPL, web palette, ACP switch_mode).
   - Conversation-visible line on switch: `[session] Mode changed: plan -> build.` — plus "applies to the next prompt" for mid-run switches (build_loop captures mode at run start).
5. **Persist mode**
   - Store mode with the session record (SQLite session state); SetMode writes through; load/resume restores; fresh = build. Update session.go comment and docs/acp.md "Mode is not persisted" section.
6. **Switch-out mechanism made real**
   - Implement the ACP agent-initiated `switch_mode` request (SwitchModeRequest / KindSwitchMode -> AskResponse confirm/deny per acp-spec session-modes.mdx). Currently dead code.
7. **Docs & tests**
   - docs/features.md, docs/acp.md, TUI help, identity.md terminology (disambiguate plan mode vs plan tool).
   - Tests: directive symmetry (both modes x both enforcement levels), git-subcommand plan-mode gate, mode persistence round-trip, ModeChangedEvent exhaustive coverage.

## Validation
go test ./... ; go vet ./... ; gofmt -l . empty ; staticcheck via `go run honnef.co/go/tools/cmd/staticcheck@latest`.
Manual: TUI /plan -> toolset + directive -> /build -> notice + mirror text; ACP set_mode round-trip; resume keeps mode.

