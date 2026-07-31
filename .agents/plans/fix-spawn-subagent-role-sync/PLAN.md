---
name: fix-spawn-subagent-role-sync
description: Fix spawn_subagent stale role enum: use live registry lookup instead of cached RoleNames slice
status: approved
---

## Problem

`spawn_subagent` validates roles against `TaskTool.RoleNames`, a `[]string` captured once at startup. When roles are added/removed via the `role` tool, the live registry updates (so `list_subagents` sees changes instantly), but `spawn_subagent`'s cached slice is stale — rejecting new roles and accepting deleted ones.

## Root Cause

In `agent_frame.go:338`, `reg.Names()` is called once and the slice is passed to `newTaskTool`. The `TaskTool` stores it and never refreshes it. Meanwhile, the `role` tool calls `SetDefaultRoleRegistry()` which updates the live registry that `list_subagents` reads from.

## Fix

Add a `RoleResolver func() []string` field to `TaskTool`. When set, `Execute` uses it as the live source of truth (falling back to `RoleNames` for backward compat).

### Files to change

1. **`internal/tools/task.go`** — Add `RoleResolver` field; use it in `Execute` validation + error messages + schema
2. **`cmd/yaah/agent_frame.go`** — Pass `func() []string { return subagent.DefaultRegistry().Names() }` as `RoleResolver`

### Files to verify

- `cmd/yaah/subagent_runner.go` — uses `TaskTool` without `RoleNames`; no change needed
- `internal/tools/task_test.go` — add test for live role resolution

## Effort

~30 lines of code across 2-3 files. Low risk, backward compatible.
