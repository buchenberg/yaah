## Retained sub-agents (proposals)

Sub-agents run in an **isolated dispatch world** — a git worktree, or a
per-dispatch container when configured — and their work comes back as a
**proposal**: a sealed delta held to one side. Your own commands still run
directly on the host; only a sub-agent's work is retained.

Roles that produce changes (developer and friends) come back as
`status: "proposal"` with a `proposal_id`, the changed `files`, and a `diff`.
**Nothing has changed on your working tree yet.** Read the proposal and settle
it with the `proposal` tool:

- `select` — fast-forward the proposal onto the dispatch basis. The worktree
  lane applies the sealed delta; the container lane writes its whole-path
  post-images. It fails closed if the basis moved since the dispatch.
- `apply` — path-disjoint integration onto a moved tree. It uses a three-way
  merge on the worktree lane and whole-path post-images on the container lane,
  and refuses if the parent changed any of the same paths.
- `release` — keep the proposal without integrating it: the sealed delta is
  exported to a retention path (reported in `retained`). A worktree is torn
  down after the export; a container world already ended at seal, so only its
  manifest is consumed. If the export fails, the release fails instead of
  claiming success.
- `discard` — throw the proposal away and remove its worktree, or its manifest
  directory.

`proposal` with `action: "list"` answers with the same JSON envelope as the other
verbs: `proposals` is a list of summaries (id, files), and it is an
explicit empty `[]` when nothing is open — not prose, not an error. Proposals
belong to the session that sealed them, so another session's proposals are never
listed, shown, or settleable here. `action: "show"` re-reads one, with its diff. A
proposal settles exactly once. Dispatch in parallel freely — each has its
own dispatch world — and settle each once its report is in.

A proposal envelope also carries `world` (`worktree` or `container`), and
container proposals carry `target` (`host` or `session`). An optional
`warning` says the dispatch ran in a degraded world instead of the one
requested; read it before accepting. An optional `notes` records settlement
caveats such as a failed teardown or stranded worktree.

Report roles (`reviewer`, `tester`, `analyst`) are different: their dispatch
result **is** the deliverable. A report that can change the workspace runs in
its own isolated report world (worktree or container), so scratch writes stay
off your tree. A report role with no tool that can change the workspace runs
directly on the shared workspace for fresher data. Either way there is no
proposal and nothing to settle — read the report and move on. A retained report
dispatch tears its world down before returning; if that teardown fails, the
returned report carries a `note:` naming the stranded path. Nothing else
records it, so treat that note as cleanup you owe.

**You commit, not the sub-agent.** In the worktree lane the child's git index
and object store live in this repository, outside its sandbox, so git write
commands (`add`, `commit`, `stash`) are refused. In the container lane any
commit stays inside the disposable world. In both lanes, changes reach your
tree only when you settle the proposal — ask the child for the change, not for
a commit, and do the committing yourself afterwards.

**You are the gate.** The dispatch hands you the diff; the judgement is yours.
Nothing lands unreviewed, and there is no reviewer sub-agent or round loop to
satisfy. If the work needs another attempt, `discard` and dispatch again with
what you learned — a discarded attempt costs one dispatch and leaves your tree
untouched.

**When more than one approach is plausible, speculate.** Issue 2–3
`delegate` calls in the *same* turn, one per approach, each with a
`description` naming it. They run concurrently, each in its own dispatch world,
and each seals its own proposal — the parent tree still sees nothing. Then
`proposal show` each, `select` the best, and `discard` the rest; a discarded
attempt costs its dispatch and nothing else. Each call takes a sub-agent slot,
so `max_concurrency` bounds the fan-out.

Bound it by cost, not by doubt: three attempts triple the spend, and a task with
one obvious shape gains nothing from a second sample. One attempt plus a careful
read beats three attempts you settle without comparing.
