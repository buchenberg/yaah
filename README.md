# yaah

**A vendor-free agent harness for orchestrating a team of AI coding agents.**

Yaah ships as a single Go binary. It runs an orchestrator agent that can work
directly, or dispatch specialist sub-agents that research, implement, test,
and review in parallel. Sub-agents can run in isolated git worktrees and
return sealed proposals, so the orchestrating agent reviews a diff before
anything reaches your working tree.

It speaks any OpenAI-compatible API or the Anthropic Messages API, keeps
sessions and memory in local SQLite, and speaks MCP as both a client and a
server. No accounts, telemetry, or hosted service required.

## Why orchestration

A single agent doing a large task spends most of its context reading files,
running tests, and retrying. yaah splits that work:

- **The orchestrator plans, judges, and lands.** It keeps the main conversation focused on the task and the review.
- **Sub-agents do the long-running work.** Each role gets a focused tool set, its own iteration/turn budget, and a structured evidence contract.
- **Parallel by default.** Independent research, implementation, and validation can run at the same time.
- **Isolated by construction.** A dispatch runs in its own git worktree and returns a sealed proposal (`select`, `apply`, `release`, or `discard`). A rejected attempt costs one dispatch and leaves the tree untouched.
- **Speculative by design.** The orchestrator can compare several proposals and land the best one; nothing is committed by a sub-agent.

Four roles are built in: `analyst`, `developer`, `tester`, and `reviewer`. Add your own in `.agents/roles/*.md` or `~/.agents/roles/*.md`.

## Install

macOS / Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/buchenberg/yaah/main/install.sh | sh
```

Windows:

```powershell
iwr -useb https://raw.githubusercontent.com/buchenberg/yaah/main/install.ps1 | iex
```

From source (Go 1.26+):

```bash
go install github.com/buchenberg/yaah@latest
```

Docker with OTLP tracing:

```bash
export DEEPSEEK_API_KEY=sk-...
docker compose --profile cli build
docker compose --profile cli run --rm yaah "explain this codebase"
```

Traces are exported to the OTLP endpoint at `host.docker.internal:4318`. Run
SigNoz (or another OTLP backend) separately and open its UI; the compose file
does not start one.

## Quick start

```bash
yaah doctor                 	# check providers, tools, and local state
yaah config edit            	# add a provider and API key
yaah "explain this repo"    	# one-shot
yaah                        	# interactive REPL
yaah tui                    	# full-screen terminal UI
yaah web                    	# browser UI on port 8080
yaah web --addr 127.0.0.1:8081 # specify a port
```

The default config lives at `~/.yaah/config.yaml` (or
`$YAAH_HOME/config.yaml`). A scaffold is written on first run. See [configuration.md](./docs/configuration.md).

## Orchestration examples

### Parallel investigation

```bash
yaah "Work out how context compaction is triggered. Dispatch an analyst to trace the code path and a tester to find existing coverage, then summarize both with file references."
```

The orchestrator sends both dispatches in one turn. Each sub-agent returns a structured report; the orchestrator synthesizes them instead of filling its own context with raw file reads.

### Implement, test, and review as one wave

```bash
yaah "Add a per-model max_tokens override to provider config. Have a developer implement it, a tester run the affected suites, and a reviewer inspect the diff. Do not call it done until the tests pass."
```

Quality gates can encode this directly:

```yaml
agents:
  quality_gates:
    developer: [tester]
```

### Speculate on an approach

```bash
yaah "We need faster session search. Dispatch two developers with different designs in parallel, compare their proposals, and apply the one with the smaller diff."
```

With retained mode active, each dispatch is a separate worktree. The orchestrator reads both proposals (diff, changed files, status) and calls `proposal select` or `proposal apply` on the winner; the other is discarded. Neither attempt touched your working tree until settlement.

### Add a specialist role

```bash
mkdir -p .agents/roles
cat > .agents/roles/security-auditor.md <<'EOF'
---
name: Security Auditor
specialty: security
description: Scans for secrets, unsafe patterns, and injection vectors
tools: [read, grep, glob, ls, bash]
max_iterations: 30
max_turns: 10
timeout: 180
---

