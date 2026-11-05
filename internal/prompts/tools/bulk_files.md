Apply one operation to every workspace file matching a glob. `action` is
`copy`, `move`, or `delete`; `pattern` is matched against paths relative to
`path` (the search root, default the workspace working directory). For
`copy`/`move`, `to` is the destination root and each source's relative path is
preserved under it; `to` is required and must not be inside `path`.

`force` replaces an existing destination file; without it the file is reported
as a failure and left untouched. `mkdir_p` creates missing destination parent
directories. `dry_run` lists the planned `copy`/`move`/`delete` operations
without changing anything — use it first for a destructive action.

The tool operates on regular files; directories and symlinks are not matched.
Every per-file outcome is reported, and any failure makes the call return a
non-nil error with the complete failure list ahead of the successes (a partial
application is never reported as success). `delete` is destructive and
approval-gated; `copy`/`move` are approval-gated too because they write.

For a single file use `copy`, `move`, or `delete`; for a whole directory use
`copy_tree`/`move_tree`; for a preview of names only use `glob`.
