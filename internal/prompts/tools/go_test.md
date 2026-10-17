Runs Go tests with structured JSON output: `go_test(packages, timeout_seconds?, flags?, coverprofile?)`.

`packages` is resolved against the **workspace's** working directory — your worktree, or the session directory — so use a relative pattern such as `./...` or `./internal/tools/...`. An absolute path works only when it points into this module; a path into another checkout (including the repository this worktree came from) fails with "directory prefix … does not contain main module".

Read `status` before the counts:

- `ok` — the run completed and nothing failed. A package with no test files is legitimately 0 tests.
- `failed` — the run completed and something failed: `failed_tests` names failing tests, and `failed_builds`/`build_output` name a package that did not compile while other packages still ran.
- `error` — there is no verdict: nothing ran (`-c` only compiles, `-json=false` emits text this tool cannot parse, a pattern matched no packages), a build failed before any test reported, or the run was killed or timed out (`timed_out: true`, partial counts kept).

Never read `passed: 0, failed: 0` as green without checking `status`; `raw_output` carries the stream tail when there is one. A toolchain that cannot start at all (the `go` binary is missing or unrunnable) is a tool error, not a JSON result.

Also returns per-package counts, timing, and optionally coverage.
