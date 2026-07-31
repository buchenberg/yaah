`bash(command, timeout)` — Run a shell command and capture its output.

Executes the given command in a Unix shell (sh/bash). `timeout` is an optional duration in seconds to bound execution; long-running or interactive commands may be killed when it elapses. Useful for building, testing, git operations, and any command-line work the host OS supports.

See also: `powershell` for the Windows PowerShell equivalent.
