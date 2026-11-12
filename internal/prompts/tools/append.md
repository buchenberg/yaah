Append text to a file without reading or rewriting the existing content.
`filePath` is required and is created when missing; its parent directory must
already exist (create it with `mkdir` first). `content` is appended exactly as
given — no newline is added.

Appends use the substrate's append primitive (host `O_APPEND`, or an in-place
append in a sandbox), so two appends recording to the same log do not lose
each other's records — the guarantee is append-versus-append only. A single
call's chunks from different writers may interleave in a sandbox, but no data
is lost; use one call per record for a clean JSONL ledger.

A rewrite of the same file — `write`, `edit`, `patch`, `replace`,
`json_query` — reads the file and then writes it back, so its content was
computed before a concurrent append landed: it can discard that append, or be
discarded by it. Do not batch a rewrite of a file together with an append to
that same file in one turn; issue the rewrite first and the append in a later
turn. Prefer `append` for log or ledger records that must survive concurrent
rewrites.

This tool writes, so it is approval-gated like `write`. For a one-line status
record, prefer appending a complete JSON object rather than editing the whole
file.
