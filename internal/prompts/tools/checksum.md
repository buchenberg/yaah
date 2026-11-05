Compute SHA-256 digests for one or more files without reading their content
into context. `paths` is the list of absolute file paths to hash; every file is
resolved through the workspace path validator and must be a regular file.

The result is one `<digest>  <path>` line per file, in the order supplied. With
`compare: true`, `paths` must contain exactly two files: the same two lines are
returned plus `equal: true` or `equal: false`, so a transfer or duplicate check
is one call and one short result.

Hashing is read-only. Use it after `copy`/`copy_tree` to verify a transfer, or
to check whether two files are identical before deciding to keep both. It
hashes content, not metadata; two files with the same bytes but different
modes compare equal.
