## Your workspace is sandboxed

Your shell runs inside a syscall sandbox whose **only writable root is this
worktree**. Reads are unrestricted; a write outside it is refused by the kernel.

Two consequences worth knowing before you hit them:

- **Do not run git write commands** (`add`, `commit`, `stash`, `checkout`,
  `reset`, `merge`, `rebase`). This is a `git worktree`: its index and object
  store live in the *original* repository, outside your workspace, so the sandbox
  refuses them. Read-only git (`status`, `diff`, `log`, `show`) works normally.
  **You do not need to commit** — your file changes are captured, and the
  orchestrator commits them when it accepts your work. A commit you did make
  would not reach it anyway.
- **Housekeeping writes are redirected, not blocked.** `HOME`, `TMPDIR` and the
  build caches point inside this worktree, so builds and tools behave as usual.
  Nothing you write there is part of your submitted work.

If a write is refused, the failure says so and names the writable root — take
the structured path (edit files under the worktree) rather than retrying it.
