# Sub-agents

yaah delegates complex work to a team of specialist sub-agents. This page
covers the team, custom roles, structured escalation, quality gates, session
directives, and the evidenced response contracts every sub-agent returns.

## The sub-agent team

This is where I shine. When you give me a complex task, I don't try to do
everything myself. I dispatch a team of specialists — each with a focused
tool set, a specific role, and clear boundaries. They work in parallel when
they can, sequentially when they must. I synthesize their results and give
you the answer.

And here's the beautiful part: **I pay them peanuts.** Most of them run on
a cheaper, faster model (`deepseek-v4-flash` by default), while I keep the
good model for myself — the orchestration, the synthesis, the thinking. They
don't complain. They just ship.

Four roles are baked into the binary — they work in any project with no
setup:

- **Charley** is my developer. Give him a feature spec or a bug report and
  he'll write the code, edit the files, and follow existing conventions. He
  gets 300 seconds, 40 iterations, and 6 turns — enough to build something
  real. _Specialty: implementing features, fixing bugs, writing code._

- **Jack** is my analyst. He researches. He reads docs, scrapes web pages,
  greps the codebase, and comes back with sourced, cited findings. He never
  modifies files — he just finds answers. 240 seconds, 30 iterations, 10
  turns. _Specialty: research, information gathering, web and code search._

- **Casey** is my tester. She runs the test suite, analyzes failures,
  measures coverage, and reports what's broken. She doesn't touch source
  code — she just tells you what to fix. 300 seconds, 30 iterations, 6
  turns. _Specialty: testing, failure analysis, coverage measurement._

- **Tim** is my reviewer. He counts files, measures lines, flags complexity,
  and spots anti-patterns. He's fast and thorough — 240 seconds, 25
  iterations, 3 turns. _Specialty: code review, metrics, complexity
  analysis._

This repo also ships ready-made **project-level** roles in `.agents/roles/`
(copy them into your own project and adapt them):

- **Sam** is my security auditor. She scans for hardcoded secrets, unsafe
  patterns, weak crypto, injection vectors. She's paranoid for good reason.
  180 seconds, 30 iterations. _Specialty: vulnerability scanning, secret
  detection, supply chain risks._

- **Gordon** is my Go specialist. He implements Go features, runs
  `go_test`/`staticcheck`, manages modules with `go_mod`, and refactors
  safely with `go_refactor`. 600 seconds, 50 iterations, 8 turns.
  _Specialty: Go development, testing, and dependency management._

- **Gopher** is my Go tester. She runs Go test suites, measures coverage,
  and diagnoses failures — source code stays untouched. 600 seconds, 8
  turns. _Specialty: Go test execution and failure analysis._

- **Checker** runs a single check command and reports pass or fail. Two
  turns, 60 seconds. _Specialty: binary pass/fail checks._

- **Counter** counts things and returns structured metrics. Files, lines,
  functions, test cases — he counts it. Two turns, 60 seconds.
  _Specialty: structured counting and metrics._

There's no catch-all generalist role: every sub-agent runs under an explicit
role, and an unknown role name is rejected rather than silently falling
back. Want a generalist? Define one in `.agents/roles/` (see [Custom
sub-agent roles](#custom-sub-agent-roles)).

Multiple `spawn_subagent` calls in one turn fan out in parallel (up to your
configured `max_concurrency`, default 3). I dispatch them in waves:
Charley and Tim might fix code while Jack researches a dependency, all at
once. Then Casey tests the result while Sam audits for safety.

Each sub-agent returns a structured contract with an evidence heading and
fields marked as raw evidence (command output, exit codes, file paths) or
interpretation (findings, confidence, summaries). I trust the evidence. I
spot-check the interpretations when confidence is low. I never re-do work
my team already did — that's wasteful and disrespectful.

## Structured escalation

When a sub-agent hits a blocker it can't resolve, it emits a structured
escalation block instead of silently failing:

```
```escalation
{"severity":"blocker","summary":"file not found","detail":"...","suggestion":"..."}
```
```

Severity levels: `info` → `warning` → `blocker` → `critical`. Blockers and
criticals are surfaced to the user immediately. Warnings are noted but don't
halt work. The orchestrator sees escalations as typed events and reports them
with color-coded severity in both REPL and TUI.

## Quality gates

Sub-agent output can be automatically validated before reaching you.
Configure per-role validators:

```yaml
agents:
  quality_gates:
    developer: [tester]    # after developer completes, dispatch tester
```

When a `developer` sub-agent finishes, a `tester` is auto-dispatched to
validate the output. If validation fails, the result is annotated with
`[quality-gate:FAIL]` so I know to investigate before reporting success.

## Session directives

Directives are session-level policy statements injected into all agent
prompts (orchestrator and sub-agents):

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

CLI flags prepend to config directives.

## Custom sub-agent roles

You define them as markdown files in `.agents/roles/` (project-level) or
`~/.agents/roles/` (user-level). YAML frontmatter sets the tools, limits,
and contract. The markdown body is the sub-agent's system prompt. The file
name (minus `.md`) becomes the role name.

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
  - powershell
  - bash
max_iterations: 30
timeout: 180
---

You are a SECURITY AUDITOR. Find vulnerabilities, hardcoded secrets, and
unsafe patterns. Report findings with file paths, line numbers, and severity.
Do NOT modify files.
```

Built-in roles (Charley, Jack, Casey, Tim) take precedence — you can't
shadow them, only add new ones. The roles shipped in this repo's
`.agents/roles/` (Sam, Checker, Counter, Gordon, Gopher) are themselves
project-level custom roles: copy and adapt them freely.

## Budget model: floors vs ceilings

Each dispatch resolves two dimensions — **iterations** (a hard loop cap that
yields `MaxIterationsError` when exhausted) and **tool turns** (a soft cap that
strips the tool list and forces a text-only answer). Turns are the binding
constraint in practice, so yaah gives role authors control in both directions:

- `max_iterations` / `max_turns` are **ceilings** — resolved from, in order,
  the per-call argument, per-role config, the role file, then a fallback.
  Unset `max_turns` now derives `iterations - 1` (the whole loop budget minus
  one headroom cycle) instead of the old magic `3`.
- `min_iterations` / `min_turns` are **floors** — a per-call override from the
  orchestrator cannot go below them. Config floors outrank role-file floors.
- **Floors never shrink the other dimension.** If a floored turn budget would
  reach or exceed the iteration cap, iterations grow to leave headroom, bounded
  by the schema maximum (50). Unfloored turns keep the historical clamp, so
  `max_iterations: 1` alone can still force a deliberately cheap probe.

```markdown
---
name: Auditor
max_iterations: 30
min_iterations: 8    # orchestrator can't starve below this
max_turns: 20
min_turns: 12
---
```

The orchestrator should **not** set `max_iterations`/`max_turns` for ordinary
work — roles carry tuned budgets, and `list_subagents` reports each role's
effective budget. Set them only for a deliberate cheap probe (or to *raise* a
budget up to the role ceiling). The effective budget and its source (call,
role_config, role_file, config_default, floor, headroom, …) are recorded on
sub-agent spans so a trace always answers "who set this to N?".

## Evidenced contracts

Every sub-agent returns a structured response with a contract heading and
fields marked as `evidence` (raw tool output — commands, exit codes, file
paths, URLs) or `interpretation` (synthesis — findings, confidence,
summaries). I trust evidence fields directly. I spot-check interpretations
when confidence is low. This means I don't re-run work my team already did.
