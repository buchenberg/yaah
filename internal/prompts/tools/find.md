Search a workspace directory tree and return matching paths with size and
modification time, one `path<TAB>bytes<TAB>RFC3339` line each. `path` is the
directory to search (default the workspace working directory).

`name` is an optional glob matched against each entry's base name (`*.go`,
`test_*`). `type` selects `file` (default), `dir`, or `all`. `min_size` and
`max_size` are inclusive byte bounds; set only the bound you need. `newer_than`
keeps entries modified within the given duration (`24h`, `30m`), while
`older_than` keeps entries not modified within it. Durations are Go duration
strings: use hours for days (`168h`), because `d` is not a duration unit.

Symlinks are skipped, so a search cannot wander outside the workspace. All
matches are sorted before the first 1000 are returned, so the kept page is
lexicographically first rather than traversal order; a prefix marker reports
how many matched when the cap trims the list. The output is also capped at the
tool result limit. This is a read-only tool: use `glob` when you only need
names, and `find` when size or modification age is part of the query.
