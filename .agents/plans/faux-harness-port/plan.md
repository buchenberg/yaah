---
name: faux-harness-port
description: Port pi's faux-provider benchmark pattern to yaah — a scripted, zero-API-cost provider plus a full-stack test harness, regression-suite convention, golden transcripts, and a benchmark scenario runner.
status: proposed
---

# Faux Provider & Test Harness Port (pi pattern → yaah)

## 1. Goal

Make yaah's loop, pipeline, and session behavior testable and benchmarkable
**without any real LLM API calls**, by porting the pattern pi uses
(`pi/packages/ai/src/providers/faux.ts` + `pi/packages/coding-agent/test/suite/harness.ts`):
a scriptable faux provider registered behind the normal provider seam, plus a
harness that wires the whole stack (loop + pipeline + tools + persistence +
events) in memory, plus a regression-suite convention and a benchmark scenario
runner that emits rows for `BENCHMARKS.md` deterministically.

Success criteria:

1. Any loop/pipeline behavior can be exercised end-to-end with scripted
   responses — including steering mid-tool-phase, truncation, retries,
   compaction triggers, sub-agent dispatch — with zero API cost.
2. New regression tests follow one discoverable convention.
3. `yaah bench --model faux` reproduces the BENCHMARKS.md measurement workflow
   (turns / subs / tools / tokens / time) deterministically, so loop changes
   (pruning, compaction, continuation guards) can be A/B'd for free before
   spending API budget.

## 2. Verified current state

### What pi has (read from `pi/` source)

- **`packages/ai/src/providers/faux.ts`**:
  - `FauxResponseStep = AssistantMessage | FauxResponseFactory` where the
    factory is `fn(context, options, state.callCount, model)` — responses can
    be **dynamic functions of the actual request**, not just static scripts.
  - `registerFauxProvider({models, tokensPerSecond, tokenSize})` returns a
    handle: `setResponses`, `appendResponses`, `getPendingResponseCount`,
    `state.callCount`, `unregister`.
  - Synthesizes usage from content (`estimateTokens = ceil(len/4)`), random
    IDs, normalized content blocks, `stopReason` control (incl. `"length"`,
    `"error"`), and streaming behavior (tokensPerSecond pacing).
- **`packages/coding-agent/test/suite/harness.ts`** (219 lines):
  `createHarness()` wires faux provider → in-memory model registry → in-memory
  auth/settings/session managers → `Agent` → `AgentSession` with real
  `convertToLlm`, extension runner, and resource loader; subscribes and
  captures **all session events** into an array with `eventsOfType<T>()`
  typed accessors; provides `tempDir` + `cleanup()`.
- **Conventions** (from pi AGENTS.md): regressions live in
  `test/suite/regressions/<issue>-<slug>.test.ts`; the suite must use the
  harness + faux provider — "No real provider APIs, keys, or paid tokens."
- **What that buys pi**: loop-mechanics tests for steering injection,
  truncated-tool-call refusal, follow-up continuation — the exact class of
  behaviors where yaah's current tests are thinnest at the seams.

### What yaah has today (read from `yaah/` source)

- **`internal/agent/agent_test.go`** (1,823 lines), loop-level only:
  - `fakeProvider`: static `responses []*types.ChatResponse` list, records
    `requests`, `failCount/maxFails/failErr` error injection. No factories,
    no usage synthesis, no accessor helpers, no stream synthesis.
  - `fakeStreamProvider`: fixed chunk list + `closeCh`, error injection.
  - `fakeTool`: name/result/err/delay + concurrency tracking
    (`concurrent`/`maxSeen` atomics) — good, worth keeping.
  - Behavior-named tests already cover: plain text, followup/steer channel
    injection, tool calling, max loop cycles, tool-result truncation, loop
    detection (incl. no-false-positive), parallel execution, concurrency caps,
    retry, token usage tracking, context-window trimming.
