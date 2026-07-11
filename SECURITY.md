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

yaah v0.1 ships with these defaults (see the design plan §8 for the
full table):

- `read`, `glob`, `grep`, `list` — `allow`
- `skill`, `bash`, `write`, `edit`, `webfetch` — `ask`
- MCP tools — `ask` (override per-server in the MCP manifest)

The last matching rule wins, broad rules first, narrow rules last.
There is no global "allow all" mode. If a user wants that, they can
pass `--yes` (one-shot auto-approve) or set `default.approval: allow`
in their config (we recommend against it).

## Reporting MCP server issues

If an MCP server you downloaded is misbehaving, the issue is with the
server, not yaah. File it upstream against the server's maintainer.
yaah is the wrong place for a bug report.

If, however, yaah's MCP client code has a vulnerability (sandbox
escape, unvalidated JSON, etc.), that belongs here.
