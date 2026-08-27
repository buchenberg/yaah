---
name: tool-result-pruning-recovery
description: Make tool-result pruning recoverable and preventable via spill-to-disk, informative stubs, corrected defaults, and tool head-limits
status: approved
---

# Tool-result pruning recovery & prevention

**Status:** approved
**Owner:** TBD
**Related:** `internal/agent/pipeline/pruner.go`, `internal/agent/agent_truncation.go`, `internal/tools/grep.go`, `internal/config/load.go`

## 1. Problem statement

During investigation-heavy work, tool results that I have already read are
replaced in my working context by:

```
[output pruned — 5412 chars omitted to save context; re-run the tool if you need it again]
```

Three concrete harms were observed on a real task:

1. **Silent information loss.** I read four files in turn 1; after I issued
   a second turn of tool calls, turn 1's file contents were pruned from my
   context. The content I had not yet summarised was gone, forcing me to
   re-read the same files (more tokens, more turns) instead of reasoning
   from held context.
2. **No recoverability.** The only recovery path offered is "re-run the
   tool". It does not name which tool, does not carry the original
   arguments, and does not provide a spill file. Re-running `grep` or a
   web fetch is expensive and non-deterministic.
3. **Pruning was triggered by over-fetch that the tool layer could have
   prevented.** `grep` returns up to ~8 KB and then hard-caps with a
   generic marker; there is no `max_results` / head-limit parameter to
   scope a broad search before pulling content into context.

This is a **context-management** issue, not a turns issue. The previous
plan (`subagent-turn-budget-floors`) addresses how many tool turns a
sub-agent gets; this plan addresses what happens to the *content* of
those tool results once obtained.

## 2. Root cause analysis

### 2.1 The pruner is the source of `[output pruned ...]`

`internal/agent/pipeline/pruner.go` implements soft-pruning, ported from
kilocode's compaction pass:

- `Mark(messages, reason)` walks history backwards, finds stale tool
  results beyond the protect window, and records their `ToolCallID`s in a
  `pruned` set.
- `Filter(messages)` returns a copy where every pruned tool message's
  `Content` is replaced by `pruneStub(len(content))`.
- The originals are never mutated; only the **ephemeral provider request**
  sees stubs. But from the model's perspective the provider request *is*
  its memory — a pruned result is effectively forgotten unless the model
  already wrote a durable summary.

This is why I "saw" pruned output in my own transcript: the transcript is
the agent's provider-bound message list, which has been through `Filter`.

### 2.2 Defaults lose the prior turn immediately

```go
const (
    defaultPruneProtectTokens = 2000
    defaultPruneMinReclaim    = 200
    defaultPruneMinTurns      = 1     // <-- the smoking gun
)
```

`MinTurns` counts backwards over "user messages OR assistant messages with
tool calls". With `MinTurns == 1`, the moment the model issues its second
tool-using turn, **every tool result from the first turn is immediately
eligible for pruning** (subject only to `ProtectTokens` = 2000 ≈ 8 KB).

The config comment claims a different policy:

> "protect 3k tokens ... commit once it reclaims > 500 tokens, always
> keeping the last 2 turns"

The documentation describes `3k / 500 / 2`; the code implements
`2000 / 200 / 1`. The actual behaviour is therefore *more aggressive* than
the maintainers' own stated intent. This drift is itself a bug.

### 2.3 The stub is a dead end

```go
return "[output pruned — " + strconv.Itoa(omittedChars) +
       " chars omitted to save context; re-run the tool if you need it again]"
```

`Filter` has `m.Name` available (the tool message carries it), and the
loop has a `ToolSpillDir` concept — but the stub uses neither. So the model
cannot even tell *which* of its four parallel results was pruned.

### 2.4 Sub-agents inherit the same aggressive pruning

`internal/agent/subagent_loop.go` and `internal/agent/runner/runner.go`
construct the same `PruneConfig` defaults as the parent loop. A reviewer
sub-agent with a small `max_turns` budget (see the floors plan) is doubly
penalised: it has few turns *and* its few read results are aggressively
pruned away.