- **Provider seam is clean and narrow** (`internal/agent/llm/types.go`):
  `Provider.Send` + optional `StreamProvider.SendStream` — a faux provider
  drops in with zero loop changes.
- **Truncation handling exists** (`llm/client.go:110`, `llm/stream.go:221`):
  `finish_reason=length` + tool calls → error "discarding N tool calls".
  Contrast with pi: pi **synthesizes per-tool-call error results** and lets
  the loop continue (model re-issues calls in the same run). yaah fails the
  turn → errorclassify → possible checkpoint rewind → re-send (may re-truncate).
  The harness must be able to express this scenario; fixing the behavior is
  backlog item G1, not part of this port.
- **Pipeline middleware is config-composed** (`pipeline.PipelineConfig`),
  hooks emit `HookEvent` via `l.Hooks` — natural event-capture point.
- **Plans convention**: `.agents/plans/<slug>/plan.md` with frontmatter
  (this file). Benchmark history: `BENCHMARKS.md` (append-only rows; scenario
  methodology already proven — it surfaced the continuation-guard +52% finding).

## 3. Design decisions

1. **Faux provider lives in `internal/agent/llm/faux`** (new subpackage), not
   in a `_test.go` file. It must be importable by `cmd/yaah` (bench command),
   future regression tests, and examples. Non-test package, test-only usage.
2. **Factories over static lists as the primary abstraction.** A
   `ResponseStep` is either a `*types.ChatResponse` or
   `func(ctx context.Context, req types.ChatRequest, st *State) (*types.ChatResponse, error)`.
   `State` carries `CallCount`, `Requests` (recorded), and a mutable
   `Scratch map[string]any` so multi-step scenarios can branch on what the
   model actually sent (assert tool args, echo values, simulate compaction
   effects by shortening system prompt, etc.). Static responses are sugar:
   `faux.Say("done")`, `faux.CallTool("read", args)`, `faux.Err(...)`.
3. **Streaming is synthesized from the scripted message** (content deltas,
   tool-call-argument deltas, finish chunk) rather than scripted as raw
   chunks. Tests should express intent ("model calls read then answers"), not
   wire format. A `Chunked(bool)` option exercises the loop's streamed-path
   branches (`result.Streamed`, FlushEvent publication).
4. **Harness wires the real thing.** `testharness.New(t, opts)` builds a real
   `agent.Loop` with a real pipeline from a real `PipelineConfig`, real tools
   registry (fake/custom tools), memory persister, broker, and hook recorder —
   never a stubbed loop. Options: `WithResponses`, `WithResponseFactory`,
   `WithTools`, `WithMiddleware` (append/replace by name), `WithConfig`
   (mutate `LoopConfig`), `WithWorkspace(files map[string]string)` (materialized
   into `t.TempDir()`), `WithSubAgents(roles...)`.
5. **Event capture rides the existing hook bus**, not a new one: a
   `hookRecorder` implementing the loop's hook emission path records every
   `HookEvent` with turn/iteration metadata and exposes typed accessors
   (`EventsOf(events.ToolStart)`, `ToolCalls()`, `TurnCount()`). One capture
   point, zero loop changes.
6. **Regression suite convention**: `internal/agent/regressions/<slug>_test.go`
   (package `regressions_test`), one behavior per file, table-driven where
   natural, each file's header comment cites origin (issue, review finding, or
   cross-framework pattern). Seed set below locks behaviors yaah already fixed.
7. **Golden transcripts are JSONL fixtures**, one line per provider call:
   `{request: {messages, tools}, response: <scripted or recorded>}`. Replay
   mode diffs the request sequence the loop produced against the fixture —
   this is the loop-level analogue of the shepherd kernels' shared golden
   vectors, and it locks middleware ordering + message shapes cheaply.
   Fixtures live in `internal/agent/regressions/testdata/golden/`.
