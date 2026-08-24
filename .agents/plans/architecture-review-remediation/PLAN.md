---

# Architecture Review & Remediation Plan (2026-08-24)

> **For the implementing agent:** Work phase-by-phase, task-by-task. Phases 0–3
> touch overlapping files in `internal/agent`, `internal/tools`, and `cmd/yaah` —
> implement those sequentially. Phase 7 tasks are largely independent and can be
> parallelized. Every task states its own acceptance criteria; the global gates
> are listed at the end. Do not treat findings tables in other docs as
> authoritative — this plan was verified directly against the code on the review
> date.

**Goal:** Fix the verified security and data-integrity defects found in the
2026-08-24 code-only architectural review, make `main` green on all three CI
platforms, and pay down the structural debt that produced them — without
changing yaah's engine-view architecture, which the review found sound.

**Architecture:** No skeleton changes. yaah keeps its engine-view boundary
(`agent.Loop` → typed events → `View` consumers), the middleware pipeline with
injected hooks, the tool registry, and the `internal/*` layering the review
confirmed is correct (no import cycles; `mcp` self-contained; `providers` →
types/config only; `jobs` independent of `agent`). This plan repairs guarantees
the architecture promises but currently breaks: an authenticated perimeter,
fail-closed safety defaults, single-writer message state, and honest config
semantics.

**Tech Stack:** Go 1.25+, stdlib `net/http`, cobra/pflag, modernc.org/sqlite,
OpenTelemetry SDK, `testing` with table-driven subtests per repo convention.
Web UI change adds DOMPurify to the embedded `cmd/yaah/web/` vendor set. No new
Go dependencies.

---

## Part I — The review

### Method

Full review of the code only (docs disregarded, per request), on `main` at
`16681e3`, 2026-08-24:

- Eight parallel subsystem deep-dives (agent core, pipeline/providers, tools,
  composition root, sub-agents/jobs, MCP/ACP, presentation, state/config),
  cross-checked against direct reads of the load-bearing files.
- Objective gates run locally (Windows): `go build ./...`, `go vet ./...`,
  `gofmt -l`, `staticcheck`, `go test ./... -count=1`.
- Every Critical/High finding below was re-verified in source by a second
  reader, not just reported. Line numbers are as of the review date and may
  drift.

### Measured health

| Check | Result |
|---|---|
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `gofmt -l` | clean |
| `staticcheck` | clean (exit 0) |
| `go test ./... -count=1` | **3 failures on Windows** (see B1) |
| Size | ~34.7k prod lines / 249 files; ~23.7k test lines / 135 files |
| Largest non-test file | 691 lines (`internal/agent/runner/runner.go`) |
| Panics in non-test code | zero |

### Progress since the 2026-08-22 review — verified

The `architecture-improvements` and `pipeline-safety-refactor` plans landed.
Confirmed fixed in this pass:

- A1 sub-agent permission enforcement — `NewSubAgentPipeline` prepends
  `permission` when parent rules exist (`pipeline/config.go`).
- A2 strip-reasoning retry bound — `maxStripReasoningAttempts` cap in
  `llm/client.go`.
- A3 event exhaustiveness — `events/exhaustive_test.go` exists (source-scanning;
  see A2-finding below for its remaining gap).
- A4 broker re-arm on second Run — pinned by `TestLoop_secondRunDeliversEvents`
  (but hooks are still single-Run; see B2).
- B1 flag globals → `SessionOptions` explicit data (`session_options.go`).
- Approval/inline-limit/conflict-detect are real middleware with
  `SynthesizedResults` (per the safety refactor plan).

### Findings

Severity: CRIT = exploitable or loses user data; HIGH = broken behavior or
safety gap; MED = significant debt or latent risk; LOW = wart.

#### Security

| ID | Sev | Finding | Where |
|----|-----|---------|-------|
| S1 | CRIT | **Web UI → RCE chain.** No auth/token/Origin check on any endpoint; `/api/action` decodes JSON regardless of Content-Type (CSRF via no-preflight `text/plain` POST); `marked.parse` output goes to Alpine `x-html` with no sanitizer, so prompt-injected content executes JS that can drive `/api/action` → shell tool → RCE. `http.Server` has no `ReadHeaderTimeout`. | `cmd/yaah/web.go` (`handleAction`, server setup), `cmd/yaah/web/index.html:243,271,433-435` |
| S2 | CRIT | **`serve --http` unauthenticated.** Unknown session IDs are auto-registered; requests with no session header accepted (codified by tests). Anyone who can reach the port can `prompt`/`steer`/`compact` — arbitrary agent execution. | `internal/mcp/http_server.go` (`handlePost`), `cmd/yaah/serve.go` |
| S3 | HIGH | **Approval gating fails open.** `classifyDanger` returns false for any tool not in the registry; remote MCP tools can never implement `DangerClassifier`, so every MCP tool bypasses approval in all modes. | `internal/agent/agent_safety.go:56-64`, `internal/tools/tools.go:123-130` |
| S4 | HIGH | **SSRF guard bypassable.** DNS lookup failure fails open; check-time DNS is re-resolved by the fetch (rebinding TOCTOU); shared `http.Client{}` follows redirects without re-validation (allowed host → 302 → `169.254.169.254`). Zero tests. | `internal/tools/urlguard.go:69-75`, `internal/tools/http.go:26` |
| S5 | HIGH | **Self-update without integrity.** No checksum/signature on the downloaded release asset; bare `http.Get` (no timeout/context); Windows path launches detached `powershell -ExecutionPolicy Bypass` where `escapePSPath` escapes for single-quoted context but the script embeds double-quoted strings — paths with `"` or `$(…)` inject; no Windows rollback. Untested. | `internal/update/selfupdate.go` |
| S6 | HIGH | **Filesystem containment is opt-in and unconfigured.** PathValidator installed only with `--workspace`; default mode has no containment. `DenyPatterns` always passed `nil`; patterns match basename only; `EvalSymlinks` failure silently falls back to the unresolved path. | `cmd/yaah/wiring.go:79-82`, `internal/tools/path_validator.go` |
| S7 | MED | **Secret exfiltration surface.** Verbose OTel exports full prompts/args/results; hook JSONL has no rotation/size cap; `sessions.system_prompt` persists full prompts unredacted; stdio MCP servers inherit the full parent environment (`os.Environ()`). | `internal/observability/otel.go`, `internal/agent/events/hooks.go`, `internal/memory/memory.go`, `internal/mcp/client.go` |

