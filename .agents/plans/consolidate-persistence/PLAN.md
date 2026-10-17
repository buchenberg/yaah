---
name: consolidate-persistence
description: "Remaining persistence work: a unified session CLI over sessions and Shepherd traces."
status: draft
---

# Persistence consolidation — remaining work

Sessions, memories, Shepherd traces, and OTel spans remain separate stores by
design. The cross-link phase shipped: messages carry `trace_id`/`turn_id`,
OTel turn spans carry session and turn identifiers, and `trace_owners` maps
Shepherd owners to parent sessions.

## Remaining: unified session CLI

Today `yaah session` and `yaah shepherd-trace` expose different views of the
same work. Add one read-only CLI surface that joins them:

```bash
yaah session list                 # sessions + trace owners + trace availability
yaah session show <id>            # messages + tool calls + trace ownership
yaah session profile <id>         # turns, tokens, tool stats, success rates
```

- Keep `yaah shepherd-trace list|show|profile` as aliases for compatibility.
- Read from the existing stores; do not migrate data or merge SQLite files.
- If a trace store is unset, the session views still work and say so.

Acceptance: the three commands work with and without `shepherd_trace_dir`;
`shepherd-trace` output is unchanged; no new store or background process.

## Deferred

- **Co-locating the Shepherd store with `state.db`** is blocked until the
  kernel exposes a `TraceStore` interface (it currently owns its SQLite
  layout). Revisit only if that interface ships.
- **Token-storage deduplication** is not worth the migration: sessions are the
  unconditional source, OTel usage is per-span, and Shepherd facts are
  append-only.
- **WebSocket migration** of the web UI is out of scope; SSE is the transport.
