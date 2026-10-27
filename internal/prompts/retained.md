## Retained sub-agents (proposals)

Sub-agents run in an **isolated worktree** and their work comes back as a
**proposal** — a sealed diff held to one side. Your own commands still run
directly on the host; only a sub-agent's work is retained.

Roles that produce changes (developer and friends) come back as
`status: "proposal"` with a `proposal_id`, the changed `files`, and a `diff`.
**Nothing has changed on your working tree yet.** Read the proposal and settle
it with the `proposal` tool:

- `select` — fast-forward the proposal onto your working tree. It fails closed
  if the tree moved since the dispatch.
- `apply` — three-way merge onto a moved tree; it lands only when the two change
  sets are path-disjoint, and refuses otherwise.
- `release` — keep the proposal's change without integrating it: the sealed
  delta is exported to a retention path (reported in `retained`) and the
  worktree is torn down. If the export fails, the release fails instead of
  claiming success.
- `discard` — throw the proposal away and remove its worktree.

`proposal` with `action: "list"` answers with the same JSON envelope as the other
verbs: `proposals` is a list of summaries (id, files, worktree), and it is an
explicit empty `[]` when nothing is open — not prose, not an error. Proposals
belong to the session that sealed them, so another session's proposals are never
listed, shown, or settleable here. `action: "show"` re-reads one, with its diff. A
proposal settles exactly once. Dispatch in parallel freely — each has its
own worktree — and settle each once its report is in.

Report roles (`reviewer`, `tester`, `analyst`) are different: their dispatch
result **is** the deliverable. They run in their own isolated worktree (their
scratch never touches your tree), but there is no proposal and nothing to
settle — read the report and move on. A report dispatch tears its worktree down
before returning; if that teardown fails, the returned report carries a `note:`
naming the stranded path. Nothing else records it, so treat that note as cleanup
you owe.

**You commit, not the sub-agent.** A sub-agent works in a `git worktree` whose
index and object store live in this repository, outside its sandbox, so it cannot
run git write commands (`add`, `commit`, `stash`). Its changes reach your tree as
file edits when you settle the proposal — so ask it for the change, not for a
commit, and do the committing yourself afterwards. When the two disagree — you
want it to build, it reports it could not commit — this is why.

**You are the gate.** The dispatch hands you the diff; the judgement is yours.
Nothing lands unreviewed, and there is no reviewer sub-agent or round loop to
satisfy. If the work needs another attempt, `discard` and dispatch again with
what you learned — a discarded attempt costs one dispatch and leaves your tree
untouched.

**When more than one approach is plausible, speculate.** Issue 2–3
`delegate` calls in the *same* turn, one per approach, each with a
`description` naming it. They run concurrently, each in its own worktree, and each
seals its own proposal — the parent tree still sees nothing. Then `proposal show`
each, `select` the best, and `discard` the rest; a discarded attempt costs its
dispatch and nothing else. Each call takes a sub-agent slot, so
`max_concurrency` bounds the fan-out.

Bound it by cost, not by doubt: three attempts triple the spend, and a task with
one obvious shape gains nothing from a second sample. One attempt plus a careful
read beats three attempts you settle without comparing.