#### Correctness bugs

| ID | Sev | Finding | Where |
|----|-----|---------|-------|
| B1 | HIGH | **`main` fails tests on Windows.** `TestApprovalMiddlewareViaPipeline` + `TestToPipelineConfig_ApprovalHooksWired` (registry platform-gates `bash` out on Windows, so registry-based classification returns false) and `TestStartEchoProducesOutput` (process timing). CI matrix includes `windows-latest`, so CI is red or was bypassed. | `internal/agent/{approval_pipeline,buildpipeline}_test.go`, `internal/process/process_test.go`, `internal/tools/tools.go:187-205` |
| B2 | HIGH | **Hooks die after the first Run of a reused Loop.** `teardown` calls `Hooks.Close()` before `Hooks.Emit(SessionEnd)`; `Close` nils the file but leaves `ok=true` and `sync.Once` consumed, so `SessionEnd` is never written and every later emit silently writes to a nil file. | `internal/agent/lifecycle_teardown.go:45-58`, `internal/agent/events/hooks.go` |
| B3 | HIGH | **`role edit` destroys the response contract.** The tool's `roleFrontmatter` omits the `Contract` field the canonical `subagent.RoleDef` has, so rewriting a role file drops its `contract:` block. Silent data loss in user files. | `internal/tools/role.go:28` vs `internal/agent/subagent/role_def.go:50` |
| B4 | HIGH | **Embedder arguments swapped.** Call passes `(BaseURL, Model, APIKey)`; signature is `(baseURL, apiKey, model, …)`. The API key is sent as the model name and vice versa — semantic memory search cannot work. | `cmd/yaah/memory.go:120`, `internal/memory/vector.go:50` |
| B5 | MED | **User-level plans searched in the wrong home dir.** `planSearchPaths` joins `config.HomeDir()` (= `~/.yaah`) with `.agents/plans` → searches `~/.yaah/.agents/plans`; `skillSearchPaths` correctly uses `os.UserHomeDir()` for `~/.agents/skills`. | `cmd/yaah/plan.go:11-30` vs `cmd/yaah/skill.go:139-167` |
| B6 | HIGH | **Message-state ownership ambiguous.** `runMiddleware` works on a local copy and writes `l.State.Messages = messages` back at ~8 points, while compaction can mutate `State.Messages` from inside `LLM.Call`; the later write-back silently discards it. No ownership invariant. | `internal/agent/loop.go` (`runMiddleware`), `internal/agent/tools.go:65` |
| B7 | MED | **Persistence cursor breaks after compaction.** Tail-persist assumes `MsgIdx == persisted count`; compaction replaces messages without resetting the cursor (safety-net persist silently no-ops). Turn restore rewinds `MsgIdx` without deleting rolled-back DB rows (orphan resurrection on resume). | `internal/agent/persist.go`, `internal/agent/turn_checkpoint_loop.go:83` |
| B8 | HIGH | **Quality gates bypass supervision.** Gate sub-agents are spawned via direct `Registry.Execute("spawn_subagent", …)` inside tool goroutines — skip `subAgentSem`, skip the watchdog, emit no events; verdict parsed by last-occurrence substring match on "PASS"/"FAIL"; gates skipped whenever any escalation parses (gaming vector). | `internal/agent/agent_tools.go:236-256` |
| B9 | MED | **StalenessMiddleware effectively inert.** Epoch advances only on tool results named `steer`/`followup`/`question` — no steer/followup tools exist (those arrive as injected user messages this middleware never observes); `pending` consumed by the first `PostTool`. Design ≠ reality. | `internal/agent/pipeline/staleness.go` |
| B10 | MED | **Config semantics lie.** (a) `middleware.enabled` REPLACES the default list — a config listing 10 middleware silently disables `inline_limit` and `conflict_detect`; (b) `prompt_caching` absent from `defaultPipelineNames`, so the boolean knob is dead unless the whole list is overridden; (c) `observability.otel.traces` ignored (hardcoded true in wiring); (d) loop-detect default triplicated with divergent values (5 / 5 / 4); (e) doctor duplicates the middleware list and has drifted (8 of 10 names). | `internal/agent/pipeline/config.go` (`resolvedPipelineNames`), `cmd/yaah/wiring_otel.go`, `internal/doctor/checks.go` |
| B11 | MED | **ACP gaps.** Unknown methods silently dropped (no `-32601`); turn completion never signaled (`DoneEvent` explicitly dropped); second `session/prompt` blocks on the stdin-read goroutine, stalling all protocol handling including `session/cancel`; snake_case/camelCase field mix. Zero tests in the package. | `internal/acp/server.go`, `view.go`, `ctrl.go` |
| B12 | MED | **View parity drift.** `sseView` silently drops `EscalationEvent` and writes an empty `{"type":""}` SSE frame for any unmatched event; TUI `AddToolEnd` shows the tool name as its own summary instead of using `toolfmt`; compaction summaries formatted three different ways across views. | `cmd/yaah/web_view.go`, `internal/tui/proxy.go`, `internal/toolfmt/` |
| B13 | MED | **MCP client spec-fragile.** No request/response ID correlation (any interleaved notification corrupts a call); no stdio timeouts (hung server blocks forever); `tools/list` pagination ignored; protocol version hardcoded; `autoDetectReader` returns EOF when the first 16 bytes are whitespace; `Info()` reports Connected whenever the process struct exists. | `internal/mcp/client.go`, `internal/mcp/http.go` |
| B14 | LOW-MED | **Lifecycle leaks.** Background process manager never stopped on session `Close()` (processes outlive the session); `jobs.CancelPending` has zero call sites; HTTP MCP client `Close` never sends DELETE. | `cmd/yaah/session.go:139`, `internal/jobs/manager.go`, `internal/mcp/http.go` |
| B15 | LOW-MED | **Per-turn provider reconstruction.** `resolveCompact`/`resolveFallback` construct fresh HTTP clients on every turn in `runPrompt`/`runHeadless`/`compactContext`. | `cmd/yaah/build_loop.go`, `cmd/yaah/provider_resolve.go` |
| B16 | LOW | **OAuth:** no token refresh or expiry handling; uses `http.DefaultClient` (no timeout). | `internal/providers/oauth.go` |
| B17 | LOW | **TUI warts.** `FlushEvent` queued as droppable (token output can stall under backpressure); thinking spinner never animates (`Advance` only called from tests); `messages.Build` uncalled; `approval.Show` interpolates raw tool args into tview tag text (tag injection). | `internal/tui/event_queue.go`, `components/thinking`, `components/messages`, `components/approval` |