### 2.5 Tool-side over-fetch is unchecked

`internal/tools/grep.go` truncates at a hard 8 KB cap and appends a generic
marker with no spill and no way to request fewer results up front. There is
no `max_results` (grep/ls) or head-limit (powershell) parameter. Broad
searches therefore produce near-cap output, which then trips the pruner on
the next turn.

## 3. Goals / non-goals

**Goals**

- G1. Pruned content is **recoverable** without re-running the originating
  tool (spill to disk, recoverable path in the stub).
- G2. The prune stub is **informative**: names the tool and the recovery
  path, and keeps the "re-run" escape hatch as a fallback.
- G3. Defaults match documented intent: `MinTurns` becomes 2 (and the
  config comment is corrected to match constants — or constants raised to
  match the comment; see §10).
- G4. Tools that tend to over-fetch expose an explicit head-limit so the
  model can *prevent* prune pressure instead of only reacting to it.
- G5. Prune activity is observable: the spill file path and per-tool
  prune counts are surfaced in trace/hook stats.

**Non-goals**

- Removing soft-pruning or making it unlimited (context is a real, finite
  resource; pruning is the right mechanism, it is just lossy today).
- LLM-based summarisation of pruned content (that is compaction, a
  separate, already-implemented path).
- Changing the protect-window sizing algorithm itself beyond correcting
  the documented defaults.

## 4. Design

### 4.1 Spill pruned content to disk (recoverability)

Reuse the existing `ToolSpillDir` pattern from `agent_truncation.go`, but
use a **separate directory** so truncated-raw and pruned-content have
distinct lifecycles:

- `ToolSpillDir` → `~/.yaah/truncated` (existing; raw oversized results)
- `PruneSpillDir` → `~/.yaah/pruned` (new; content elided by soft-prune)

Rationale for separation: truncated results are still *present* in context
(just cut); pruned results are *absent* from context. Mixing them in one
directory makes `ls ~/.yaah/truncated` ambiguous about what is recoverable
and what is not.

#### Pruner changes (`internal/agent/pipeline/pruner.go`)

Keep the package pure and testable by injecting I/O through a nil-safe
hook rather than importing filesystem code into the policy:

```go
// SpillFunc persists a pruned tool result and returns a path the model
// can read back. Nil disables spilling (bare stub fallback).
type SpillFunc func(toolCallID, toolName string, content string) string

type PruneConfig struct {
    ProtectTokens  int
    MinReclaim     int
    MinTurns       int
    ProtectedTools map[string]bool
    Spill          SpillFunc   // NEW; nil = no spill
}
```

Spill at **commit time** in `Mark` (the one place a candidate transitions
from "candidate" to "pruned"):

```go
for _, c := range candidates {
    p.pruned[c.id] = true
    if p.cfg.Spill != nil {
        if p.spillPath[c.id] == "" {
            p.spillPath[c.id] = p.cfg.Spill(c.id, c.name, c.content)
        }
    }
}
```

Consequences:

- I/O happens once per pruned tool call, not once per `Filter` call
  (`Filter` runs on every provider request and must stay cheap).
- The dedupe map `spillPath map[string]string` prevents double-writes when
  a candidate is marked and then re-marked after `Reset`.

`Mark` currently stores only `{id, tokens}` per candidate; extend
`pruneCandidate` to `{id, name, content string, tokens int}` so the commit
loop has everything the spill hook needs.

#### Filter stub changes

`Filter` already has `m.Name`; look up `p.spillPath[m.ToolCallID]`:

```go
out[i].Content = pruneStub(m.Name, len(m.Content), p.spillPath[m.ToolCallID])
```

New stub shape (ASCII, provider-agnostic, single line):

```
[output pruned — 5412 chars of <name> omitted; full content: ~/.yaah/pruned/<name>_<id>.txt; re-run the tool if you need it again]
```

When spilling is disabled, omit the path segment and keep the tool name:

