Duplicate a file inside the workspace, leaving the original untouched. `path` is the source file and `to` is the destination path.

The content is copied by the substrate itself, so the bytes are never decoded, re-encoded, or passed through the model — a binary or non-UTF-8 file arrives unchanged, and the source's permission mode is preserved. `path` and `to` are both resolved through the workspace path validator, so deny patterns and workspace-root containment apply exactly as they do for read, write, and delete.

Files only: a directory source is refused, and a destination that is an existing directory is refused. An existing destination is refused unless `force` is true, in which case it is replaced. The destination's parent directory must already exist — create it first. Copying a file needs approval like every other mutating file tool. To relocate the file instead of duplicating it, use `move`.