#### Architecture debt

| ID | Sev | Finding | Where |
|----|-----|---------|-------|
| A1 | HIGH | `executeAndCollect` is one ~270-line closure mixing concurrency gates, events, watchdogs, quality gates, escalation parsing, persistence — untestable in branches and the root cause of B8. | `internal/agent/agent_tools.go:20` |
| A2 | MED | Five entrypoints duplicate wiring: `runServe`/`runServeHTTP` (~80 near-verbatim lines); `runPrompt`/`runHeadless`/`compactContext` loop setup; question/approval wiring ×3 with divergent timeout policies (TUI 30 s / web unbounded / ACP auto); model-list prefetch ×2. No shared driver bootstrap. | `cmd/yaah/serve.go`, `build_loop.go`, `tui.go`, `web.go`, `internal/acp/server.go` |
| A3 | MED | Loop↔ContextManager boundary leaks: shared `*LoopState` pointer aliasing, dual defaults population (`ctxMgr()` vs `applyDefaults()`), three compaction entry points, re-export shims kept only for tests. | `internal/agent/context_manager.go`, `lifecycle_init.go`, `agent_context.go` |
| A4 | LOW-MED | Four overlapping mutation tools (`edit`/`replace`/`sed`/`patch`); `replace` and `sed` overlap ~80%; `patch` is a hand-rolled, untested unified-diff applier. | `internal/tools/{edit,replace,sed,patch}.go` |
| A5 | LOW-MED | Three hand-rolled frontmatter parsers despite `yaml.v3` in go.mod (flat `key: value` only); skills/plans discovery copy-pasted; `instructions.Load` walk-up is dead code (only call site passes `cwd, cwd`); no size cap on injected AGENTS.md. | `internal/{skills,plans,instructions}/`, `cmd/yaah/wiring_prompt.go:26` |
| A6 | LOW-MED | `memory`: `schema_meta` written, never read; brute-force vector search with hand-rolled bubble sort; N individual UPDATEs per search; `system_prompt`/`compacted_summary` grow unredacted/unbounded. | `internal/memory/memory.go` |
| A7 | LOW | Globals against convention: `tools.SharedTraceStore`/`SharedScopeManager`, `supervisedSessions` registry, OTel global providers. (Atomic role registry is sanctioned.) | `internal/tools/`, `internal/observability/otel.go` |
| A8 | LOW | Layering inversions & heuristics: `llm/stream.go` imports `internal/tools` for `SendHeartbeat`; backoff has no jitter and ignores Retry-After; degenerate-stream detection matches an error string generated two files away. | `internal/agent/llm/` |
| A9 | LOW | Stale hardcoded knowledge: `modelinfo.go` context windows stop at gpt-4o/claude-3.5 era; three sources of thinking-model truth; TUI `usage.go` carries a stale model-price table. | `internal/providers/modelinfo.go`, `internal/tui/usage.go` |
| A10 | LOW | `runMiddleware` repeats the same write-back/FailTurn boilerplate at 6 sites; `ForceCompact` fakes state to bypass its own guards; default model `"deepseek-v4-pro"` hardcoded in engine code. | `internal/agent/loop.go`, `agent_context.go:137`, `lifecycle_init.go` |

