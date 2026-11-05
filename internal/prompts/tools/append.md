Append text to a file without reading or rewriting the existing content.
`filePath` is required and is created when missing; its parent directory must
already exist (create it with `mkdir` first). `content` is appended exactly as
given — no newline is added.

Appends use the substrate's append primitive (host `O_APPEND`, or an in-place
append in a sandbox), so two writers recording to the same log do not lose
each other's records. A single call's chunks from different writers may
interleave in a sandbox, but no data is lost; use one call per record for a
clean JSONL ledger.

This tool writes, so it is approval-gated like `write`. For a one-line status
record, prefer appending a complete JSON object rather than editing the whole
file.
