# Security Policy

yaah is a CLI that runs on your machine and talks to AI model providers
and (optionally) MCP tool servers you choose. Because it executes
shell commands, edits files, and makes network requests on your
behalf, security matters.

## Supported versions

Security fixes land on a rolling window — the latest minor line and the one
before it — so this table states the policy and rolls forward with each
release instead of being pinned to one version. As of the current release line
(0.113.0 in `.release-please-manifest.json`):

| Version | Supported          |
| ------- | ------------------ |
| latest minor line (0.113.x today) | ✅ active |
| previous minor line (0.112.x today) | ✅ security fixes |
| older   | best-effort only   |

Only those two lines are supported at any moment; anything older is
best-effort and may receive no fix at all.

## Reporting a vulnerability

**Please do not file a public GitHub issue for security bugs.**

Email `security@buchenberg.dev` (PGP key on request). Include:

- A short description of the issue and the impact
- A reproduction (commands, config, output)
- Your yaah version (`yaah version`) and OS

You should hear back within 72 hours. We will coordinate a fix and
credit you in the release notes (unless you prefer to stay anonymous).

## Threat model

yaah trusts:

- The model provider (OpenAI, Anthropic, OpenRouter, Ollama, etc.) — the
  model can refuse, return garbage, or be prompt-injected. The user is
  expected to review tool calls before approval.
- The user — yaah runs with the user's permissions and can do anything
  the user can do on the machine.

yaah does **not** trust:

- The contents of files in the working directory. `AGENTS.md` and
  `SKILL.md` are user-authored but in hostile repos they can be a
  prompt-injection vector. yaah renders them as plain data; it does
  not give them elevated trust.
- MCP tool servers. By default, every MCP tool call asks the user
  (`mcp_approval: ask`) before executing. Auto-approval must be opted
  into explicitly with `mcp_approval: allow`.
- Skill bodies. The `skill` tool returns skill content as text; the
  agent can then act on it, but yaah does not execute skill bodies
  outside the sandbox of the agent's own tool-calling loop.

## Permission model

yaah's safety model is intentionally simple: one global `approval`
setting, a coarse deny-list for obviously destructive shell patterns,
and an explicit opt-in for auto-approve.

### `default.approval` (in `~/.yaah/config.yaml`)

| Value   | Behavior                                                                 |
|---------|--------------------------------------------------------------------------|
| `ask`   | (Default) Prompt before every tool call. User types `y` / `n` at the REPL.|
| `allow` | Run every tool call without prompting. Explicit opt-in.                  |
| `deny`  | Refuse every tool call. Useful for read-only sessions.                   |

Approval is a single global switch — there are no per-tool allow/ask/deny
rules. If you need a read-only mode, set `approval: deny`.

Two additional path-level controls exist alongside the approval switch:

