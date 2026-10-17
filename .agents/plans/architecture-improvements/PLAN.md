# Architecture improvements — remaining work

Most of this plan shipped: event exhaustiveness, loop single-use, parent
permission enforcement, bounded reasoning retries, broker recreation, the
session options struct, typed provider errors, control-plane type separation,
and the delegate options struct. This file tracks only the work that remains.

## 1. Extract trace query formatting

`cmd/yaah/trace.go` is still ~550 lines and mixes flag parsing, store access,
aggregation, and table rendering.

- Move store querying and aggregation into an internal package next to the
  trace store (`internal/memory` or a new `internal/shepherd` — follow the
  store's home).
- Keep flag parsing and terminal rendering in `cmd/yaah`.
- No behavior change; the CLI output must remain byte-identical for the
  existing fixtures.

Acceptance: `go run . shepherd-trace list`, `show <id>`, `show --latest`, and
`profile <id>` produce the same output; `go test ./...` passes.

## 2. Extract MCP YAML editing

The `yaah mcp add/remove` commands still mutate YAML in `cmd/yaah/mcp.go`.

- Add `internal/config/mcp_edit.go` with `AddMCPServer` and
  `RemoveMCPServer`, preserving comments and unrelated keys where practical.
- Keep confirmation and UX in `cmd/yaah`.

Acceptance: add/remove round-trips a config with stdio and HTTP servers; the
result loads without validation errors.

## 3. Extract skill scaffolding

`yaah skill create` still owns the SKILL.md template and directory creation.

- Add `internal/skills/scaffold.go` with `CreateSkill(dir, name, description)`.
- Keep prompts and output formatting in `cmd/yaah`.

Acceptance: `yaah skill create` produces a loadable skill with valid
frontmatter; duplicate names are refused.

## 4. Finish the shared control-plane driver

`cmd/yaah/control_driver.go` has the shared helpers, but TUI, web, and serve
still assemble question/approval plumbing independently.

- One driver owns the channel round-trip, timeout, cancellation, and cleanup.
- Transports supply only delivery: TUI `QueueUpdateDraw`, web SSE + HTTP
  response, headless serve auto-timeout policy.
- Give TUI the same bounded wait the web path uses.

Acceptance: a question and an approval can be answered from the TUI, the web
UI, and a headless client through the same driver; no goroutine leaks on
disconnect or cancellation.

## 5. Remove the remaining `config.HomeDir` reach-in

`internal/agent/runner/runner.go:238` still calls `config.HomeDir()`.

- Inject the resolved home (or the specific role-search path) through
  `taskRunnerOpts` from the composition root.
- Keep the runner free of config path resolution.

Acceptance: `grep -rn "config.HomeDir" internal/agent/runner` is empty; role
discovery still works for project and user roles.

## Gates

```bash
go build ./...
go vet ./...
gofmt -l .
go test ./... -count=1
```