You are a security auditor. Report findings with file paths, line numbers,
and severity. Do not modify files.
EOF

yaah "Audit the authentication surface with the security-auditor role."
```

Built-in roles cannot be shadowed; custom roles are additive. The full frontmatter reference and the full evidence/interpretation contract are in [sub-agents.md](./docs/sub-agents.md).

### Drive yaah from another agent

`yaah serve` exposes yaah as an MCP server (`prompt`, `traces`, `status`, `steer`, `compact`), so another agent can run multi-turn tasks and inspect the traces. This is mostly used for testing and developing yaah itself.

```bash
yaah serve --http 127.0.0.1:7333
```

`yaah acp-serve` exposes the ACP JSON-RPC server over stdio. Both are useful for automation and agent-to-agent coordination.

## How a retained dispatch works

```
orchestrator
  └─ delegate(role, task)
       ├─ fork isolated git worktree
       ├─ run the sub-agent with a role-scoped tool set
       └─ seal the result as a proposal
            ├─ select   fast-forward onto the current tree
            ├─ apply    three-way merge (path-disjoint)
            ├─ release  keep what landed, drop the record
            └─ discard  throw the attempt away
```

Retained mode is the default in local sessions: tracing is integral, so the shepherd root scope always exists. With `agents.subagent.mode: shared`, or if the trace store cannot be opened, dispatches run directly on the host. See [configuration.md](./docs/configuration.md#retained-worktrees-and-turn-checkpoints)
for the prerequisites and knobs.

## Commands

```bash
yaah                              # interactive REPL
yaah "prompt"                     # one-shot
yaah --resume <id> "continue"     # resume a session
yaah --approval allow "..."       # override approval policy
yaah --workspace ~/code "fix X"   # confine file tools to a directory
yaah -d "always run tests first" "fix X"

yaah tui                          # terminal UI
yaah web                          # browser UI
yaah serve [--http addr]          # MCP tool server
yaah acp-serve                    # ACP server

yaah config show|edit             # inspect or edit config
yaah doctor                       # diagnostics
yaah memory add|search            # persistent memory
yaah skill list|show|create|edit  # skills
yaah mcp list|add|remove          # MCP servers
yaah shepherd [prune <path>]      # inspect/clean crashed worktrees
yaah shepherd-trace list|show|profile
yaah update                       # update the binary
yaah version
```

## Documentation

| Doc | Contents |
|---|---|
| [architecture.md](./docs/architecture.md) | Agent loop, middleware pipeline, tool dispatch, context management, streaming, persistence, traces |
| [sub-agents.md](./docs/sub-agents.md) | Roles, budgets, escalation, quality gates, retained proposals, custom roles |
| [configuration.md](./docs/configuration.md) | Full `config.yaml` reference: providers, agents, sub-agents, workspace, supervision, hooks, observability |
| [features.md](./docs/features.md) | TUI/REPL/web, memory, MCP, tools, approval, workspace containment, tracing |
| [prompts.md](./docs/prompts.md) | System prompt assembly and every injection point |
| [agent-instructions.md](./docs/agent-instructions.md) | The exact sub-agent instructions the model receives |
| [code-organization.md](./docs/code-organization.md) | Package layout and file-size guidelines |
| [tui-components.md](./docs/tui-components.md) | TUI component system |
| [web-ui.md](./docs/web-ui.md) | Web UI architecture and wire events |
| [isolated-workspace-testing.md](./docs/isolated-workspace-testing.md) | Live containerd workspace test runbook |

## Development

```bash
go build .
go test ./...
go vet ./...
```

See [CONTRIBUTING.md](./CONTRIBUTING.md) for the PR process and
[AGENTS.md](./AGENTS.md) for the repository layout and conventions.

## License

`MIT OR Apache-2.0` — your choice. See [LICENSE](./LICENSE).
