---
name: architecture-review-remediation
description: "Remaining review remediation: driver bootstrap and the ongoing coverage rebalance."
status: draft
---

# Architecture review remediation — remaining work

The security, data-integrity, loop-integrity, config, and protocol phases of
the 2026-08-24 review shipped. This file tracks only the remaining structural
work and the ongoing coverage rebalance.

## 1. Driver bootstrap (Task 7.1)

`cmd/yaah` still has separate `runPrompt` and `runHeadless` paths that each
assemble a loop, and no `cmd/yaah/driver.go`.

- Define a small session driver that owns the shared sequence: snapshot
  session state, build the loop, run, update usage, forward control events.
- `runPrompt`, `runHeadless`, and serve become thin callers with their own
  view/approval policy.
- Preserve current behavior for REPL, one-shot, TUI, web, ACP, and MCP serve.

Acceptance: one bootstrap path; `go test ./...` and the three-platform CI
matrix pass; a smoke run of each interface still streams and reports usage.

## 2. Coverage rebalance (Phase 8, ongoing)

Add tests where the remaining risk is highest:

1. `internal/tools/patch.go` — exact, whitespace-tolerant, and near-miss
   hunks; multi-file diffs; near misses must not apply.
2. Provider HTTP layer — `httptest` fixtures for OpenAI and Anthropic
   `Send`/`SendStream`, auth/header construction, and typed API errors.
3. `internal/agent/llm` — fallback swap (including provider/model identity),
   overflow compaction, and degenerate-stream replay.
4. `internal/control` — message routing basics.
5. Hook rotation/size cap, or at minimum the lifecycle test.
6. `cmd/yaah/view_terminal.go` and web handlers beyond the auth tests.

Acceptance: each area has at least one happy-path and one failure-path test;
race-enabled CI stays green.

## Gates

```bash
go build ./...
go vet ./...
gofmt -l .
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
go test -shuffle=on -race ./... -count=1
```
