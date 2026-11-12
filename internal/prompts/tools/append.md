Append text to a file without reading or rewriting the existing content.
`filePath` is required and is created when missing; its parent directory must
already exist (create it with `mkdir` first). `content` is appended exactly as
given — no newline is added.

Appends use the substrate's append primitive (host `O_APPEND`, or an in-place
append in a sandbox), so two appends recording to the same log do not lose
each other's records — the guarantee is append-versus-append only. A single
call's chunks from different writers may interleave in a sandbox, but no data
is lost; use one call per record for a clean JSONL ledger.

The call also holds the per-path write lock for its whole duration, so
appends are serialized in-process against same-file rewrites: an append
cannot land inside a rewrite's compare-and-swap window and make that write
look stale. Success ends with a receipt line, `appended to <filePath>
(+N bytes)`, naming the byte count added; the digest the other write
receipts carry is omitted here for cost — appends cannot lose each other, so
a post-write digest would be a full read of the file for no diagnostic gain.
A rewrite whose file changed since it was read is refused with both digests
(`file changed by another writer ...; re-read and retry`), leaving that file
untouched — which is why the batching note below still stands.

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
