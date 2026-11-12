`replace(pattern, replacement, path, include?, dry_run?)` — Recursively finds and replaces using Go regex syntax across files in a directory. Supports capture groups (`$1`, `$2`). A per-file read or write failure is reported before the successful replacements and makes the whole call an error.

Same-file writes are serialized in-process, so a same-file pair in one batch
is ordered, not lost. Each written file's line ends with a receipt folded in
after the match count — the byte delta and `sha256:<12 hex of the post-write
content>`. A file that changed since the run read it is refused with both
digests (`file changed by another writer ...; re-read and retry`) and left
untouched; the run continues with the remaining files.