#### Process & testing

| ID | Sev | Finding | Where |
|----|-----|---------|-------|
| P1 | HIGH | **Risk-inverted coverage.** Untested: `patch.go` (diff applier), `urlguard.go`, `selfupdate.go`, all of `internal/acp`, `internal/doctor`, hooks, provider HTTP paths (`Send`/`SendStream` for both clients, OAuth flow), `web.go`/`web_view.go`/`view_terminal.go`, `internal/control`. | — |
| P2 | MED | CI Windows leg effectively broken (B1); CI runs `go test -shuffle=on` but not `-race` (README suggests `-race`). | `.github/workflows/ci.yml` |
| P3 | LOW | Doc drift: AGENTS.md references deleted files (`subagent_runner.go`), calls the web view "WebSocket" (it is SSE); `tui.go` has a stale bubbletea comment. Flagged peripherally; docs were out of scope for the review itself. | `AGENTS.md`, `cmd/yaah/tui.go` |

### Strengths — what the plan must NOT touch

1. **Package layering.** Dependency direction is consistently downward with no
   cycles: `mcp` imports nothing internal; `providers` only types+config;
   `jobs` only types+observability (agent loop injected as a closure); `tools`
   never imports `agent`. Keep it that way when implementing every task below.
2. **`internal/types` as canonical format**, with wire lowering isolated in
   `providers/wire.go` and `anthropic.go`.
3. **Engine↔view boundary** — sealed `Event` interface, broker, single
   forwarder, serial delivery contract. Extend it (B12), don't replace it.
4. **Pipeline DI** — the pipeline never imports `config`; hooks injected as
   functions/interfaces. The `errorclassify`/`APIError` seam (typed-first,
   recovery hints) is the cleanest joint in the stack.
5. **Tool registry design** — single `leafTools` source of truth,
   `NewLeafTool` for curated registries, generation counter, PathValidator
   auto-injection.
6. **Persistence engineering** — WAL, busy_timeout, immediate txlock,
   idempotent message IDs, debounced writer.
7. **TUI concurrency design** — bounded queues, coalescing, single-flight
   incremental rendering, instrumented. Fix the specific warts (B17), keep the
   model.
8. **Test culture** — 0.68 test:prod line ratio, fakes over mocks, parity
   tests. The gaps are concentration, not absence.

---

## Part II — Development plan

Sequencing rationale: Phase 0 makes CI truthful so every later phase is
verifiable. Phases 1–2 stop active bleeding (security, data loss). Phase 3–4
fix the engine invariants that produced the bugs. Phase 5 makes config
predictable. Phases 6–8 are hardening and debt, independently shippable.

Total estimate: ~3–4 weeks part-time. Every phase ends with the global gates
green.

---

### Phase 0 — Guardrails: make CI truthful (≈1 day)

**Objective:** `go test ./...` green on Windows, macOS, and Linux; regression
tests written (red) for B2/B3/B4 so later phases fix against a target.

#### Task 0.1 — Make danger classification platform-independent (fixes B1a, hardens S3)

**Files:** modify `internal/agent/agent_safety.go`; test
`internal/agent/buildpipeline_test.go`, `approval_pipeline_test.go`.

Registry lookup stays the primary path, but add a fail-closed fallback for
names that are dangerous regardless of registration:

```go
// alwaysDangerous names are gated even when not present in the registry
// (platform-gated shell tools, unknown names). Fail closed, not open.
var alwaysDangerous = map[string]bool{
	"bash": true, "powershell": true, "shell": true,
}

func (l *Loop) classifyDanger(name, args string) bool {
	if t := l.Registry.Get(name); t != nil {
		if dc, ok := t.(tools.DangerClassifier); ok {
			return dc.IsDangerous(args)
		}
		return false
	}
	return alwaysDangerous[name]
}
```

Note: this deliberately does NOT make unknown/MCP names dangerous by default —
that policy decision belongs to Task 2.2 with a config knob. This task only
removes the platform accident.

**Acceptance:** the two approval tests pass unmodified on Windows; no
behavior change on POSIX (bash was already classified via the registry).

#### Task 0.2 — Fix the process test on Windows (fixes B1b)

**Files:** `internal/process/process_test.go`.

`TestStartEchoProducesOutput` assumes POSIX `echo` semantics. Gate the command
on `runtime.GOOS` (`cmd /c echo` on Windows) or poll for completion with a
deadline instead of a fixed 2 s sleep.

**Acceptance:** test passes on all three CI OSes.

#### Task 0.3 — Red regression tests for the data-integrity bugs

Write failing tests only; fixes land in Phases 2–3.

- B2: `internal/agent/hooks_lifecycle_test.go` — a reused Loop's second Run
  still emits hook events; `SessionEnd` present in the JSONL after teardown.
- B3: `internal/tools/role_test.go` — create a role with a `contract:` block,
  run the tool's `edit` action, re-read the file, assert the contract survived.
- B4: `cmd/yaah/memory_test.go` (or extend an existing test) — with a stub
  provider, assert the embedder request carries the model in the body and the
  key in `Authorization`, not swapped.

#### Task 0.4 — Add `-race` to CI

