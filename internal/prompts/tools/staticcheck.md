Runs Go static analysis (`go vet` and/or `staticcheck`) on a package pattern and returns diagnostics grouped by file with severity (error/warning). `packages` resolves against the workspace's working directory, so use a relative pattern like `./...`.

`status` is `ok` only when every requested analyzer actually ran. `error` means one did not — a missing binary, an unusable toolchain, or a build failure surfaced as raw output. Zero diagnostics with `status: "error"` is not clean code.

Use for code review, catching bugs, and enforcing best practices.
