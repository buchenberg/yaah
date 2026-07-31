---
name: role-hot-reload
description: Wire ReloadDefaultRoles into the agent lifecycle so role definitions reload from disk without restart
status: in_progress
---

## Problem

`ReloadDefaultRoles()` is defined in `internal/agent/subagent/role.go` but has **zero callers**. It's dead code. Roles are loaded once at startup in `agent_frame.go:171` via `SetDefaultRoleRegistry(reg)` and never refreshed.

Similarly, `DefaultRegistry()` and `Roles()` are exported but have zero callers — nothing queries the live registry at runtime.

## What needs to happen

### 1. Add a reload trigger

Two options (do both):

- **Slash command** `/reload-roles` in the REPL — simplest, explicit, works everywhere
- **File watcher** using `fsnotify` on `.agents/roles/` and `~/.agents/roles/` — automatic, pick up edits immediately

### 2. Wire the reload

The reload path is:
```
trigger (slash cmd or fsnotify event)
  → subagent.ReloadDefaultRoles(ReloadDefaultRolesOptions{
        BuiltinFiles: builtinRoleFiles,  // already available at startup
        SearchDirs:   roleSearchDirs,    // already available at startup
    })
  → atomically swaps defaultRoleReg
  → new sub-agent dispatches pick up updated registry immediately
  → in-flight sub-agents unaffected (by design)
```

### 3. Expose via MCP (stretch)

Add a `reload_roles` tool to the MCP serve tool set so remote hosts can trigger it.

## Files to touch

| File | Change |
|------|--------|
| `cmd/yaah/repl_loop.go` | Add `/reload-roles` slash command |
| `cmd/yaah/agent_frame.go` | Expose `builtinRoleFiles` + `roleSearchDirs` for reload; or store on a struct |
| `internal/agent/subagent/role.go` | Already done — `ReloadDefaultRoles` exists |
| `cmd/yaah/serve.go` | Optional: expose `reload_roles` MCP tool |

## Acceptance criteria

- [ ] `/reload-roles` in the REPL reloads roles and prints a summary (N roles loaded, M from disk)
- [ ] New roles appear in `list_subagents` immediately after reload
- [ ] Removing a role file + reload removes it from available roles
- [ ] In-flight sub-agents are unaffected
- [ ] Tests pass