8. **Bench runner is a cobra command, not a test**: `yaah bench --scenario
   <name> [--model faux|<real>] [--trials N]` runs scenario definitions
   (Go funcs in `internal/bench/`) through the standard wiring path
   (`buildLoop`-equivalent) and appends a BENCHMARKS.md-shaped row to stdout
   or `--out`. Faux scenarios double as deterministic CI smoke: token counts
   are synthesized, so a pruning change that alters request sizes **shows up
   as a faux-token delta in CI** before any paid run.

## 4. Implementation phases

Strict TDD per house rules: each phase lands tests-first where the behavior
is new, and `go build && go vet && staticcheck && gofmt -l .` must stay clean.

### Phase 1 — Faux provider package (`internal/agent/llm/faux`) — ~1 day

- [ ] `types.go`: `ResponseStep`, `State`, `Options{Provider, Model,
      ContextWindow, Chunked, TokensPerSecond, UsageFactor}`.
- [ ] `faux.go`: `Provider` struct implementing `llm.Provider` **and**
      `llm.StreamProvider` (assert both at compile time:
      `var _ llm.StreamProvider = (*Provider)(nil)`).
- [ ] Script API: `SetSteps`, `AppendSteps`, `Pending`, `CallCount`,
      `Requests()` (deep-copied), `Scratch`.
- [ ] Constructors: `Say(text)`, `CallTool(name, argsJSON)`,
      `CallTools(...calls)`, `TextThenTools(...)`, `Fail(err)`,
      `FailNTimes(n, err)` (composes with scripted steps), `FinishLength()`
      (message with `FinishReason: "length"` + partial tool calls — the
      scenario the current loop mishandles), `Usage` synthesis
      (`ceil(chars/4) × context.DefaultEstimateFactor`).
- [ ] Stream synthesis from message (`stream.go`): content/toolcall deltas +
      finish; respect `ctx.Done()`; optional pacing.
- [ ] Tests (table-driven): interface satisfaction; static step playback;
      factory receives request + state; error injection then recovery;
      stream reassembly equals non-streamed message; `FinishLength` produces
      the exact error string the client emits today (locks the seam for G1).

### Phase 2 — Test harness (`internal/agent/testharness`) — ~1 day

- [ ] `harness.go`: `New(t, opts...) *Harness` per decision 4/5; `Run(ctx,
      prompt) (string, error)`; `RunErr`; workspace helpers (`WriteFile`,
      `ReadFile`, `Abs`); `Events()`, `EventsOf(kind)`, `ToolCalls()`,
      `Requests()`, `Turns()`, `Tokens()`; `Cleanup` registered via
      `t.Cleanup`.
- [ ] Memory persister (records persisted messages in order; `MsgIdx`
      semantics preserved) — reuse if one already exists in tests.
- [ ] `WithSubAgents`: register fake roles via the existing
      `subagent.RoleRegistry` + a `NewSubAgentLoop` factory wired to the same
      faux provider, so dispatch/fan-out scenarios run without network.
- [ ] Assert helpers: `AssertCalledTool(name, substring)`,
      `AssertRequestCount(n)`, `AssertPersistedOrder(roles...)`.
- [ ] Tests: harness self-test (two-step tool scenario passes; events
      captured in order; workspace materialized; concurrency cap middleware
      observable).
- [ ] Migrate 3 existing `agent_test.go` tests to the harness as proof
      (`TestLoop_toolCalling`, `TestLoop_steerChannelInjects`,
      `TestLoop_retryOnError`) — keep originals until Phase 3 lands, then
      dedupe.

### Phase 3 — Regression suite convention + seed tests — ~0.5 day

- [ ] Create `internal/agent/regressions/` + document the convention in
      `AGENTS.md` (test layout section).
