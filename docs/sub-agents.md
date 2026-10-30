# Sub-agents

yaah delegates complex work to specialist sub-agents. This page covers roles,
budgets, escalation, quality gates, session directives, retained proposals,
and the evidenced contract every sub-agent returns.

## The team

The orchestrator keeps the main conversation focused on planning and
judgement. Sub-agents do the long-running exploration, implementation, and
validation in parallel.

Four roles are built in and work in any project with no setup:

| Role | Persona | Defaults | Specialty |
|---|---|---|---|
| `developer` | Charley | 75 iterations (cap 50), 24 turns, 900s | Implement features, fix bugs, edit files |
| `analyst` | Jack | 75 iterations (cap 50), 24 turns, 600s | Research from code, docs, and the web; no file changes |
| `tester` | Casey | 30 iterations, 8 turns, 300s | Run suites, analyze failures, measure coverage |
| `reviewer` | Tim | 25 iterations, 12 turns, 600s | Inspect code, complexity, dead code, maintainability |

There is no default role and no full-tools fallback: every dispatch names a
role. Unknown role names are rejected when the registry-backed role list is
available. A role with an empty tool set is valid (for example a joke-teller
or a no-tool judge).

Sub-agents inherit the orchestrator's provider and model unless
`agents.subagent.provider` / `agents.subagent.model` or a per-role override
is set. This is useful for running the team on a cheaper or faster model.

Multiple `delegate` calls in one turn fan out in parallel, bounded by
`agents.subagent.max_concurrency` (default 3).

Project-level roles live in `.agents/roles/*.md`; user-level roles live in
`~/.agents/roles/*.md`. This repository ships examples (security auditor,
Go developer/tester, single-command checker, counter, consolidator, and a
couple of no-tool novelty roles). Built-in role names always win; custom roles
are additive.

## Custom roles

A role is a markdown file with YAML frontmatter. The filename (minus `.md`)
is the role name; `name:` is the display persona.

```markdown
---
name: Auditor
specialty: security
description: Scans for vulnerabilities, secrets, and unsafe patterns
contract:
  heading: "## Audit"
  fields:
    - { name: severity, kind: interpretation }
    - { name: files_scanned, kind: evidence }
    - { name: issues_found, kind: interpretation }
    - { name: findings, kind: interpretation }
    - { name: summary, kind: interpretation }
tools:
  - read
  - grep
  - glob
  - ls
  - bash
max_iterations: 30
min_iterations: 8
max_turns: 20
min_turns: 12
timeout: 180
---

You are a SECURITY AUDITOR. Find vulnerabilities, hardcoded secrets, and
unsafe patterns. Report findings with file paths, line numbers, and severity.
Do NOT modify files.
```