**Files:** `.github/workflows/ci.yml` — change the test step to
`go test -shuffle=on -race ./...` (matches the README's documented gate).

**Phase 0 acceptance:** `go test ./... -count=1` green locally on Windows; CI
green on all three OS legs; three red tests exist and are skipped or marked
with `t.Skip` + issue reference ONLY if a phase is deferred (prefer keeping
them red in-branch, fixed by the phase that owns them).

---

### Phase 1 — Security: web UI and serve perimeter (≈2–4 days)

**Objective:** close S1 and S2. Nothing in this phase changes agent behavior.

#### Task 1.1 — Web UI authentication + CSRF hardening (S1, first half)

**Files:** `cmd/yaah/web.go`, `cmd/yaah/web_view.go`.

- Generate a 32-byte session token at startup (override with `--token`);
  print it once to the terminal with the URL (`http://127.0.0.1:8080/?t=…`).
- Require the token on `/` (query, one-time redirect to cookie is acceptable)
  and on every `/api/*` route (header `X-Yaah-Token` or cookie). Constant-time
  compare.
- `/api/action` and `/api/commands`: reject requests without
  `Content-Type: application/json`, and reject requests whose `Origin`/`Host`
  header does not match the listen address (blocks cross-origin `text/plain`
  CSRF even without the token).
- Set `http.Server{ReadHeaderTimeout: 10 * time.Second}`.
- Single-client note: keep the current "second tab hijacks the stream"
  behavior but document it in the startup banner.

**Tests:** `cmd/yaah/web_test.go` with `httptest` — no token → 401;
`text/plain` cross-origin POST → 415/403; valid token → 202.

#### Task 1.2 — Sanitize rendered markdown (S1, second half)

**Files:** `cmd/yaah/web/index.html`, `cmd/yaah/web/` (vendor DOMPurify
minified, same pattern as the existing vendored libs).

- `md(t) { return DOMPurify.sanitize(marked.parse(t)) }` and the same wrap in
  `add()`.
- Keep `esc()` for non-assistant roles.

**Tests:** manual verification plus a unit-style test is impractical for the
embedded HTML; instead add a Go test that asserts the embedded index.html
contains no `x-html` bound to an unsanitized expression (source-scanning,
matching the precedent of `events/exhaustive_test.go`).

#### Task 1.3 — `serve --http` auth (S2)

**Files:** `internal/mcp/http_server.go`, `cmd/yaah/serve.go`.

- Require `Authorization: Bearer <token>` on all HTTP endpoints except
  `/health`. Token via `--token` flag or auto-generated + printed.
- Stop auto-registering unknown `Mcp-Session-Id`s unless a new
  `--allow-unknown-sessions` flag is set; keep the no-header convenience only
  behind that flag. Update the tests that codify the old behavior.
- `ReadHeaderTimeout` here too.

**Acceptance:** `curl` without token → 401; with token → works; existing HTTP
server tests updated and green.

---

### Phase 2 — Safety perimeter: tools and update (≈3–5 days)

#### Task 2.1 — Fix the SSRF guard (S4)

**Files:** `internal/tools/urlguard.go`, `internal/tools/http.go`,
`internal/tools/webfetch.go`; new `internal/tools/urlguard_test.go`.

- DNS lookup failure → **deny** (fail closed).
- Defeat rebinding and redirect bypass together: give the shared tool HTTP
  client a custom `DialContext` that resolves the host once, validates every
  resolved IP against `blockedCIDRs`, and dials the validated IP (pin via
  `addr` in the dialer). This covers re-resolution TOCTOU.
- Add `CheckRedirect` that re-runs `validateURL` on each hop target; stop on
  violation.
- Remove the global `ssrfGuardEnabled = false` in `http_test.go`; point tests
  at explicitly-allowed hosts or inject a test hook.

**Tests:** table-driven: loopback literal, private literal, localhost,
name resolving to private (fake resolver), redirect to loopback (httptest),
DNS failure → denied.

#### Task 2.2 — Close the MCP approval gap (S3)

**Files:** `internal/config/` (new knob), `internal/agent/agent_safety.go`.

Add `agents.mcp_approval: ask|allow|deny` (default `ask`). In
`classifyDanger`, after the registry miss and the `alwaysDangerous` check,
treat names registered as MCP-origin tools as dangerous when the knob says so.
Requires the registry (or the Loop) to know which names are MCP tools — the
MCP wiring in `cmd/yaah/wiring_mcp.go` knows this at registration time; record
the name set on the session and pass it into `LoopConfig`.

**Acceptance:** with `mcp_approval: ask`, a model-issued call to an MCP tool
routes through the approval middleware; `allow` reproduces today's behavior.
Document the default in `docs/configuration.md` when docs are touched.

#### Task 2.3 — Self-update integrity (S5)

**Files:** `internal/update/selfupdate.go`, release process note in
CONTRIBUTING (or release-please config).

- Release pipeline publishes `checksums.txt` (SHA-256 per asset); `Apply`
  downloads the checksums file, verifies the asset before replacing the
  binary, aborts on mismatch.
- `Download` takes a context; 60 s default timeout; no `http.DefaultClient`.
- Windows: fix `escapePSPath` to match the actual quoting context (embed paths
  single-quoted with `''` escaping, or pass via `-EncodedCommand`), and add the
  same rollback the Unix path has (keep a `.bak` copy, restore on failure).

**Tests:** httptest release server with good/bad checksums; quoting test with
hostile path strings (`"`, `$(calc)`).

#### Task 2.4 — PathValidator defaults (S6)

**Files:** `cmd/yaah/wiring.go`, `internal/tools/path_validator.go`,
`internal/config/`.

- Wire a default `DenyPatterns` set from config (`.env`, `*.pem`, `id_rsa*`,
  `*.key`) when a workspace is active; make it configurable.
- Support path-segment patterns, not basename-only (match against the
  workspace-relative path).
- When `EvalSymlinks` fails on an existing-path walk, deny rather than fall
  back to the unresolved path; keep the fallback only for non-existent leaves
  (the existing ancestor-resolution case).
- Keep containment opt-in (`--workspace`/config), but have `yaah doctor`
  report loudly when it is off.

**Acceptance:** `path_validator_test.go` extended for deny patterns and the
fail-closed symlink case.

---

### Phase 3 — Data-integrity bugs (≈2–3 days)

#### Task 3.1 — Hooks lifecycle (B2)

**Files:** `internal/agent/lifecycle_teardown.go`,
`internal/agent/events/hooks.go`; test from Task 0.3 goes green.

- Emit `SessionEnd` **before** `Close()` in `teardown`.
- Make `HookEmitter` reusable: `Close()` sets `closed=true`; `Emit` after close
  re-opens (replace `sync.Once` with a mutex-guarded lazy open that respects
  close/reopen). Alternatively re-create the emitter per Run in
  `applyDefaults` — choose one, document the invariant.

#### Task 3.2 — Single role parser (B3)

**Files:** `internal/tools/role.go`, `internal/agent/subagent/role_def.go`.

Delete `roleFrontmatter`/`parseRoleContent`/`marshalRoleFile` in the tool and
route through the `subagent` parser + a shared marshaler that round-trips every
`RoleDef` field (contract included). If the import direction objects (`tools`
must not import `agent/subagent`), extract the parser into a small leaf
package (`internal/rolefile`) both consume — same shape as the existing
`RoleResolver` inversion.

#### Task 3.3 — Embedder arguments (B4)

**Files:** `cmd/yaah/memory.go:120`. Swap to
`memory.NewEmbedder(p.BaseURL, p.APIKey, cfg.Embedding.Model, nil)` and also
call `cfg.Resolve()` first so `${VAR}` placeholders are substituted (currently
they reach the embedder literally). Test from Task 0.3 goes green.

#### Task 3.4 — Plans home dir (B5)

**Files:** `cmd/yaah/plan.go`. Use `os.UserHomeDir()` for the user-level
`.agents/plans` dir, mirroring `skillSearchPaths`. Add a unit test for both
path builders (they are pure given a cwd/home injection — refactor the home
lookup into a parameter if needed for testability).

#### Task 3.5 — Persistence cursor & orphans (B7)

**Files:** `internal/agent/persist.go`, `internal/agent/turn_checkpoint_loop.go`,
`internal/memory/` (new delete-range method).

- After compaction replaces `State.Messages`, reset `msgIdx` to 0 and persist
  the compacted slice as the new baseline (or rebuild IDs — they are
  deterministic, so re-persist is idempotent).
- On turn restore, delete message rows with `idx >= restored length` for the
  session before resetting `MsgIdx`.

**Tests:** compaction-then-persist appends correctly; restore-then-resume does
not resurrect rolled-back messages.

---

### Phase 4 — Loop integrity (≈3–5 days)

#### Task 4.1 — Single-writer message state (B6)

**Files:** `internal/agent/loop.go`, `tools.go`, `context_manager.go`.

Establish the invariant: **only `runMiddleware` writes `l.State.Messages`**.
Compaction triggered from inside `LLM.Call` must not mutate state directly —
instead the `Compactor` returns the compacted slice through the client's
overflow-recovery return path (or a callback field on `Step`), and the loop
applies it at the iteration boundary. Remove the ~8 scattered
`l.State.Messages = messages` write-backs in favor of one deferred write at
the end of each iteration plus the defined compaction point.

**Test:** fake provider that overflows on call N; assert the compacted state
survives to iteration N+1 and is persisted.

#### Task 4.2 — Extract the dispatch closure; route quality gates through it (A1, B8)

**Files:** `internal/agent/agent_tools.go` (+ new `agent_dispatch.go`).

- Extract a `toolDispatch` type owning one tool call's lifecycle: semaphore
  acquisition, events, watchdog, execution, truncation, result. The 270-line
  closure becomes a method on it; unit-test branches individually.
- Quality gates call the same dispatch path for the validator role — acquiring
  `subAgentSem`, arming the watchdog, emitting start/end events.
- Replace `gateVerdictFail` substring heuristic with a structured verdict: the
  gate role's contract gets a `verdict: PASS|FAIL` field (ContractDef already
  supports typed fields); fall back to the heuristic only when the field is
  absent, and log that the role contract needs updating.