- [ ] Seed regressions (all behaviors yaah already implements — these are
      locks, not fixes):
  - `overflow-adoption` — loop adopts compacted baseline when `Call`
    replaces messages (review findings B6/B7 cited in `loop.go`).
  - `synthesized-denial-results` — denied/dropped tool calls still get
    result messages so `tool_call_id` pairing holds
    (`pipeline.Step.SynthesizedResults`).
  - `orphan-tool-results` — pruning never orphans a tool result from its
    call (agent_orphan.go behavior).
  - `length-truncation-retry` — current behavior locked (turn fails,
    checkpoint restore path exercised); will be superseded by G1.
  - `steer-mid-tool-phase` — steer drains into next request, not lost.
  - `inline-limit-drops` — calls beyond `MaxInlineToolsPerTurn` dropped
    with synthesized results + `InlineDropped` count.

### Phase 4 — Golden transcripts — ~1 day

- [ ] `Record` mode: harness writes JSONL request/response transcript per
      scenario (hash-normalized: strip timestamps/IDs).
- [ ] `Replay` mode: diff produced request sequence vs fixture; failure
      output shows first diverging message.
- [ ] Record goldens for: 5 seed regressions + `b4-audit-mini` (below).
- [ ] CI: replay runs on every push; `go test ./internal/agent/regressions/ -run Golden`.

### Phase 5 — Benchmark scenario runner (`internal/bench` + `yaah bench`) — ~1.5 days

- [ ] Scenario type: `struct { Name string; Prompt string; Steps
      []faux.ResponseStep; Want tools?; Files map[string]string }`; scenarios
      are code, versioned with the repo.
- [ ] Ship `b4-audit-mini` (compressed B4 audit: 3 tools, 1 sub-agent),
      `ctx-pressure-20k` (forces compaction at `ContextWindow: 20000` — the
      exact knob BENCHMARKS.md used), `long-tail-steer`.
- [ ] `yaah bench` command: runs scenarios × trials, emits BENCHMARKS.md row
      (turns/subs/tools/orch-tokens/sub-tokens/time) to stdout/`--out`;
      `--model faux` default; real models opt-in.
- [ ] CI job (weekly): faux bench diff vs committed baseline row — fail on
      >5% token delta (excluding intended changes; update baseline via flag).
- [ ] Docs: section in `BENCHMARKS.md` explaining faux rows vs paid rows.

## 5. Risks & mitigations

| Risk | Mitigation |
|---|---|
| Faux provider drifts from real wire behavior (usage, finish reasons) | Golden tests pin the exact error strings/shapes the real clients emit today; borrow fixtures from `llm/client_test.go` |
| Harness grows a parallel config surface that hides real wiring bugs | Harness must build from the same `PipelineConfig`/wiring helpers the CLI uses (`cmd/yaah.buildLoop` extracted or reused), never a bespoke assembly |
| Golden transcripts churn on prompt wording changes | Normalize: exclude system-prompt content by default (`--include-system` opt-in), hash only structure + roles + tool calls |
| Regression package becomes a dumping ground | Convention: one behavior per file, header cites origin, no helpers beyond harness (new helpers go in testharness) |

## 6. Verification

- [ ] `go build . && go vet ./... && gofmt -l .` clean; `staticcheck ./...` clean.
- [ ] `go test ./...` green including new packages.
- [ ] Proof of port: steering-mid-tool-phase + length-truncation scenarios
      run with zero network (assert via `Requests()` and absence of any HTTP
      client construction).
- [ ] `yaah bench --scenario ctx-pressure-20k --model faux` emits a row whose
      token totals shift when the pruner is disabled (demonstrates the CI
      delta detector works).
- [ ] AGENTS.md updated (test layout + bench command); BENCHMARKS.md gains
      the faux-row documentation.

## 7. Sequencing note

This port is the enabler for backlog items G1 (length-truncation graceful
degradation) and G6/G7 (swe-pruner, cache-aligned pruning) — every one of
those lands cleaner with faux scenarios expressing the before/after. Land
Phases 1–3 first, then start pulling backlog P0 items through the new harness.
