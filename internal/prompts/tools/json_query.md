Read, write, or delete a value in a JSON file using a dot-notation path. Supports objects and arrays: use `"store.books[0].title"` to access nested data, `action: "read"` to retrieve, `action: "write"` with `set` to modify, and `action: "delete"` to remove (deleting an array element splices it out). Ideal for editing configuration files (package.json, tsconfig.json, etc.) without needing to parse/stringify manually. Writes and deletes rewrite the whole file with 2-space indentation and sorted object keys, and the result says so; untouched regions do not keep their original formatting.

Writes and deletes are serialized in-process against other same-file writers,
so a same-file pair in one batch is ordered, not lost. A successful write or
delete ends with a receipt line — `wrote <file> (before -> after bytes,
sha256:<digest of the post-write content>)`. If the file changed since it was
read, the change is refused with both digests (`file changed by another
writer ...; re-read and retry`) and the file is left untouched.