- Do not skip gates when an escalation parsed; record escalation and gate
  independently.

#### Task 4.3 — Staleness: wire or delete (B9)

**Files:** `internal/agent/pipeline/staleness.go`, `steer.go`, `followup.go`,
`config.go`.

Preferred: have `SteerMiddleware`/`FollowupMiddleware` record injections on
the `Step` (e.g. `step.ContextInjected = true`), and let staleness advance its
epoch from that signal in `PostTool`/`PrepareStep`. If that proves awkward,
delete the middleware and its config entry rather than keep dead logic. Either
way the sub-agent pipeline doc comment referencing staleness gets updated.

#### Task 4.4 — Cache providers across turns (B15)

**Files:** `cmd/yaah/build_loop.go`, `cmd/yaah/provider_resolve.go`,
`cmd/yaah/session.go`.

Resolve compact/fallback providers once per session (and again only when the
model/provider config actually changes — e.g. `/model` switch), store them on
`agentSession`, reuse in `runPrompt`/`runHeadless`/`compactContext`.

---

### Phase 5 — Honest config (≈2 days)

#### Task 5.1 — One defaults owner

**Files:** `internal/config/load.go` (owner), `internal/agent/lifecycle_init.go`,
`internal/agent/pipeline/config.go`, `internal/agent/context/truncation.go`,
`cmd/yaah/provider_resolve.go`.