Per-turn checkpointing is configured in `config.yaml`, not in the role-file
frontmatter: set `agents.subagent.roles.<role>.turn_checkpoints: true`.
The restore limit is `agents.default.max_turn_restores`. See
[configuration.md](./configuration.md#retained-worktrees-and-turn-checkpoints).

## Structured escalation

When a sub-agent cannot resolve a blocker, it emits a structured escalation
instead of silently failing:

````
```escalation
{"severity":"blocker","summary":"file not found","detail":"...","suggestion":"..."}
```
````

Severity is `info`, `warning`, `blocker`, or `critical`. The event is
surfaced to the orchestrator, which decides whether to retry, narrow the
task, or halt the wave. The escalation does not automatically cancel sibling
sub-agents.

## Quality gates

A completed sub-agent can be validated before its result is accepted:

```yaml
agents:
  quality_gates:
    developer: [tester]
```

When `developer` finishes, a `tester` is dispatched with the output as
context. A failing verdict annotates the result with
`[quality-gate:FAIL]`. Gates run after escalation parsing as well.

## Session directives

Directives are policy statements injected into the orchestrator prompt:

```bash
yaah -d "prefer table-driven tests" -d "always run go vet" "implement X"
```

Or in config:

```yaml
agents:
  default:
    directives:
      - "always run tests after implementation"
```

CLI directives are prepended to config directives. Sub-agents do not inherit
these; give them per-role directives under
`agents.subagent.roles.<role>.directives`.

## Budget model: floors and ceilings

Each dispatch resolves two dimensions: **iterations** (a hard loop cap that
yields `MaxIterationsError`) and **tool turns** (a soft cap that strips the
tool list and forces a text-only answer).

- `max_iterations` / `max_turns` are ceilings, resolved from the per-call
  argument, per-role config, the role file, then a fallback.
- `min_iterations` / `min_turns` are floors: a per-call override cannot drop
  below them. Config floors outrank role-file floors.
- Floors never shrink the other dimension. If a floored turn budget would
  reach the iteration cap, iterations grow to leave headroom, bounded by the
  schema maximum (50). Unfloored turns are clamped to `iterations - 1`.

```markdown
---
name: Auditor
max_iterations: 30
min_iterations: 8
max_turns: 20
min_turns: 12
---
```

The effective budget and its source are recorded on sub-agent spans and
reported by `list_subagents`, so a trace answers which layer set a value.

### Wall-clock timeout

`timeout` (per role) and `agents.subagent.default_timeout` are **total-run
wall-clock budgets**, not per-request timeouts. The clock starts at dispatch
and includes provider time-to-first-token, reasoning, streaming, and tool
calls. When it expires mid-call, the failure is attributed to the budget
rather than the provider: in the retained lane the run is sealed with a
timeout note, and in the direct lane the delegate returns a structured
timeout result. The provider did not reject the request. A provider-internal
request deadline while the dispatch is still live, or a parent deadline that
fires first, is not reported as role-budget exhaustion.

Reasoning models can spend a long time thinking before their first content
block, and a parallel wave can increase per-request latency. Budgets that
work for a solo cheap model can expire before the first content block when
several reasoning agents run at once. Size them from the role's expected
thinking time, not from the number of tool calls: a short budget that fits a
counter role may not fit a developer role that thinks for longer. Raise
`agents.subagent.roles.<role>.timeout` or `agents.subagent.default_timeout`
for long-reasoning roles, and check the role named in the timeout error
before suspecting the provider or the network.

## Retention and isolation

A sub-agent's work is retained, not trusted: it runs in its own world and
returns a proposal the orchestrator settles.

- **Retention.** In a local session the shepherd root scope always exists
  (tracing is integral), so each proposal-lane dispatch runs in a detached
  `git worktree` by default and returns a sealed proposal. With a configured
  per-dispatch container world, the work is a whole-path `GitDelta` manifest
  under `.yaah/proposals/<id>/` instead. `select` fast-forwards onto the
  dispatch basis, `apply` is path-disjoint (three-way for a worktree,
  whole-path post-images for a container), `release` exports the delta before
  dropping the record, and `discard` removes the worktree or manifest. A
  rejected attempt never touches the parent tree.
- **Report roles.** A role whose frontmatter declares `output: report`
  decouples the output contract from the substrate (yaah-7yf): a report that
  can mutate runs in an isolated report world (worktree or container) so
  scratch writes, builds, and test runs stay off the parent tree, but returns
  the report text directly — no proposal is sealed and nothing needs settling;
  the report world is torn down when the dispatch returns. A report role that
  carries no tool capable of changing the workspace skips the fork entirely
  and runs directly on the shared workspace, so its reads see fresh data.
  Roles that declare nothing keep the proposal lane, and an explicit
  `output: proposal` always seals.
- **Shared mode.** `agents.subagent.mode: shared` is the explicit
  direct-to-host opt-out. Without a trace store, or in a containerd/isolated
  session, the retained lane is not available and dispatches use the direct
  lane.
- **Turn checkpoints.** A role whose config sets
  `agents.subagent.roles.<role>.turn_checkpoints: true` captures workspace and
  conversation before each turn, so a turn that fails in the tool phase or
  exhausts its iteration budget rewinds instead of losing the attempt.

Because every retained dispatch is isolated, several can be compared safely:
issue two or three calls for one task in a single turn, read the proposals,
keep one, and discard the rest.

A worktree contains tracked files only. Gitignored build inputs must be
recreated by `retained_worktree_bootstrap`. See
[configuration.md](./configuration.md#retained-worktrees-and-turn-checkpoints)
for the worktree prerequisites and tuning.

## Evidenced contracts

Every sub-agent returns a structured response with a contract heading and
fields marked as `evidence` (raw tool output: commands, exit codes, file
paths, URLs) or `interpretation` (synthesis: findings, confidence,
summaries). Evidence fields can be trusted directly; interpretations are
spot-checked when confidence is low. This avoids re-running work the team
already did.
