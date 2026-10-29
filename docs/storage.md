# Storage model: `state.db` and `trace.db`

yaah has two durable stores with different jobs. They overlap in content, and
that overlap is intentional. They are **not replicas** and are not required to
be byte-identical.

| | `state.db` | `trace.db` |
|---|---|---|
| **Role** | Active conversation and retrieval model | Append-only evidence, causal record, and recovery source |
| **Default path** | `~/.yaah/state.db` (or `$YAAH_HOME/state.db`) | `~/.yaah/trace.db` (configurable with `shepherd_trace_path`) |
| **Primary consumer** | Resume, session picker, history, FTS5, embeddings | `shepherd-trace`, `proposal`/supervision provenance, contract recovery, `rebuild` |
| **Mutation** | Positional rows are rebased/truncated on compaction or restore; sessions can be deleted | Append-only; facts are never rewritten |
| **Message text** | The active rows the next Run resumes from | A content-bearing transcript mirror, capped at 64KiB per free-text field |
| **Sub-agent work** | Not persisted | Persisted under a synthetic owner; the parent mapping is in `trace_owners` |
| **Failure mode** | Writes are debounced; a hard crash can drop buffered rows | Append intents and content facts are durable before the process advances |
| **Deletion** | `sessions delete` removes session/message rows and stale `trace_owners` rows | No owner-deletion API; facts remain |

## What is deliberately duplicated

For a main-session turn, both stores may contain:

- the system prompt, cwd, and model (`sessions` / `session:context`)
- the user prompt (`messages` / `turn:created`)
- assistant content, reasoning, signature, and tool calls
  (`messages` / `assistant:message`)
- tool arguments (`messages.tool_calls` / trace declarations)
- tool output (`messages` / `tool:<name>:result`)
- token counts (`sessions` totals / `turn:completed`)

This is the cost of crash-safe evidence: trace writes its own content before
`state.db`'s debounced message write can be lost. The duplication is not a
cache; it is a **write-ahead/audit tier beside a mutable read model**.

## What is not duplicated

- **Sub-agent intermediate messages** exist only in `trace.db`. `state.db`
  intentionally skips sub-agent persistence; only the final delegate result
  reaches the parent transcript.
- **Causal structure** — fact edges, owner scopes, frontiers, contexts,
  witness refs, retained context — exists only in trace.
- **Retrieval and product state** — FTS5, vector embeddings, pagination,
  titles, session ordering, compaction state, deterministic message IDs —
  exists only in `state.db`.
- **After compaction**, `state.db` has deleted/replaced active rows while trace
  still holds the earlier facts. The two stores then describe different views
  of the same session on purpose.

## Fidelity limits

`trace.db` is a recovery/evidence store, not a backup of every byte:

- the transcript content fields introduced by `yaah-o86` — session/system
  prompt, assistant content, reasoning, signature, refusal, the assistant
  tool-call argument copies, and tool output — are capped at 64KiB and carry
  `*_truncated` markers;
- oversized tool output carries the loop's `output_spill` path, not a second
  copy of the full body; the declaration `args` payload keeps its existing
  schema and is not covered by this cap, so trace is not a byte-exact backup
  of every field;
- trace records a provider-shaped conversation (`rebuild`) but does not carry
  every `state.db` concern: titles, embeddings, FTS index, session ordering, or
  the compaction working set.

`yaah shepherd-trace rebuild <session>` can reconstruct a transcript from trace
facts alone. It does not replace a resume: resume still reads `state.db`.

## Retention and deletion

`yaah sessions delete` currently removes the session, its messages, and stale
`trace_owners` mappings. It does **not** remove the trace facts for that owner:
the kernel trace store is append-only and exposes no owner-deletion operation.

Do not treat session deletion as trace redaction. The trace remains evidence
for its owner and for supervision/recovery. Retention, redaction, and possible
deduplication are tracked in `yaah-1e6g`; any future owner-deletion API must
preserve causal consistency (contexts, frontiers, witnesses) rather than
deleting rows behind the store's back.

## Why not one store yet?

A single physical store would be simpler, but the current split is not just
legacy:

- the kernel owns its SQLite layout and does not expose the `TraceStore`
  interface needed for co-location;
- the kernel has no FTS/vector query path, session pagination, or read model
  that `state.db` provides;
- `state.db` compaction is destructive/rebase-based, while trace is
  append-only and cut-based;
- sub-agent transcripts have no `state.db` home at all.

The realistic future options are a shared content-addressed body store (both
stores keep metadata and reference the same bodies) or co-located logical
schemas with `state.db` as a projection over trace facts. Both need a
retention/GC contract and an upstream storage seam. They are intentionally
deferred to the investigation above rather than attempted as a schema merge.

## Practical rule

- Need to **resume or search** a session: `state.db`.
- Need to know **what happened, in what order, with which authority**, or
  recover an interrupted transcript: `trace.db`.
- Need to **delete** user data: remove the state session, and treat trace
  retention as an open item until `yaah-1e6g` lands an owner-deletion/redaction
  path.