All "0 means default X" resolution happens in `internal/config` at load time;
downstream code sees concrete values and treats zero as zero. Fix the
loop-detect divergence (pick 5, delete the 4 fallback). Delete
`DefaultTruncateMaxLines/MaxBytes` duplication downstream.

#### Task 5.2 — Fix `enabled` semantics (B10a)

**Files:** `internal/agent/pipeline/config.go`, `internal/config/validate.go`,
`cmd/yaah/config.go` (doctor output).

Change `middleware.enabled` to mean "in addition to defaults" and let
`middleware.disabled` remove — or, if replacement semantics are kept, make
`config validate` warn when the enabled list omits defaults (`inline_limit`,
`conflict_detect`, …). Decision point: additive is safer for users; document
whichever ships. Add a parity test that the resolved pipeline for the shipped
example config contains exactly the documented defaults.

#### Task 5.3 — Wire or delete dead knobs (B10b, B10c)

- `prompt_caching: true` → include the middleware in the resolved pipeline
  when the flag is set, independent of the name list (builder-level insert,
  idempotent). Or delete the boolean and require the name-list entry — pick
  one, document.
- `observability.otel.traces` → honor it in `cmd/yaah/wiring_otel.go`.
- Fix doctor's middleware list by reading `pipeline.DefaultPipelineNames()`
  instead of maintaining a copy (B10e).

#### Task 5.4 — Doctor tests (P1 gap)

First tests for `internal/doctor`: each check gets a fixture-driven test; the
pipeline check asserts parity with the source of truth.

---

### Phase 6 — Protocol hardening (≈4–6 days)

#### Task 6.1 — MCP client correctness (B13)

**Files:** `internal/mcp/client.go`, `internal/mcp/http.go`,
`internal/mcp/tools.go`.

- Add `id` correlation: pending-request map keyed by JSON-RPC id; reader loop
  dispatches responses and routes notifications to a handler (log/drop
  initially, `list_changed` → re-fetch tools later).
- Honor `ctx` deadlines on stdio reads (per-call deadline goroutine or
  `SetReadDeadline` where the pipe allows it; otherwise a watcher goroutine
  that kills the process on timeout).
- Implement `tools/list` pagination (`nextCursor` loop with a sane cap).
- Negotiate protocol version: send supported list, inspect the server's
  choice, warn on mismatch.
- Fix `autoDetectReader` whitespace-EOF; make `Info().Connected` reflect an
  actual liveness check.
- Widen the `MCPClient` interface to include `Tools()` and remove the
  type-assertion fan-out in `StartMCPClientsFromConfig`; extract the shared
  initialize/fetchTools/callTool core both transports use.

**Tests:** fake stdio server fixtures (notifications interleaved before
response; paginated tools/list), httptest Streamable HTTP server.

#### Task 6.2 — ACP compliance (B11, P1)

**Files:** `internal/acp/*.go`; new `internal/acp/server_test.go`.

- Respond `-32601` to unknown methods.
- Signal turn completion: on `DoneEvent` emit `session/update` with stop
  reason (`end_turn`/`max_iterations`/`cancelled`) and resolve the prompt
  response then — stop dropping `DoneEvent`.
- Run each prompt in its own goroutine with a `done` channel; never block the
  stdin read goroutine; `session/cancel` must land while a prompt runs.
- Validate `session/prompt` session IDs against `session/new`.
- Pick one casing (camelCase to match the broader ACP ecosystem) and stick to
  it; alias the old names for one release if compatibility matters.

**Tests:** JSON-RPC fixture-driven suite over an in-memory pipe — this package
currently has zero tests.

#### Task 6.3 — Unify serve bootstrap (A2, first cut)

**Files:** `cmd/yaah/serve.go`.

Collapse `runServe`/`runServeHTTP` into one `runServeTransport(transport)` —
shared mutexes, usage counters, `ensureSession`, `registerServeTools`; only
the listener setup differs.

---

### Phase 7 — Structural debt (≈5–8 days; tasks largely independent)

#### Task 7.1 — Driver bootstrap (A2)

**Files:** new `cmd/yaah/driver.go`; refactor `tui.go`, `web.go`,
`repl_loop.go`, `serve.go`, `internal/acp/server.go` call sites.

One function produces: session + ctrl channel + forwarding loop +
question/approval wiring + model-list prefetch, parameterized by a small
`DriverOptions` (approval timeout policy, auto-answer policy). The three
divergent timeout policies become named presets, not copy-paste.

#### Task 7.2 — Enforce view parity (B12, A3-event)

**Files:** `internal/agent/events/exhaustive_test.go` (extend),
`cmd/yaah/web_view.go`, `internal/tui/proxy.go`.

