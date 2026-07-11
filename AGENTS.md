# AGENTS.md — yaah

> Notes for AI coding assistants (and humans) working on the yaah codebase.

## What yaah is

yaah is a vendor-free AI agent harness. One Go static binary, minimal
config at `~/.yaah/`, skills at `~/.agents/` (cross-tool standard),
MCP over stdio for tool servers. See `README.md` for the user-facing
pitch and `../Markdown/agentic/yaah-plan.md` (in the author's note
vault) for the full v0.1 design plan.

## Repo layout (canonical)

```
yaah/
├── main.go                      # calls cmd/yaah.Execute()
├── go.mod
├── cmd/yaah/                    # cobra commands (root, version, …)
│   ├── root.go                  # build-time vars (version, commit, date)
│   ├── root_cmd.go              # rootCmd + runRoot (REPL/one-shot in M1)
│   └── version.go               # yaah version
├── internal/                    # all real implementation (future)
│   ├── config/                  # M1
│   ├── providers/               # M2
│   ├── agent/                   # M2
│   ├── tools/                   # M2
│   ├── mcp/                     # M4
│   ├── skills/                  # M3
│   ├── instructions/            # M3
│   ├── memory/                  # M5
│   ├── session/                 # M5
│   ├── permissions/             # M2
│   └── version/                 # future
├── scripts/                     # release, cross-compile
├── .github/workflows/ci.yml
├── README.md
├── CONTRIBUTING.md
├── AGENTS.md                    # this file
└── LICENSE                      # MIT OR Apache-2.0
```

The plan calls for everything real to live in `internal/` so the Go
compiler enforces "this code is for yaah only" — no accidental public
surface area for skill authors to depend on yet.

## Build & test

```bash
go build .                      # produces ./yaah
go test ./...                   # all tests
go vet ./...                    # vet
gofmt -l .                      # must be empty
go run honnef.co/go/tools/cmd/staticcheck@latest ./...   # staticcheck

# Cross-compile matrix (M0 acceptance test)
GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags '-s -w' -o dist/yaah-darwin-arm64  .
GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o dist/yaah-darwin-amd64  .
GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o dist/yaah-linux-amd64    .
GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags '-s -w' -o dist/yaah-linux-arm64    .
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o dist/yaah-windows-amd64  .
```

## Conventions

- **Go 1.22+** (per `go.mod`).
- **No codegen, no build tags, no `go generate`.** If you need
  something generated, write a script in `scripts/` and run it
  manually; commit the result.
- **cobra + pflag** for CLI. Don't pull in urfave/cli or kingpin.
- **`internal/` for everything private.** `pkg/` is reserved for
  something we might want to export later (none in v0.1).
- **No globals except the build-time vars in `cmd/yaah/root.go`**
  (`version`, `commit`, `date`). Everything else flows through
  dependencies passed explicitly to constructors.
- **Errors are values, not panics.** Reserve `panic` for "this is
  unrecoverable and indicates a programmer error." User input,
  network errors, file I/O errors, and config errors all return
  `error`.
- **No third-party HTTP client.** Use `net/http` from stdlib. If a
  test needs an HTTP fixture, use `httptest.NewServer`.
- **No third-party YAML library except `gopkg.in/yaml.v3`.** If the
  test suite pulls in another parser for fixtures, that's fine; the
  runtime config path uses yaml.v3 only.
- **No third-party SQLite library except `modernc.org/sqlite`.** Pure
  Go, no CGo, FTS5 + JSON1 included.
- **No model SDK.** Direct HTTP to `chat/completions`. If a provider
  needs a different wire format, write a thin adapter in
  `internal/providers/<name>.go`.

## Style

- Run `gofmt -w .` before committing. CI enforces `gofmt -l .` being
  empty.
- `go vet` must be clean.
- `staticcheck` must be clean.
- Prefer `for ... range` over index-based loops. Prefer early returns
  over nested `if/else`. No `else` after a `return`.
- One file, one concern. A package of 200 lines is fine. A file of
  1000 lines is not.
- Tests live next to the code they test (`foo.go` ↔ `foo_test.go`),
  not in a separate `tests/` package.
- Use `t.Run("name", func(t *testing.T) { ... })` for subtests; it
  makes failures greppable.

## Milestone status

- **M0 (this commit):** bootstrap. `yaah --version` prints
  `0.0.0`. Cross-compile matrix is in CI.
- **M1:** config + REPL + `yaah doctor` + `yaah update`. Not started.
- **M2:** providers + agent loop. Not started.
- **M3:** skills + instructions. Not started.
- **M4:** MCP client. Not started.
- **M5:** persistent memory. Not started.

## What NOT to do

- Don't add a `TUI/` package, a `web/` package, or anything that needs
  a frontend framework. v0.1 is CLI-only.
- Don't add a `gateway/` package or anything that talks to
  Telegram/Discord/Slack. Out of scope for v0.1.
- Don't add a "yaah cloud" or any hosted service. The project is
  vendor-free.
- Don't add Anthropic-specific features (1M context, prompt caching,
  computer use) unless via MCP. Out of scope for v0.1.
- Don't change the license. `MIT OR Apache-2.0` is the deal.
- Don't bump the version in a PR. Maintainers cut releases.