```
[output pruned — 5412 chars of grep omitted; re-run the tool if you need it again]
```

This preserves wire-format correctness (the tool message stays, with
`ToolCallID` and `Name` intact) while making the model's next action
obvious: `read` the spill file (cheap, deterministic) or re-run the tool.

### 4.2 Wire the spill dir at the composition root

`cmd/yaah/wiring.go` already owns `toolSpillDir()` and injects it into
parent and sub-agent loops. Add the parallel source:

```go
func pruneSpillDir() string { return filepath.Join(config.HomeDir(), "pruned") }
```

Inject as `PruneSpillDir` through the existing `LoopConfig` /
`SubAgentLoopConfig` / `RunnerOptions` plumbing (mirror `ToolSpillDir`
exactly — including the `config_parity_test.go` exemption comment). The
`Loop` wraps the directory in a `SpillFunc` that:

- `os.MkdirAll(dir, 0o755)`
- writes `filepath.Join(dir, fmt.Sprintf("%s_%s.txt", sanitisedToolName, toolCallID))`
- sanitises the tool name (no separators, no `..`) and dedupes with an
  in-memory `sync.Map` guard so `Mark`'s own dedupe is belt-and-braces.

### 4.3 Tool-side prevention

Add an optional head-limit parameter to the two worst offenders so the
model can scope *before* fetching:

- `grep` — `max_results int` (default 0 = unchanged). When set:
  - ripgrep path: pass `--max-count <n>`.
  - native walker path: break after `n` matches.
  - When the 8 KB cap still triggers, the truncation marker becomes
    actionable: `... [truncated at 8 KB; re-run with max_results to scope]`.
- `ls` — `max_results int` (default 0 = unchanged); slice after `n`
  entries with an actionable marker.

`powershell`/`bash` already let the model write its own `Select-Object
-First N` / `| head`; document that in the tool descriptions rather than
adding a special param (keep the schema surface minimal).

Note: `read` already exposes `offset`/`limit` — no change. This phase
directly prevents the "broad search → 8 KB result → pruned next turn"
loop.

### 4.4 Fix default drift (G3)

Decide the truth, then make code and comment agree. Two options in §10;
recommended default for this plan: **raise constants to the documented
values** `3000 / 500 / 2`, because the doc comment encodes the
maintainers' stated intent and the current values are objectively too
aggressive for investigation sessions.

```go
const (
    defaultPruneProtectTokens = 3000
    defaultPruneMinReclaim    = 500
    defaultPruneMinTurns      = 2
)
```

Update the `PruneConfig` doc comment to match **exactly** (remove the
hand-wavy "3k/500/2" prose and make it enumerate the constants). Add a
compile-time-free unit test asserting `DefaultPruneConfig()` equals the
documented triple so drift regressions are caught.

### 4.5 Config surface

`internal/config/load.go` already exposes prune tuning; verify all three
fields plus the new spill toggle are present and wired into both the parent
loop (`cmd/yaah/build_loop.go`) and sub-agent loop (`runner.go`):

```yaml
agents:
  context:
    prune_protect_tokens: 3000   # was 2000
    prune_min_reclaim: 500       # was 200
    prune_min_turns: 2           # was 1
    prune_spill_dir: ""          # NEW; empty = ~/.yaah/pruned
    prune_spill_enabled: true    # NEW; false = bare stub fallback
```

Add validation: `prune_min_turns >= 1`, `prune_protect_tokens > 0`,
`prune_min_reclaim > 0`. Add a parity test entry for `PruneSpillDir`
(mirror `ToolSpillDir`).

### 4.6 Observability (G5)

Extend `PruneStats` with:

```go
Spilled int // count spilled this commit
```

Thread it into the existing `HookEvent` / `events.ContextPrune` and OTel
`FinishPrune` span. Emit one attribute per spill with the file path
(bounded: keep the full path on the span, and the short basename in the
hook). This gives `yaah shepherd-trace` users a direct "what was pruned
and where is it" answer.

