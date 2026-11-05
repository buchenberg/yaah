Create a directory at `path`, including any missing parent directories. `path` is resolved through the workspace path validator, so deny patterns and workspace-root containment apply exactly as they do for read, write, and delete.

New directories are created with mode 0755. If the directory already exists it is left untouched and the result says so; if `path` exists as a regular file, the call is refused. Creating a directory needs approval like every other mutating file tool.

This is the explicit way to make a destination directory. `move` and `copy` require the destination's parent to exist, so create it here first when writing into a new nested path.
