`powershell(command, timeout)` — Run a PowerShell command and capture its output.

Executes the given command via PowerShell (pwsh). The command already starts in the working directory; do not re-enter it with `cd`, but `cd <subdir>` is fine when needed. `timeout` is an optional duration in seconds to bound execution. Useful on Windows hosts for build, test, and scripting work.

See also: `bash` for the Unix shell equivalent.
