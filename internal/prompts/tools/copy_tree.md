Recursively copy a directory tree inside the workspace. `path` is the source
directory and `to` is the destination; the destination is created (directories
and files) rather than requiring an empty target.

`force` replaces an existing destination tree; without it an existing `to` is
refused. A forced copy is staged beside the destination and the old tree is
removed only after the new copy succeeds, so a source read failure leaves the
old destination intact. The source and destination must not overlap: a
destination inside the source (recursion) or an ancestor of the source (a
forced replace would delete it) is refused. `mkdir_p` creates missing parent
directories of `to`; otherwise the parent must already exist (use `mkdir`).

Symlinks are refused at any depth: the tool copies regular files and
directories only, so it cannot silently follow a link out of the workspace or
produce a substrate-dependent result. Special files (FIFOs, sockets, devices)
are refused for the same reason.

A copy that fails part-way leaves the files already copied in place and returns
a non-nil error naming the failing path; the destination is not rolled back.
Prefer `move_tree` when the source should not remain, and `copy` for a single
regular file. The call writes, so it is approval-gated.
