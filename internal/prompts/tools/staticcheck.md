Runs Go static analysis (`go vet` and/or `staticcheck`) on a package pattern and returns diagnostics grouped by file with severity (error/warning). `packages` resolves against the workspace's working directory, so use a relative pattern like `./...`.

`status` is `ok` only when every requested analyzer actually ran on a compilable package. `error` means one did not: a missing binary, an unusable toolchain, or a build failure — in which case the compiler output is preserved verbatim in `stdout` and is not reported as diagnostics. Zero diagnostics with `status: "error"` is not clean code.

Use for code review, catching bugs, and enforcing best practices.