### 4.7 Protected tools (optional, config-gated)

`ProtectedTools` already exists and defaults to
`{"skill": true, "spawn_subagent": true}`. Consider making
`read`/`grep`/`glob`/`file_info` protected-by-default is tempting but
changes the context-growth profile; **leave defaults unchanged** and add a
config override so operators can opt in:

```yaml
prune_protected_tools: [read, grep, glob, file_info]
```

Merged *additively* onto the hardcoded defaults (never removed, so the
safety properties of `skill`/`spawn_subagent` are preserved).

## 5. Alternatives considered

| Option | Verdict |
|---|---|
| **A. Just raise `ProtectTokens` / `MinTurns`** | Helps (§4.4) but leaves pruned content unrecoverable and the stub nameless. Necessary, not sufficient. |
| **B. Spill to the existing `truncated` dir** | Fewer moving parts, but conflates two different lifecycles and makes recovery ambiguous. Rejected in favour of `~/.yaah/pruned`. |
| **C. Keep full content in a side-band map keyed by ToolCallID** (memory, not disk) | Survives a session but not a restart; disk is cheaper and already the pattern for truncation. Rejected. |
| **D. Disable soft-pruning for sub-agents** | They are the most budget-constrained and need context the most, but disabling entirely causes unbounded growth and OOM. Rejected; sub-agents get the same spill + `MinTurns=2` fix. |
| **E. LLM-summarise pruned results** | More tokens and latency than spilling, and summaries lose fidelity. Rejected for this plan (compaction already covers the long-horizon case). |

## 6. Implementation phases

Each phase lands green independently.

### Phase 0 — Baseline & characterization
- Add a characterization test for the current `pruneStub` format and for
  `MinTurns == 1` losing the prior turn after a second tool-using turn.
  This is the regression proof; the Phase 4 diff edits these tests.
- File `bd` epic + sub-issues.

### Phase 1 — Informative stub (no I/O, no behaviour change)
- Change `pruneStub` signature to `pruneStub(toolName string, omittedChars int, spillPath string)`.
- Keep spill path empty for now; include tool name.
- `Filter` passes `m.Name`.
- Update tests. **Gate:** `go test ./internal/agent/...` green.

### Phase 2 — Spill infrastructure
- Add `SpillFunc`, `spillPath` dedupe map, `Spilled` stat.
- Extend `pruneCandidate` to carry name + content.
- Spill at commit time in `Mark`.
- `pruneSpillDir()` + wiring through `LoopConfig`, `SubAgentLoopConfig`,
  `RunnerOptions`, `config_parity_test.go`.
- **Gate:** unit test that a pruned result is written once, path is in
  stub, `Filter` does not re-write.

### Phase 3 — Tool head-limits
- `grep` `max_results` (ripgrep `--max-count` + native fallback).
- `ls` `max_results`.
- Actionable truncation markers referencing `max_results`.
- Update tool descriptions so the model knows the param exists.
- **Gate:** tool-level tests for both ripgrep and native paths.

### Phase 4 — Default-drift fix
- Raise constants to `3000 / 500 / 2`, correct the doc comment.
- Edit the Phase 0 characterization tests to the new expectations.
- **Gate:** `go test ./internal/agent/pipeline/...`.

### Phase 5 — Config, observability, protected-tools opt-in
- `prune_spill_enabled`, `prune_spill_dir`, `prune_min_turns` validation.
- `prune_protected_tools` additive merge.
- `PruneStats.Spilled` → hook + OTel span.
- **Gate:** config validation + parity tests.

### Phase 6 — Docs & full gates
- `docs/configuration.md`: prune knobs + spill dir + recovery workflow.
- `docs/architecture.md` or `docs/features.md`: prune/spill architecture.
- `AGENTS.md` repo-layout note if a new helper package is added (likely
  none; spill stays in `pipeline` + `cmd/yaah`).
- **Gates:** `go test ./...`, `go vet ./...`, `gofmt -l .` empty,
  `staticcheck ./...` clean.

