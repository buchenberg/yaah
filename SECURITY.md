# Security Policy

yaah is a CLI that runs on your machine and talks to AI model providers
and (optionally) MCP tool servers you choose. Because it executes
shell commands, edits files, and makes network requests on your
behalf, security matters.

## Supported versions

| Version | Supported          |
| ------- | ------------------ |
| 0.1.x   | ✅ active          |
| 0.0.x   | best-effort only   |

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
  (`ask` mode) before executing. Servers that need auto-approval
  must be opted into explicitly.
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

There are no per-tool allow/ask/deny rules. The "last matching rule
wins" framing from earlier design notes does not apply — the code is
a single switch. If you need a read-only mode, set `approval: deny`
or only register the `read`/`memory_search` tools via your config.

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
appended to the agent's available tools. There is no per-server
approval override today — MCP tools follow the global `approval`
setting (including any `--approval` or `YAAH_APPROVAL` override).
(Per-server overrides are a future enhancement.)

## Reporting MCP server issues

If an MCP server you downloaded is misbehaving, the issue is with the
server, not yaah. File it upstream against the server's maintainer.
yaah is the wrong place for a bug report.

If, however, yaah's MCP client code has a vulnerability (sandbox
escape, unvalidated JSON, etc.), that belongs here.
