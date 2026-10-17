Runs Go tests with structured JSON output: `go_test(packages, timeout_seconds?, flags?, coverprofile?)`.

`packages` is resolved against the **workspace's** working directory — your worktree, or the session directory — so use a relative pattern such as `./...` or `./internal/tools/...`. An absolute path works only when it points into this module; a path into another checkout (including the repository this worktree came from) fails with "directory prefix … does not contain main module".

Read `status` before the counts:

- `ok` — the run happened and nothing failed. A package with no test files is legitimately 0 tests.
- `failed` — tests ran and some did not pass; `failed_tests` names them.
- `error` — the run did not happen: the toolchain could not start, a package did not build, no test events could be parsed, or the time limit killed it. Never read `passed: 0, failed: 0` as green when `status` is `error`; the cause is in `build_output`, `raw_output`, `stderr`, and `exit_code`.

Also returns per-package counts, timing, and optionally coverage.
