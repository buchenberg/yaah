Executes a shell command using `sh -c` and returns its combined stdout/stderr. Use for quick CLI operations: file inspection, package management, build commands, git operations. Output is truncated if very large. Set `timeout` to control the maximum runtime (default 30s).

**Dangerous commands** (rm, sudo, chmod, etc.) are pattern-blocked unless approval gating is enabled. Prefer safer alternatives (`git`, `read`, `write`, `ls`, `glob`) when available.
