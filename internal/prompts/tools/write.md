`write(content, filePath)` — Creates or overwrites a file. Use `edit` for partial changes to an existing file.

Writes are confined to the workspace root. Paths outside it (including the system temp directory) are refused with the rejected path and the allowed root; use `.scratch/` inside the workspace for disposable files.
