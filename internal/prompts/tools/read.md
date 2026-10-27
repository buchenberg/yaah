`read(filePath, offset, limit)` — Read a text file from disk.

Reads the file at `filePath`. `offset` (1-based line number to start from) and `limit` (maximum lines to return) optionally slice the read for large files. Returns the file's text; when the slice stops early, a truncation marker follows the last returned line. Fails if the file does not exist.

See also: `ls` to list directories, `file_info` for metadata without content.