## 7. Test plan

**Unit — `internal/agent/pipeline`**

| Case | Expectation |
|---|---|
| `MinTurns=2`, two tool turns | turn-1 results survive; turn-0 results candidates |
| `MinTurns=1` (current) | turn-1 results eligible (characterization, later removed) |
| Stub with name, no spill | `[output pruned — N chars of grep omitted; ...]` |
| Stub with spill path | path present, single line, ASCII |
| Spill dedupe | one file per ToolCallID across repeated `Mark` |
| `Filter` after spill | does not rewrite the file |
| Spill disabled | bare stub, no path, no error |
| Protected tools | `skill`/`spawn_subagent` never pruned even when stale |
| `prune_protected_tools` override | additive merge, defaults preserved |

**Unit — tools**

| Case | Expectation |
|---|---|
| `grep max_results=5` (ripgrep) | `--max-count 5` in argv, ≤5 result lines |
| `grep max_results=5` (native) | breaks after 5 matches |
| `grep` 8 KB cap + `max_results` hint | marker mentions the param |
| `ls max_results=3` | ≤3 entries + actionable marker |

**Config**

| Case | Expectation |
|---|---|
| `prune_min_turns: 0` | validation error |
| `prune_spill_enabled: false` | bare stub fallback |
| parity test | `PruneSpillDir` exempted like `ToolSpillDir` |

**Integration**
- Run a real investigation task; after the second tool turn, assert a
  prior result's stub contains a `.txt` path, `read` that path, and confirm
  content round-trips byte-for-byte.
- Manual smoke via `yaah-dev-loop` (HTTP MCP hot-reload): rebuild, restart
  on `127.0.0.1:7333`, verify `pid` changed, dispatch a grep-heavy task,
  inspect `mcp__yaah__traces --tree` for prune span + spill path.

## 8. Acceptance criteria

1. `[output pruned ...]` stubs name the tool and (when enabled) carry a
   recoverable spill path.
2. Pruned content is written exactly once to `~/.yaah/pruned` and can be
   `read` back byte-for-byte.
3. `MinTurns` defaults to the documented value (2) and the code comment
   matches the constants.
4. `grep` and `ls` expose `max_results`; broad searches can be scoped
   before over-fetching.
5. Prune spans/hooks report spill counts and paths.
6. `go test ./...`, `go vet ./...`, `gofmt -l .` (modulo the pre-existing
   `internal/tui/helpers_msg.go` finding), `staticcheck ./...` clean.

## 9. Risks

| Risk | Mitigation |
|---|---|
| Spilling every pruned result fills disk | Files are small (pruned results are typically KBs), live under `~/.yaah/pruned`, and `PruneStats.Spilled` makes growth visible. Follow-up issue: age-based GC. |
| Stub becomes too verbose and consumes the context it saved | Keep stub one line, ASCII, tool-name only; the spill path is short and stable. |
| `--max-count` changes grep semantics for the model | It is opt-in (`max_results: 0` = unchanged); the description states it caps *result lines*, not context. |
| Raising `MinTurns` to 2 grows context | Bounded (one extra turn), and it matches already-documented intent; operators can lower via config. |
| I/O in `Mark` violates "pure policy" purity | `Mark` stays pure in the *policy* sense; the filesystem call is injected as `SpillFunc` and nil by default in tests. |

## 10. Decisions

1. **Raise the code** to `3000/500/2` — the doc comment is authoritative
   and the current `2000/200/1` values are too aggressive. §4.4 proceeds
   by raising constants.
2. **Spilling defaults on.** The escape hatch is `prune_spill_enabled:
   false`; an opt-in recovery feature has no utility.
3. **Auto-GC is a follow-up issue** (age-based cleanup of `~/.yaah/pruned`
   or a directory-size cap). Not blocking this plan.
4. **Do not protect `read`/`grep`/`glob` by default.** Expose via
   `prune_protected_tools`; revisit if investigation sessions still churn
   on re-reads after `MinTurns=2`.