- **Workspace restriction** (`--workspace <dir>`, plus `--allow-home`,
  `--workspace-ask` and `agents.default.workspace_deny_patterns`):
  file-accessing tools are validated against a `PathValidator` rooted at the
  given directory; out-of-workspace paths are denied (or prompted for, with
  `--workspace-ask`), and the deny patterns apply even inside the workspace.
  The validator is inherited by sub-agent tool registries. This bounds **file
  tool calls in this session** — `read`, `write`, `edit`, `grep` and the other
  path-taking tools — and nothing else: `bash`/`powershell` run with the
  workspace as their working directory but can still read or modify paths
  outside it (`internal/tools/bash.go`: "Containment is NOT enforced for shell
  commands"). Treat `--workspace` as a file-tool guard, not a containment
  boundary.
- **Permission middleware**: path-pattern allow/deny rules for tool calls,
  enforced inside sub-agent loops as well (denied calls are filtered before
  execution). Note: there is currently no user-facing config surface for
  these rules — `WithPermissionRules` is exercised only by tests, and naming
  `permission` in `agents.middleware.enabled` without rules is a no-op (an
  empty rule set allows everything). It is a pipeline mechanism awaiting a
  config schema, not yet an opt-in security control.

### Confinement and egress for dispatched sub-agents

Process containment is a separate axis from `--workspace`. Two
`agents.subagent.*` keys govern **dispatched sub-agent processes**; neither
governs the parent session. Both are validated at load time so an unknown
value cannot silently mean "unconfined" (`internal/config/validate.go`).

- **`agents.subagent.containment`** (`internal/config/load.go`) selects the
  syscall jail. `""`/`auto` (default) jails on the platform backend —
  Landlock on Linux, winacl on Windows, Seatbelt on macOS, chosen per `GOOS`
  in `internal/jail/jail.go` — and degrades visibly to advisory where no jail
  is available; `advisory` and `off` never jail; `paddock` (alias `jail`)
  fails closed instead of degrading. A jailed dispatch gets its worktree as
  its sole writable root, and cannot run git write commands
  (`add`/`commit`/`stash`), because a linked worktree's index lives in the
  host repository outside that root.
- **`agents.subagent.network`** (with `network_hosts`) selects the egress
  posture of a confined dispatch. `allow_all` (default) leaves egress
  ungoverned; `deny_all` refuses TCP egress; `broker` pins the jail's egress
  to the session broker, which admits only the **exact** host names in
  `network_hosts` (patterns are rejected — a rule that looks present and
  admits nothing is the wrong failure direction), and empty `network_hosts`
  under `broker` admits nothing. The restrictive postures need a kernel
  network axis (Landlock ABI ≥ 4 on Linux; winacl has no network axis and
  refuses them; macOS Seatbelt enforces `deny_all` but its broker allowlist is
  cooperative rather than kernel-pinned), and a posture the host cannot enforce
  **fails the dispatch** rather than running it with open egress. They only
  bite on a confined dispatch: with containment `off` there is no jail to hold
  the rule, and the config is reported as unenforced.
- **What is never jailed.** The orchestrator/parent process is not confined by
  either key, so parent-side `bash`, `read`, `grep` and friends run at the
  user's own privilege, bounded only by `--workspace`'s file-tool checks, the
  `approval` gate and the deny-list below. Neither key is a network policy for
  the session: model requests and the update check are made by the yaah
  process itself, and `http`/`webfetch` are bounded by the always-on URL guard,
  not by `agents.subagent.network`.

So `--workspace` guards *file tool calls*, `containment`/`network` guard
*jailed child processes*, and the URL guard bounds *the two tools that fetch
URLs in this process* — none of the three is complete on its own. The
row-by-row statement of what each mode governs is the isolation-vs-egress
matrix in
[docs/configuration.md](./docs/configuration.md#the-isolation-vs-egress-matrix).

### Approval override

The global `approval` setting can be overridden at runtime via the `--approval`
(`-a`) CLI flag or the `YAAH_APPROVAL` environment variable. Resolution order:
CLI flag → `YAAH_APPROVAL` env var → `config.yaml` → built-in default (`ask`).

```bash
yaah --approval allow "run headless tests"
YAAH_APPROVAL=deny yaah                       # read-only session
```

Invalid values fall back to `ask` with a warning on stderr. The override
applies to all tools — there is still no per-tool or per-path approval.

### Dangerous-command guard (best-effort, not a security boundary)

The `bash` and `powershell` tools have a coarse deny-list of obviously
destructive patterns (`rm -rf /`, disk-init commands, etc.). It catches
the most blatant mistakes. It is **not** a security boundary — model-
generated shell can trivially evade a substring deny-list. Real
protection comes from the `approval` gate above; the deny-list only
buys you protection against accidental destructive commands when
`approval: allow` is set.

### MCP server tools

Each registered MCP server is launched at startup; its tool list is
appended to the agent's available tools. MCP-served tools have their own
approval policy, `agents.default.mcp_approval` in
`~/.yaah/config.yaml` (`internal/config/load.go`; validated in
`internal/config/validate.go`):

| Value   | Behavior                                                                        |
|---------|---------------------------------------------------------------------------------|
| `ask`   | (Default) Prompt before every MCP tool call — even when the global `approval` is `allow`. |
| `allow` | Run MCP tool calls without prompting. Explicit opt-in.                          |
| `deny`  | Refuse every MCP tool call; the user cannot approve past it.                    |

The decision is made per call before the tool registry is consulted, because
MCP tools are remote wrappers that cannot classify their own danger
(`internal/agent/agent_safety.go` classifies them, and
`internal/agent/pipeline/approval.go` applies `ask`/`deny` independently of the
global approval mode). Scope and limits worth knowing:

- The policy is global to **all** MCP-served tools: there is no per-server
  override today, so a single untrusted server cannot be denied while a
  trusted one runs unprompted.
- `--approval` and `YAAH_APPROVAL` override the global `approval` setting
  only; they do not change `mcp_approval`
  (`cmd/yaah/provider_resolve.go`).
- An unrecognised value is a config validation error and resolves to `ask`.
- A server that needs auto-approval must be opted into explicitly by setting
  `mcp_approval: allow`, which covers every MCP tool in the session.

## Reporting MCP server issues

If an MCP server you downloaded is misbehaving, the issue is with the
server, not yaah. File it upstream against the server's maintainer.
yaah is the wrong place for a bug report.

If, however, yaah's MCP client code has a vulnerability (sandbox
escape, unvalidated JSON, etc.), that belongs here.
