Recursively move a directory tree inside the workspace. `path` is the source
directory and `to` is the destination. On the same filesystem the substrate
renames the whole tree without reading it; when that is refused (for example a
cross-device move), the tool copies the tree and removes the source, reporting
an error if the copy or the removal fails.

`force` replaces an existing destination tree; without it an existing `to` is
refused and the source is untouched. A forced move renames the old destination
aside as a backup, moves the source into place (the rename fast path, so
symlinks inside the tree move too), then removes the backup; if the move fails
the backup is restored. The source and destination must not overlap: a
destination inside the source or an ancestor of the source is refused.
`mkdir_p` creates missing parent directories of `to`; otherwise the parent must
already exist.

Symlinks and special files are refused at any depth, matching `copy_tree`.
Only a genuine cross-device rename falls back to copy + remove; any other
rename error is returned without touching the destination. A cross-device
fallback that copied successfully but could not remove the source returns an
error naming both paths — the destination exists, so inspect before retrying.
A same-filesystem rename is the cheap path; the copy fallback reads the bytes.

Use `move` for a single regular file. The call writes, so it is approval-gated.