- Extend the exhaustiveness test to scan `web_view.go` (and any future view) —
  every event type must appear in each consumer's switch; unmatched events
  must not emit empty wire frames (add a default case that logs/drops
  explicitly).
- `sseView`: handle `EscalationEvent`; drop the unconditional
  `v.write(marshalWire(we))` after the switch.
- TUI: pass `toolfmt.Summary` into `AddToolEnd` instead of the tool name.
- Consolidate compaction-summary formatting into one shared formatter
  (`toolfmt` or `events` helper).

#### Task 7.3 — One frontmatter parser (A5)

**Files:** `internal/skills/`, `internal/plans/`, `internal/tools/role.go` (or
the `internal/rolefile` package from Task 3.2), `internal/instructions/`.

Use `gopkg.in/yaml.v3` (already a dependency) everywhere; delete the
line-by-line parsers. Either wire `instructions.Load` to the real worktree
root (git toplevel / walk until marker) or delete the walk-up code. Add a size
cap on injected instruction content (config knob, default ~64 KB, truncate
with notice).

#### Task 7.4 — ContextManager boundary cleanup (A3)

**Files:** `internal/agent/context_manager.go`, `lifecycle_init.go`,
`agent_context.go`, `loop.go`.

- Populate ContextManager defaults in exactly one place.
- Collapse the three compaction entry points into one exported method the Loop
  and the `/compact` path both call.
- Delete the re-export shims; point the tests at `internal/agent/context`
  directly.

#### Task 7.5 — Safer file writes + tool overlap (A4)

**Files:** `internal/tools/{write,edit,patch,replace,sed,go_refactor,json_query,role}.go`.

- Atomic writes: temp file in the same directory + rename, preserving the
  existing file's mode (stat before, chmod the temp). Applies to all 11
  `os.WriteFile` call sites that modify existing user files.
- Then (separately, reviewable on its own): evaluate consolidating `sed` into
  `replace` (superset); deprecate rather than delete in the same release.

#### Task 7.6 — Memory hygiene (A6, S7 partial)

**Files:** `internal/memory/memory.go`, `message_repo.go`.

- Read `schema_meta` on open; fail fast on unknown future versions; keep the
  `ensureColumn` list but gate it by version.
- Batch `access_count` updates into one statement per search.
- Cap `sessions.system_prompt`/`compacted_summary` persisted length (truncate
  with a marker), and sort the vector results with `sort.Slice` (delete the
  bubble sort).

---

### Phase 8 — Coverage rebalance (ongoing, interleave with above)

Add tests where risk lives, roughly in this order:

1. `internal/tools/patch.go` — fixture-based: exact hunks, whitespace-tolerant
   hunks, near-miss hunks must NOT apply, multi-file diffs.
2. Provider HTTP layer — `httptest` fixtures for `OpenAIClient.Send/SendStream`,
   `AnthropicClient.Send/SendStream`, header/auth construction, live
   `APIError` shapes; OAuth device-flow happy path against a fake endpoint.
3. `llm.Client` — fallback swap, compaction-on-overflow, degenerate replay
   (currently 2 tests).
4. `internal/control` — message routing basics (currently zero tests).
5. Hooks rotation/size cap if implemented (S7); else at minimum the lifecycle
   test from Phase 3.
6. `cmd/yaah/view_terminal.go` and the web handlers beyond Phase 1's auth
   tests.

---

## Global gates (every phase)

```bash
go build ./...
go vet ./...
gofmt -l .                    # must be empty
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
go test -shuffle=on -race ./... -count=1
```

Plus: CI green on **ubuntu + macos + windows** legs (Phase 0 makes this
meaningful), and the `yaah-testing` skill smoke pass for any phase touching
the loop, sub-agents, or tracing.

## Out of scope / deferred (recorded, not planned)

- Provider interface inside `internal/providers` (consumer-side interfaces are
  idiomatic Go; leave as is).
- Vector index for memory search (brute force is fine at current scale;
  revisit when >10k entries).
- WebSocket migration of the web UI (SSE is fine; the README/AGENTS.md mention
  is the bug, fix the docs when they are next touched — P3).
- Replacing the `dangerousCommands` substring list — it self-documents as not
  a security boundary; approval + workspace containment are the real gates.
- MCP client reconnect/health-check UX (tracked in README "Future
  improvements"; Task 6.1 is correctness only).

## Appendix — maintainer config advisory (review date)

Findings specific to the `~/.yaah/config.yaml` in use during the review:

1. **Rotate the GitHub PAT** stored in plaintext under `mcp_servers.github` —
   treat it as exposed.
2. `middleware.enabled` (replacement semantics, see B10a) currently omits
   `inline_limit` and `conflict_detect`, so `max_inline_tools_per_turn: 12`
   has no effect and conflict detection is off. Add both names or remove the
   `enabled` list and use `disabled` for opt-outs.
3. `approval: allow` plus findings S3/S6 means nothing gates tool execution in
   that setup — deliberate for solo use, but switch to `ask` on any shared or
   networked machine, and note the MCP gap until Task 2.2 ships.
4. `shepherd_trace` is disabled in the middleware list while the supervised
   tools are configured — supervision is hard-gated on Shepherd tracing, so
   those roles currently run unsupervised. Re-enable or drop the supervised
   flags to match intent.
5. `lmstudio` provider carries a literal API key — harmless locally, but the
   `${VAR}` form keeps `yaah config show` output shareable.
