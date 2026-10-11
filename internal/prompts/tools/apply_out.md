Apply the session's isolated-workspace changes onto the host working tree.

This session runs in an isolated workspace: every file and command executed so far
happened inside a container, and the host repository has not been touched. This
tool is the single door changes leave the container through — there is no other
path, and the host tree stays untouched until it runs.

- Set `dry_run: true` to preview what would be applied (the diff since the
  session baseline) without touching the host. Always do this before applying.
- Without `dry_run`, the session's delta is applied onto the host working tree
  and the changes become real. This action requires user approval — and a dry
  run must have run first in this session, with no workspace changes since: the
  approval prompt shows only the arguments, so the user approves exactly the
  diff the dry run showed them.

When the user accepts the work ("ship it", "apply the changes", "looks good"),
call this tool without `dry_run`. If the user asks what changed, or you want to
verify before proposing to apply, call it with `dry_run: true`.

Applying is not a commit: the changes land in the host's working tree exactly
as they stand in the container; reviewing and committing them stays with the
user's normal git flow.
