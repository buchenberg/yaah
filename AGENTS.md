# AGENTS.md — yaah

> Notes for AI coding assistants (and humans) working on the yaah codebase.

## What yaah is

yaah is a vendor-free AI agent harness. One Go static binary, minimal
config at `~/.yaah/`, skills at `./.agents/` (project, walked up from cwd)
and `~/.agents/` (cross-tool standard), MCP over stdio and HTTP for tool
servers. See `README.md` for the user-facing pitch.

## Local-only directories

The following are local to a developer's working copy and are gitignored.
They are not part of the yaah repo and should never be committed:

- **`.scratch/`** — scratch space for AI coding assistants to clone
  third-party repos when building yaah skills. For example, building the
  `charm-bubbles` skill clones `github.com/charmbracelet/bubbles` here
  for reading. These clones are working copies only — the real upstream
  dependencies are pinned in `go.sum` as Go modules.
- **`.agents/`** — local user-authored skills (see "Skills" below).
- **`.ghost/`, `.claude/`, `.qwen/`, `.hermes/`, etc.** — metadata dirs
  for various AI coding tools. Never commit.

## Repo layout (canonical)

```
yaah/
├── main.go                      # calls cmd/yaah.Execute()
├── go.mod                       # Go 1.25+, cobra, modernc.org/sqlite
├── cmd/yaah/                    # cobra commands
│   ├── root.go                  # build-time vars (version, commit, date)
│   ├── root_cmd.go              # rootCmd, REPL, one-shot, agent wiring
│   ├── version.go               # yaah version
│   ├── config.go                # yaah config show/edit
│   ├── doctor.go                # yaah doctor
│   ├── update.go                # yaah update
│   ├── skill.go                 # yaah skill list/show
│   ├── mcp.go                   # yaah mcp list/add/remove
│   ├── memory.go                # yaah memory search/add
│   ├── session.go               # yaah session list/show
│   ├── tui.go                   # yaah tui (bubbletea)
│   └── color.go                 # ANSI color helpers
├── internal/
│   ├── agent/                   # agent loop (streaming, compaction, loop detection, truncation safety)
│   ├── banner/                  # figlet + lolcat banner for the TUI/REPL
│   ├── config/                  # load ~/.yaah/config.yaml, env subst
│   ├── instructions/            # walk up cwd, load AGENTS.md/CLAUDE.md
│   ├── mcp/                     # MCP client (stdio + HTTP), manifests
│   ├── memory/                  # SQLite + FTS5 (sessions, messages, memory)
│   ├── providers/               # OpenAI Chat Completions client, streaming
│   ├── repl/                    # REPL, history, slash commands, colors, banner
│   ├── skills/                  # SKILL.md discovery, frontmatter parsing
│   ├── spinner/                 # animated thinking spinner
│   ├── todo/                    # in-memory todo store
│   ├── tools/                   # built-in tools (read, write, edit, grep, glob, ls, bash, powershell, question, webfetch, task, background_process, memory, todo)
│   ├── tui/                     # bubbletea TUI (M7)
│   ├── types/                   # OpenAI message types
│   └── update/                  # GitHub release checking
├── .github/workflows/ci.yml     # CI: test, vet, staticcheck, cross-compile
├── README.md
├── CONTRIBUTING.md
├── SECURITY.md
├── AGENTS.md                    # this file
└── LICENSE                      # MIT OR Apache-2.0
```

## Build & test

```bash
go build .                      # produces ./yaah
go test ./...                   # all tests
go vet ./...                    # vet
gofmt -l .                      # must be empty
go run honnef.co/go/tools/cmd/staticcheck@latest ./...   # staticcheck

# Cross-compile matrix
GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags '-s -w' -o dist/yaah-darwin-arm64  .
GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o dist/yaah-darwin-amd64  .
GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o dist/yaah-linux-amd64    .
GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags '-s -w' -o dist/yaah-linux-arm64    .
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o dist/yaah-windows-amd64  .
```

## Install locally

```bash
go build -trimpath -ldflags '-s -w' -o yaah .
ditto --norsrc yaah ~/.local/bin/yaah  # macOS: avoids Gatekeeper quarantine
```

## Conventions

- **Go 1.25+** (per `go.mod`).
- **No codegen, no build tags, no `go generate`.**
- **cobra + pflag** for CLI.
- **`internal/` for everything private.** `pkg/` reserved for future exports.
- **No globals except build-time vars** in `cmd/yaah/root.go`.
- **Errors are values, not panics.**
- **No third-party HTTP client.** Use `net/http` from stdlib.
- **`gopkg.in/yaml.v3`** for config parsing.
- **`modernc.org/sqlite`** for SQLite (pure Go, no CGo, FTS5 included).
- **No model SDK.** Direct HTTP to `chat/completions`.

## Style

- Run `gofmt -w .` before committing.
- `go vet` must be clean.
- `staticcheck` must be clean.
- Prefer `for ... range` over index-based loops.
- Prefer early returns over nested `if/else`.
- One file, one concern.
- Tests live next to the code they test (`foo.go` ↔ `foo_test.go`).
- Use `t.Run("name", func(t *testing.T) { ... })` for subtests.

## What NOT to do

- Don't add a `web/` package, a `gateway/` package, or anything that talks to
  Telegram/Discord/Slack. Out of scope.
- Don't add a "yaah cloud" or any hosted service.
- Don't add Anthropic-specific features unless via MCP.
- Don't change the license. `MIT OR Apache-2.0` is the deal.
- Don't bump the version in a PR. Maintainers cut releases.
