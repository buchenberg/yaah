`read(filePath, offset, limit)` — Read a text file from disk.

Reads the file at `filePath`. `offset` (byte offset) and `limit` (max bytes) optionally slice the read for large files. Returns file contents along with metadata; fails if the file does not exist.

See also: `ls` to list directories, `file_info` for metadata without content.
