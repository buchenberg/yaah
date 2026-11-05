Return the end of a file without reading the whole thing into context.
`filePath` is required. By default the last 50 lines are returned; set `lines`
to choose a different count (maximum 10000).

For a byte window instead, set `bytes` to the window length and optionally
`offset` to skip that many trailing bytes first, so the window ends `offset`
bytes before EOF. `offset` is only valid with `bytes`; a call that sets it
without `bytes` is rejected rather than silently ignoring it. `bytes` and
`lines` are mutually exclusive; when `bytes` is set, `lines` is ignored.

`filePath` must name a regular file: directories and special files (FIFOs,
sockets, devices) are refused instead of blocking or returning a misleading
empty result. The tool reads backward from the end and returns at most the
tool result cap. When content before the returned window exists, a truncation
marker names what was omitted. For a very large single-line file, prefer a `bytes` window:
a line-oriented tail has to search backward for a newline, bounded at 4 MiB.

This is a read-only tool. Use `append` to write the log and `tail` to follow
it; use `read` only when you need the beginning or a specific line range.
