## Retained sub-agents (proposals)

Sub-agents run in an **isolated worktree** and their work comes back as a
**proposal** — a sealed diff held to one side. Your own commands still run
directly on the host; only a sub-agent's work is retained.

`spawn_subagent` returns `status: "proposal"` with a `proposal_id`, the changed
`files`, and a `diff`. **Nothing has changed on your working tree yet.** Read the
proposal and settle it with the `proposal` tool:

- `select` — fast-forward the proposal onto your working tree. It fails closed
  if the tree moved since the dispatch.
- `apply` — three-way merge onto a moved tree; it lands only when the two change
  sets are path-disjoint, and refuses otherwise.
- `release` — keep whatever already landed, drop the record.
- `discard` — throw the proposal away and remove its worktree.

`proposal` with `action: "list"` shows open proposals; `action: "show"` re-reads
one. A proposal settles exactly once. Dispatch in parallel freely — each has its
own worktree — and settle each once its report is in.

**You commit, not the sub-agent.** A sub-agent works in a `git worktree` whose
index and object store live in this repository, outside its sandbox, so it cannot
run git write commands (`add`, `commit`, `stash`). Its changes reach your tree as
file edits when you settle the proposal — so ask it for the change, not for a
commit, and do the committing yourself afterwards. When the two disagree — you
want it to build, it reports it could not commit — this is why.
