# Architecture Improvements Implementation Plan

> **For Hermes:** Use subagent-driven-development skill to implement this plan task-by-task.
> *(Phases modify overlapping files (`loop.go`, `config.go`, `client.go`) — implement phases sequentially yourself; delegate only independent single tasks within a phase if at all.)*

**Goal:** Close the four silent-failure gaps in the agent core, de-hidden the composition root, and fix type/interface drift found in the 2026-08-22 architectural review.

**Architecture:** No skeleton changes — yaah keeps its engine-view boundary (`agent.Loop` → typed events → `View` consumers), injection-first config, and `internal/tools` registry. The plan repairs guarantees the architecture already promises (event exhaustiveness, sub-agent permission enforcement, bounded recovery), then moves business logic out of `cmd/yaah`, then splits type-currency leaks. Each phase is independently shippable.

**Tech Stack:** Go 1.25+, cobra/pflag, modernc.org/sqlite, OpenTelemetry SDK. Pure Go, no CGo. Test gates: `go build ./... && go vet ./... && gofmt -l . && go test ./... -race -count=1`.

---

## Review findings this plan addresses

| ID | Severity | Finding | Where |
|----|----------|---------|-------|
| A1 | HIGH | Sub-agent `PermissionRules` plumbed but never enforced (no `permission` middleware in sub-agent pipeline) | `internal/agent/pipeline/config.go:101-164`, `runner/runner.go:320` |
| A2 | HIGH | Unbounded retry on `ShouldStripReasoning` (no attempt cap) | `internal/agent/llm/client.go:148-155` |
| A3 | HIGH | Event exhaustiveness not enforced anywhere; new events silently no-op in consumers | `tui/proxy.go`, `acp/view.go` |
| A4 | HIGH | Second `Run` on same Loop silently loses all events (broker closed after first Run, never recreated) | `lifecycle_init.go:153-157`, `lifecycle_teardown.go:36-37` |
| B1 | MED-HIGH | Six CLI-flag globals silently steer `newAgentSession` | `cmd/yaah/root.go:75-80` |
| B2 | MED | Business logic in cmd layer: OAuth device flow, self-update, trace query, YAML edit, skill scaffold | `cmd/yaah/{login,update,trace,mcp,skill}.go` |
| B3 | MED | Question/approval/model-list plumbing copy-pasted between tui.go and web.go (~70 lines, drifting) | `cmd/yaah/tui.go:163-205`, `cmd/yaah/web.go:112-183` |
| C1 | MED | Provider errors classified by parsing `err.Error()` strings | `llm/client.go:212-236`, `errorclassify/classify.go:20` |
| C2 | MED | Control-plane types live in "OpenAI message types" package; `CacheControl` is Anthropic-only field on neutral `Message` | `types/control.go`, `types/types.go:21` |
| C3 | LOW-MED | `NewTaskTool` takes 22 positional params incl. config blobs | `runner/runner.go:44` |
| C4 | LOW-MED | Direct `config.HomeDir()` reads inside agent/providers | `agent_truncation.go:62`, `providers/oauth.go:183` |
| D1 | LOW | `toolfmt` doc stale since tui2 promotion; sub-agent label logic duplicated 5× | `toolfmt/toolfmt.go:1-3`, `acp/view.go:63-92` |
| D2 | LOW | `PublishMustDeliver` holds RLock up to 50ms per slow subscriber (head-of-line blocking) | `pubsub/broker.go:54-64` |
| D3 | LOW | Race window on `BackgroundJobs.OnStart/OnEnd` func fields | `loop.go:307-337`, `jobs/manager.go:233-289` |
| D4 | LOW | SteerMiddleware triggers compaction (cross-concern) | `pipeline/steer.go:27-40` |
| D5 | DOC | AGENTS.md layout section stale (`agent_frame.go` gone; `wiring*.go`, `trace.go`, `login.go` unlisted) | `AGENTS.md` |

Out of scope (recorded, not planned): mcp/http_server.go split, persistence seam for `*memory.DB`, TUI question-tool UI-thread blocking redesign.

---

## Phase 0 — Guardrails (test-only, no behavior change)

### Task 0.1: Event exhaustiveness test

**Objective:** Adding a new `Event` type without updating consumers fails CI instead of silently no-oping.

**Files:**
- Create: `internal/agent/events/exhaustive_test.go`

**Step 1: Write failing test**

The test enumerates every event implementation via reflection over a registry list and asserts each consumer switch handles it. Simplest robust mechanism: a `AllEvents()` function in non-test code that returns one instance of every event type, plus a test that walks consumer files' source and asserts each event type name appears in their type switches.

```go
// internal/agent/events/exhaustive_test.go
package events_test

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/events"
)

// allEvents returns one instance of every concrete Event type.
// When you add an Event, ADD IT HERE. The exhaustive tests below will
// then force you to handle it in every View consumer.
func allEvents() []events.Event {
	return []events.Event{
		&events.TokenDeltaEvent{},
		&events.ThinkingEvent{},
		&events.FlushEvent{},
		&events.ToolStartEvent{},
		&events.ToolEndEvent{},
		&events.SubAgentStartEvent{},
		&events.SubAgentEndEvent{},
		&events.EscalationEvent{},
		&events.DoneEvent{},
		&events.CompactionStartedEvent{},
		&events.CompactionDoneEvent{},
	}
}

func TestAllEventsRegistryIsComplete(t *testing.T) {
	seen := map[reflect.Type]bool{}
	for _, e := range allEvents() {
		if seen[reflect.TypeOf(e)] {
			t.Errorf("duplicate event %T in allEvents()", e)
		}
		seen[reflect.TypeOf(e)] = true
	}

	// Cross-check against every *Event pointer type declared in
	// events.go so a new event struct can't dodge registration.
	eventsFile := "events.go"
	f, err := os.Open(eventsFile)
	if err != nil {
		t.Fatalf("open events.go: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "type ") || !strings.HasSuffix(line, "Event struct") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(line, "type "), " struct")
		found := false
		for typ := range seen {
			if typ.Elem().Name() == name {
				found = true
			}
		}
		if !found {
			t.Errorf("event %s declared in events.go but missing from allEvents()", name)
		}
	}
}

func TestConsumersHandleEveryEvent(t *testing.T) {
	consumers := []string{
		"../../tui/proxy.go",
		"../../acp/view.go",
		"../../../cmd/yaah/view_terminal.go",
	}
	for _, rel := range consumers {
		src := readFile(t, rel)
		for _, e := range allEvents() {
			name := reflect.TypeOf(e).Elem().Name()
			// Every consumer must reference the type name inside its
			// HandleEvent type switch (default fallthrough is allowed;
			// silence must be a deliberate `case ... : // ignore`).
			if !strings.Contains(src, "case *"+name) && !strings.Contains(src, "*"+name+":") {
				t.Errorf("%s does not handle %s", filepath.Base(rel), name)
			}
		}
	}
}

func readFile(t *testing.T, rel string) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(thisFile), rel)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
```

Adjust the exact event list to whatever `grep 'func (\*.*Event) eventMarker' internal/agent/events/events.go` reports when you implement.

**Step 2: Run test**

Run: `go test ./internal/agent/events/ -run TestAllEvents -v`
Expected: PASS (all current events registered and handled). If a consumer genuinely ignores an event deliberately, add it as `case *XxxEvent: // intentionally ignored`.

**Step 3: Commit**

```bash
git add internal/agent/events/exhaustive_test.go
git commit -m "test(events): enforce exhaustive handling of all agent events in consumers"
```

### Task 0.2: Loop single-use contract test

**Objective:** Document and pin the current "one Run per broker lifetime" behavior so Task 1.3 can change it safely.

**Files:**
- Create: `internal/agent/run_lifecycle_test.go`

**Step 1: Write test capturing today's behavior**

```go
package agent

import (
	"context"
	"testing"

	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

func TestLoop_secondRunDeliversEvents(t *testing.T) {
	fp := &fakeProvider{responses: []*types.ChatResponse{
		respText("first"), respText("second"),
	}}
	view := &collectingView{}
	loop := &Loop{
		Config: LoopConfig{SystemPrompt: "test", MaxLoopCycles: 5},
		Provider: fp, Registry: tools.NewRegistry(), View: view,
	}
	if _, err := loop.Run(context.Background(), "one"); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	first := len(view.doneEvents)
	if _, err := loop.Run(context.Background(), "two"); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if got := len(view.doneEvents); got != first+1 {
		t.Fatalf("second Run delivered %d DoneEvents, want %d (broker closed after first Run?)", got-first, 1)
	}
}
```

Add a tiny `collectingView` helper in the same file implementing `HandleEvent(Event)` that appends typed events (mirror any existing fake view in `agent_test.go` if one exists; reuse it instead). Add `respText(s string)` helper if absent.

**Step 2: Run**

Run: `go test ./internal/agent/ -run TestLoop_secondRun -v`
Expected: **FAIL** today ("broker closed after first Run?") — this pins finding A4.

Leave the test in place failing? No — mark `t.Skip("A4: broker not recreated across Runs — fixed in Phase 1 Task 1.3")` immediately after creating it, remove the skip in Task 1.3.

**Step 3: Commit**

```bash
git add internal/agent/run_lifecycle_test.go
git commit -m "test(agent): pin second-Run event delivery contract (currently broken, A4)"
```

---

## Phase 1 — Silent-failure fixes

### Task 1.1: Enforce parent permission rules on sub-agents (A1)

**Objective:** `parentPermissionRules` actually filter tool calls inside sub-agent loops.

**Decision (already made):** enforce via the existing `PermissionMiddleware` rather than deleting the plumbing — the rules are user-facing sandboxing semantics documented in `docs/configuration.md`.

**Files:**
- Modify: `internal/agent/pipeline/config.go:101-119,161-164`
- Modify: `internal/agent/pipeline/config_test.go` (if present, else create)
- Test: `internal/agent/pipeline/permission_test.go`

**Step 1: Write failing test**

```go
package pipeline

import (
	"context"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestNewSubAgentPipelineIncludesPermissionWhenRulesPresent(t *testing.T) {
	cfg := PipelineConfig{
		PermissionRules: []PermissionRule{{Tool: "bash", Path: "/etc/*", Mode: "deny"}},
	}
	p := NewSubAgentPipeline(cfg, nil)
	mw := p.Find("permission")
	if mw == nil {
		t.Fatal("sub-agent pipeline has no permission middleware despite PermissionRules being set")
	}
}
```

Check `NewSubAgentPipeline`'s real signature in `config.go:166-218` before writing; adapt the call.

**Step 2: Run to verify failure**

Run: `go test ./internal/agent/pipeline/ -run TestNewSubAgentPipelineIncludesPermission -v`
Expected: FAIL — no permission middleware built.

**Step 3: Implement**

In `config.go`, change `subAgentBuilders` to consult rules:

```go
var subAgentBuilders = map[string]func(PipelineConfig) Middleware{
	"permission": func(cfg PipelineConfig) Middleware {
		return &PermissionMiddleware{rules: cfg.PermissionRules}
	},
	"tool_concurrency": /* unchanged */,
	"shepherd_trace":   /* unchanged */,
}
```

And gate the name list:

```go
func SubAgentPipelineNames(disabled []string) []string {
	names := []string{"tool_concurrency", "shepherd_trace"}
	// permission is included only when rules exist, matching the
	// orchestrator's opt-in-by-config behavior.
	if cfgHasRules { names = append([]string{"permission"}, names...) }
	...
}
```

If `SubAgentPipelineNames` doesn't receive `PipelineConfig`, thread it through from `runner/runner.go` (the call site that already holds `parentPermissionRules`). Update the exclusion comment block (`config.go:146-156`) to document that `permission` IS enforced on sub-agents when rules are configured.

**Step 4: Verify pass + integration check**

```bash
go test ./internal/agent/pipeline/ -v
go test ./internal/agent/runner/ -v
```
Expected: PASS. Also confirm `makeTaskRunner` passes `parentPermissionRules` into the child loop's `PipelineConfig.PermissionRules` (it does today via `runner.go:320`; just verify end-to-end).

**Step 5: Commit**

```bash
git add internal/agent/pipeline/config.go internal/agent/pipeline/*_test.go
git commit -m "fix(pipeline): enforce parent permission rules in sub-agent loops (A1)"
```

### Task 1.2: Bound the strip-reasoning retry (A2)

**Objective:** Persistent reasoning-classified provider errors cannot spin `Call` forever.

**Files:**
- Modify: `internal/agent/llm/client.go:148-155`
- Test: `internal/agent/llm/client_test.go`

**Step 1: Write failing test**

Mirror the existing retry tests in `client_test.go` (reuse its fake `Provider` returning `failErr`):

```go
func TestCall_stripReasoningRetryIsBounded(t *testing.T) {
	calls := 0
	p := &fakeLLMProvider{send: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
		calls++
		return nil, errors.New("provider returned 400: reasoning_content is not supported by this model [msgs=3 roles=user]")
	}}
	c := &Client{Provider: p, Model: "m", MaxRetries: 3,
		StripReasoning: func() {}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.Call(ctx, types.ChatRequest{Model: "m"})
	if err == nil {
		t.Fatal("expected error after bounded retries")
	}
	if calls > 8 {
		t.Fatalf("provider called %d times — strip-reasoning retry is unbounded (A2)", calls)
	}
}
```

Verify the error string matches what `errorclassify.Classify` keys on (check `classify.go` patterns; use whichever message triggers `ShouldStripReasoning`).

**Step 2: Verify failure**

Run: `go test ./internal/agent/llm/ -run TestCall_stripReasoningRetryIsBounded -v`
Expected: FAIL or timeout — calls grows past bound / ctx expires.

**Step 3: Implement minimal fix**

In `client.go`, hoist a counter next to `compactAttempts` (declared near the top of the recovery loop) and gate the case:

```go
case classified.ShouldStripReasoning && stripAttempts < 3:
	req.Messages = stripReasoningContent(req.Messages)
	if c.StripReasoning != nil {
		c.StripReasoning()
	}
	c.replayCount = 0
	stripAttempts++
	attempt--
```

Falling through to the default branch when exhausted must return the classified error (verify the switch's fall-through behavior — the final `case` should surface the last error, matching how exhausted compaction behaves).

**Step 4: Verify pass + suite**

```bash
go test ./internal/agent/llm/ -race -count=1
go test ./internal/agent/ -count=1
```

**Step 5: Commit**

```bash
git add internal/agent/llm/client.go internal/agent/llm/client_test.go
git commit -m "fix(llm): cap strip-reasoning recovery at 3 attempts like other recovery paths (A2)"
```

### Task 1.3: Recreate broker per Run (A4)

**Objective:** `Loop.Run` is safe to call repeatedly; events flow on every Run.

**Files:**
- Modify: `internal/agent/lifecycle_init.go:153-157`
- Modify: `internal/agent/lifecycle_teardown.go:36-37`
- Modify: `internal/agent/view.go:41-67` (only if BrokerView needs re-arm support)
- Test: `internal/agent/run_lifecycle_test.go` (unskip Task 0.2 test)

**Step 1: Unskip the Task 0.2 test** (remove `t.Skip` line).

**Step 2: Implement**

Preferred shape — make teardown idempotent-per-run and creation unconditional:

`lifecycle_init.go`: replace

```go
if l.LLM == nil {
    if l.View != nil {
        l.broker = pubsub.NewBroker[Event]()
        l.brokerView = NewBrokerView(l.broker, l.View)
    }
    ...
```

with broker/view recreation guarded on closed state instead of `l.LLM == nil`. Concretely: move broker creation out of the `l.LLM == nil` block:

```go
if l.View != nil && l.brokerClosed() {
    l.broker = pubsub.NewBroker[Event]()
    l.brokerView = NewBrokerView(l.broker, l.View)
    l.rewireLLMCallbacks() // sets OnToken/OnThinking onto l.LLM
}
```

Track closure explicitly: `publishDone` currently closes broker+view; add a `l.brokerActive atomic.Bool` set true at creation, false in `publishDone`, and have `Publish` sites early-return when inactive. If `llm.Client` callbacks can't be rewired post-construction, recreate `l.LLM` too (it is cheap — same pattern as `applyDefaults`).

Simplest correct alternative if callback rewiring proves gnarly: keep `l.LLM == nil` guard but ALSO reset `l.LLM = nil` in `teardown`/`publishDone`, forcing full re-init next Run. Choose this if it passes tests with fewer moving parts; note it discards llm.Client swap state (sticky fallback) between runs — acceptable because cmd layer rebuilds loops per prompt today anyway, and the semantics become explicit.

**Step 3: Verify**

```bash
go test ./internal/agent/ -run TestLoop_secondRun -race -v   # PASS now
go test ./... -race -count=1                                  # full green
```

Also smoke-test the TUI path manually (`go run . tui` with any configured provider) to confirm no double-delivery of token deltas (the forwarder goroutine must be the only subscriber).

**Step 4: Commit**

```bash
git add internal/agent/
git commit -m "fix(agent): recreate broker/view per Run so repeated Runs deliver events (A4)"
```

---

## Phase 2 — Composition root cleanup

### Task 2.1: Replace CLI-flag globals with an options struct (B1)

**Objective:** `newAgentSession` receives all inputs as parameters; behavior no longer depends on hidden package state.

**Files:**
- Create: `cmd/yaah/session_options.go`
- Modify: `cmd/yaah/root.go:75-80` (keep flag vars as parse targets ONLY)
- Modify: `cmd/yaah/session.go`, `wiring.go`, `provider_resolve.go` (readers become parameter readers)
- Callers to update: `root_cmd.go`, `serve.go`, `tui.go`, `web.go`, `acp_cmd.go`

**Step 1: Define the struct**

```go
// session_options.go
package yaah

// SessionOptions carries everything the CLI flags contribute to agent
// construction. Built once in PersistentPreRun from parsed flags and
// passed explicitly — no function may read the raw flag globals.
type SessionOptions struct {
	ApprovalOverride  string
	ResumeSessionID   string
	DirectiveOverrides []string
	WorkspaceRoot     string
	AllowHomeAccess   bool
	WorkspaceAsk      bool
}

// sessionOptionsFromFlags is the ONLY place the flag globals are read.
func sessionOptionsFromFlags() SessionOptions {
	return SessionOptions{
		ApprovalOverride:  approvalOverride,
		ResumeSessionID:   resumeSessionID,
		DirectiveOverrides: directiveOverrides,
		WorkspaceRoot:     workspaceRoot,
		AllowHomeAccess:   allowHomeAccess,
		WorkspaceAsk:      workspaceAsk,
	}
}
```

**Step 2: Thread it**

Change signature: `newAgentSessionWithOptions(opts SessionOptions, skipMCP, skipOtel bool)`. Inside `resolveApproval`, `resolveDirectives`, and workspace validation, replace global reads with `opts.` field reads. Update all call sites to pass `sessionOptionsFromFlags()` (or a literal struct in serve mode, which wants defaults).

Same treatment for OTel globals: fold `extraOtelProcessors`/`otelInMemoryOnly` (`serve.go:25,31`) into `SessionOptions` (add `OtelProcessors []sdktrace.SpanProcessor`, `OtelInMemoryOnly bool`), set by `runServe` before calling the constructor.

**Step 3: Verify**

```bash
grep -n "approvalOverride\|resumeSessionID\|directiveOverrides\|workspaceRoot\|allowHomeAccess\|workspaceAsk\|extraOtelProcessors\|otelInMemoryOnly" cmd/yaah/*.go
# Expected: hits ONLY in root.go flag declarations + session_options.go
go build ./... && go test ./cmd/... ./internal/agent/... -count=1
go run . --help   # flags still listed
echo "hi" | go run . --approval-mode yolo "what is 2+2"   # manual flag-path check
```

**Step 4: Commit**

```bash
git add cmd/yaah/
git commit -m "refactor(cmd): pass CLI inputs explicitly via SessionOptions instead of package globals (B1)"
```

### Task 2.2: Extract self-update logic to internal/update (B2a)

**Objective:** `yaah update` becomes a thin shim; download/self-replace logic lives in `internal/update` (package already exists for release checking).

**Files:**
- Modify: `cmd/yaah/update.go` (390 lines → ~80-line cobra shim)
- Modify: `internal/update/` (add `selfupdate.go`)
- Move tests accordingly

**Steps:**
1. Move `downloadAsset`, binary replace, rollback, and `CleanOldBinary` into `internal/update/selfupdate.go` as `func Apply(ctx context.Context, repo, version string, log io.Writer) error` and `func CleanOldBinaries() error`.
2. Leave flag parsing, confirmation prompt, and progress printing in `cmd/yaah/update.go`.
3. Remove the `PersistentPreRun → CleanOldBinary()` side effect (`root.go:29`) — call it lazily inside the `update` command itself. Old binaries get cleaned when the user updates, not on every `yaah doctor`.
4. Tests: port any update.go tests to `internal/update/selfupdate_test.go` using `t.TempDir`.

**Verify:** `go build . && go vet ./... && go test ./internal/update/ -v`. Manual: `go run . update --check` (dry path).

Commit: `refactor(update): move self-update download/replace logic to internal/update (B2)`

### Task 2.3: Extract OAuth device flow to internal/providers/oauth (B2b)

**Objective:** `cmd/yaah/login.go:225` loses the device-flow sequencing (`loginOAuth`/`logoutOAuth`); it keeps only pickers + status output.

**Files:**
- Modify: `internal/providers/oauth.go` (add `DeviceFlow(ctx, DeviceFlowHooks) error` orchestrating request-device-code → poll → token-store write)
- Modify: `cmd/yaah/login.go` (shrink to provider selection + calling `providers.DeviceFlow`)

**Steps:**
1. Define hooks struct so presentation stays in cmd:
```go
type DeviceFlowHooks struct {
	VerificationURI func(uri string)      // print/open URL
	UserCode        func(code string)
	PollTick        func(interval time.Duration)
}
```
2. Move polling loop, interval math, error mapping into providers.
3. login.go keeps: numbered picker (deduplicate the two near-identical pickers at `repl_loop.go:155-222` while here — extract `pickProvider(prompt string, names []string) (string, error)` into `cmd/yaah/login.go` and reuse from both).

**Verify:** `go test ./internal/providers/ -v`; manual `go run . login` against a test provider config.

Commit: `refactor(oauth): move device-flow orchestration into internal/providers (B2)`

### Task 2.4: Extract trace query + YAML edit + skill scaffold (B2c)

Three independent mini-extractions, one commit each:

1. **`cmd/yaah/trace.go` (518 lines)** → query/format core to `internal/memory/trace_query.go` (or a new `internal/shepherd/` if trace store types live outside memory — follow the store's home). cmd keeps flag parsing + table rendering.
   Commit: `refactor(trace): move shepherd-trace querying into internal (B2)`
2. **`cmd/yaah/mcp.go` YAML mutation** → `internal/config/mcp_edit.go`: `AddMCPServer(cfgPath, name, transport, command string) error`, `RemoveMCPServer(...)`. cmd keeps UX.
   Commit: `refactor(config): move MCP server YAML editing into internal/config (B2)`
3. **`cmd/yaah/skill.go` scaffolding** → `internal/skills/scaffold.go`: `CreateSkill(dir, name, description string) error`.
   Commit: `refactor(skills): move SKILL.md scaffolding into internal/skills (B2)`

**Verify after each:** `gofmt -l . && go vet ./... && go build . && go test ./... -count=1`, plus the matching CLI smoke test (`go run . mcp list`, `go run . skill list`).

### Task 2.5: Shared interactive control-plane driver (B3)

**Objective:** One implementation of question/approval/model-list plumbing used by tui, web, and serve.

**Files:**
- Create: `cmd/yaah/control_driver.go`
- Modify: `cmd/yaah/tui.go:100-111,163-205`, `web.go:89-183`, `serve.go` lazy-session blocks

**Design:**

```go
// control_driver.go
// controlDriver wires the session's question/approval answer channels to
// a transport-specific deliver function. tui delivers via QueueUpdateDraw,
// web via SSE + HTTP response, headless serve via auto-timeout policy.
type controlDriver struct {
	sess *agentSession
	deliverQuestion func(q *types.CtrlQuestion) 
	deliverApproval func(a *types.CtrlApproval)
}

func (d *controlDriver) install() {
	d.sess.SetQuestionFn(func(q types.CtrlQuestion) chan string {
		ch := make(chan string, 1)
		go func() { d.deliverQuestion(&q); /* wait ch with timeout */ }()
		return ch
	})
	d.sess.SetApproveFn(...)
}
```

Concretely: extract the channel round-trip + timeout scaffolding shared by `tui.go:163-205` and `web.go:112-183`; each caller supplies only its delivery function. Web's extra timeouts/fallbacks become the driver's defaults (they are strictly better than tui's none — give tui a 120s default too, matching approval's 30s pattern but longer for free-form answers).

**Verify:** `go build . && go vet ./cmd/...`; manual smoke: `go run . tui` — ask a question-triggering prompt ("use the question tool to ask me X"); `go run . web` and answer via browser.

Commit: `refactor(cmd): unify question/approval control-plane plumbing in controlDriver (B3)`

---

## Phase 3 — Type & interface hygiene

### Task 3.1: Typed provider errors (C1)

**Objective:** `errorclassify` keys off structured fields, not `err.Error()` parsing.

**Files:**
- Create: `internal/providers/apierror.go`
- Modify: `internal/providers/providers.go:108`, `stream.go:88` (return `*APIError`)
- Modify: `internal/agent/errorclassify/classify.go` (+ `httpStatusCode` deletion in `llm/client.go:212-236`)
- Modify: `internal/agent/llm/client.go` (meta extraction via `errors.As`)

**Steps:**
1. Define the error type:
```go
// apierror.go
package providers

// APIError is a structured non-2xx provider response. errorclassify
// consumes StatusCode and Code directly — never parse Error() strings.
type APIError struct {
	StatusCode int
	Code       string // provider error_code field, e.g. "invalid_request_error"
	Message    string
	Body       string // truncated raw body, for diagnostics
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("provider returned %d (%s): %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("provider returned %d: %s", e.StatusCode, e.Message)
}
```
2. Both clients' non-2xx paths construct `&APIError{...}` instead of `fmt.Errorf`. Keep the human-readable format IDENTICAL (log scrapers/tests may match it) — dedupe the twin format strings at `providers.go:108`/`stream.go:88` into one constructor `newAPIError(status int, body []byte) *APIError` that also extracts the JSON `error.code`/`error.message`.
3. `errorclassify.Classify` gains a fast path:
```go
var apiErr *providers.APIError
if errors.As(err, &apiErr) {
	meta.StatusCode = apiErr.StatusCode
	meta.ErrorCode = apiErr.Code   // add field; match on it exactly
} else { /* legacy substring fallback stays for wrapped/transport errors */ }
```
4. Delete `httpStatusCode` string parsing from `client.go`; keep substring patterns only as fallback for transport-level errors.
5. Table-driven tests in `errorclassify/classify_test.go`: typed error hits exact codes even with arbitrary message text; plain `errors.New` still classifies via fallback.

**Verify:** `go test ./internal/providers/ ./internal/agent/errorclassify/ ./internal/agent/llm/ -race -count=1`.

Commit: `feat(providers): structured APIError; errorclassify prefers typed fields over message parsing (C1)`

### Task 3.2: Split control-plane types out of internal/types (C2a)

**Objective:** UI layers stop importing the "OpenAI message types" package for `CtrlMsg`.

**Files:**
- Create: `internal/control/` — WAIT: `internal/control/` already exists (per AGENTS.md layout). Inspect it first; likely the right home is there.
- Modify: `types/control.go` → move contents to `internal/control/msg.go` as `control.CtrlMsg` etc.
- Modify: importers: `internal/acp/ctrl.go`, `internal/acp/server.go`, `internal/tui/*`, `cmd/yaah/{session,tui,web,web_view,serve}.go` — mechanical import rewrite, keep type aliases temporarily if the diff balloons.

**Steps:**
1. `ls internal/control/` and read it. If it holds the channel plumbing already, move `CtrlMsg` variants there. Otherwise create the file there.
2. Mechanical move + `sed`-style import updates. Do NOT rename fields in this task.
3. Full build + race tests; grep for leftover `types.Ctrl` references: `grep -rn "types\.Ctrl" --include="*.go" . | grep -v .scratch` → expect zero.

Commit: `refactor(control): move CtrlMsg family from internal/types to internal/control (C2)`

### Task 3.3: Lower CacheControl out of the neutral Message type (C2b)

**Objective:** Anthropic cache-control markers travel via provider-side metadata, not a field on the universal currency.

**Steps:**
1. Grep all users: `grep -rn "CacheControl" --include="*.go" internal/ cmd/`.
2. Expected users: `providers/anthropic.go` (raise/lower), `pipeline/prompt_caching` middleware, possibly `tools` for pinned system prompts.
3. Introduce `Message.ProviderMeta map[string]any` ONLY if more than Anthropic needs extension points; otherwise invert: prompt_caching middleware tags messages with a dedicated wrapper type defined in pipeline (`CachedMessage{Message, CachePoint bool}`) and anthropic lowering unwraps it. Choose the smaller diff; document choice in the commit body.
4. Delete `CacheControl` from `types/types.go:21-27`; fix compile errors; ensure Anthropic prompt-cache E2E behavior preserved via existing prompt_caching middleware tests.

**Verify:** `go build ./... && go test ./internal/providers/ ./internal/agent/pipeline/ -race -count=1`.

Commit: `refactor(types): remove Anthropic-specific CacheControl from neutral Message (C2)`

### Task 3.4: NewTaskTool options struct (C3)

**Objective:** Kill the 22-parameter constructor.

**Files:**
- Modify: `internal/agent/runner/runner.go:44-76`
- Modify: sole caller `cmd/yaah/wiring.go` (task-tool construction site)

**Steps:**
1. Rename `taskRunnerOpts` usage upward:
```go
type TaskToolOpts struct {
	Provider       agent.Provider
	SystemPrompt   string
	ModelName      string
	DB             *memory.DB
	SessionID      string
	SubCfg         config.SubAgentConfig
	RoleNames      []string
	OtelEnabled    bool
	OtelVerbose    bool
	Tracker        *tools.ConflictTracker
	EstimateFactor float64
	SubContextWindow int
	OutputLimit    int
	ProviderMap    map[string]config.Provider
	Defaults       config.Defaults
	ParentPermissionRules []pipeline.PermissionRule
	PathValidator  *tools.PathValidator
	ResolveProviderByName func(map[string]config.Provider, string) agent.Provider
}

func NewTaskTool(opts TaskToolOpts) *tools.TaskTool { ... }
```
2. Update `wiring.go` call site to a keyed struct literal (compiler finds every field).
3. `gofmt`, build, run `go test ./internal/agent/runner/ -count=1`.

Commit: `refactor(runner): NewTaskTool takes TaskToolOpts struct (C3)`

### Task 3.5: Remove config.HomeDir reach-ins (C4)

**Files:**
- Modify: `internal/agent/agent_truncation.go:62` — inject the home dir (or the resolved transcript path) via `LoopConfig` at construction.
- Modify: `internal/providers/oauth.go:183` — take token store path as a constructor param; `cmd/yaah/provider_resolve.go` passes `config.HomeDir()` at the composition root.

**Verify:** `grep -rn "config\." internal/agent/*.go internal/providers/*.go | grep -v _test | grep -v "^.*://" ` → zero hits in agent (runner's config-typed opts fields are values, acceptable until Task 3.4 lands); `go test ./... -count=1`.

Commit: `refactor: inject home-dir-dependent paths instead of importing config in agent/providers (C4)`

---

## Phase 4 — Consolidation & low-severity hardening

### Task 4.1: Consolidate sub-agent label rendering (D1)

**Files:**
- Modify: `internal/toolfmt/toolfmt.go:170-186` (canonical `SubagentLabel`)
- Modify: `internal/acp/view.go:63-92`, `cmd/yaah/view_terminal.go:68-91` (call toolfmt)
- Modify: `internal/toolfmt/toolfmt.go:1-3` (doc comment: consumers are ACP view + terminal/web views, NOT the tview TUI)
- Optional: wire `toolblock.Complete` summary through `toolfmt.Summary` again if trivially compatible — otherwise delete the stale claim only.

**Verify:** `go test ./internal/toolfmt/ ./internal/acp/ -count=1 && go run . acp-serve --help`.
Commit: `refactor(toolfmt): single source for sub-agent labels; fix stale doc (D1)`

### Task 4.2: Fix PublishMustDeliver head-of-line blocking (D2)

**Files:**
- Modify: `internal/pubsub/broker.go:50-65`

**Approach:** snapshot subscribers under RLock, release, THEN deliver with per-subscriber timeout:

```go
func (b *Broker[T]) PublishMustDeliver(evt T) {
	b.mu.RLock()
	subs := slices.Clone(b.subs)
	b.mu.RUnlock()
	for _, s := range subs {
		select {
		case s.ch <- evt:
			atomic.AddInt64(&b.delivered, 1)
		case <-time.After(pubTimeout):
			atomic.AddInt64(&b.dropped, 1)
		}
	}
}
```

Reuse ONE timer via `time.NewTimer` + `Reset` across the loop instead of `time.After` per subscriber. Note in the doc comment: ordering across concurrent publishers is no longer globally serialized (acceptable — token deltas publish via `Publish`; verify which path TokenDelta uses and preserve its lock discipline).

**Verify:** `go test ./internal/pubsub/ -race -count=1`; add a test: slow subscriber (full buffer) delays neither `Publish` nor a second `PublishMustDeliver` beyond its own 50ms budget.

Commit: `fix(pubsub): don't hold broker lock while waiting on slow subscribers (D2)`

### Task 4.3: Synchronize BackgroundJobs hook fields (D3)

**Files:**
- Modify: `internal/jobs/manager.go` — guard `OnStart`/`OnEnd` with a `sync.RWMutex` or convert to `atomic.Pointer[func(...)]` pairs.
- Modify: `internal/agent/loop.go:300-337` wire/unwire to use the accessors.

Prefer accessors `SetHooks(onStart, onEnd func(...))` + internal RWMutex; job goroutines snapshot both funcs under RLock before invoking.

**Verify:** `go test ./internal/jobs/ ./internal/agent/ -race -count=1` (existing tests plus a hammer test: spawn job while toggling hooks 100×).
Commit: `fix(jobs): synchronize background-job hook fields against Run-boundary writes (D3)`

### Task 4.4: Decouple SteerMiddleware from compaction (D4)

**Files:**
- Modify: `internal/agent/pipeline/steer.go:12,27-40`
- Modify: `internal/agent/pipeline/config.go` (default chain builder injects the Compactor into steer OR registers a tiny `steerDrainHook` consulted by compaction middleware)

Chosen design: keep trigger in steer but INVERT the dependency — steer exposes `OnDrain func(ctx context.Context)`, and the composition site (`lifecycle_init.go` where pipelines assemble) wires `steer.OnDrain = compaction.Trigger`. Pipeline package no longer has steer→compaction knowledge.

**Verify:** `go test ./internal/agent/ -count=1` (compaction-on-drain behavior test exists or gets added).
Commit: `refactor(pipeline): steer notifies drain via hook instead of holding Compactor (D4)`

### Task 4.5: Refresh AGENTS.md + stale docs (D5)

**Files:**
- Modify: `AGENTS.md` repo-layout section: remove `agent_frame.go`, `repl_loop.go` description stays, add `wiring.go`, `build_loop.go`, `session_options.go`, `serve_tools.go`, `trace.go`, `login.go`, `compact_cmd.go`, `resume.go`, `quickref.go`, `update.go`; update Engine-View section: exhaustiveness now enforced by `internal/agent/events/exhaustive_test.go` (Task 0.1), so soften "the compiler will find missing cases" to cite the test.
- Modify: `docs/architecture.md`, `docs/features.md` where they reference moved code (Tasks 2.x).
- Modify: `internal/toolfmt` doc (done in 4.1).

**Verify:** manual read-through; `grep -n "agent_frame" AGENTS.md docs/` → zero.
Commit: `docs: refresh repo layout and engine-view notes to match tree (D5)`

---

## Execution order & dependencies

```
Phase 0 (guardrails) ──► Phase 1 (A1→A2→A4 sequential; all touch agent core)
Phase 2 tasks are independent of Phase 1 except 2.1 before 2.4 (options struct changes signatures the extractions also touch).
Phase 3: 3.1 standalone; 3.2 BEFORE 3.3 (both touch types/); 3.4 before 3.5 (both touch runner/wiring).
Phase 4 anytime after Phase 1; 4.5 LAST (documents final layout).
```

Suggested PR slicing: one PR per phase, or per-task PRs for Phase 2/3 (each is independently revertable).

## Final acceptance checklist

- [ ] `go build ./... && go vet ./... && staticcheck ./... && gofmt -l .` all clean
- [ ] `go test ./... -race -count=1` green
- [ ] Exhaustiveness test fails when a dummy 12th event is added and removed (proves the guard works)
- [ ] Sub-agent with a deny-rule for `/etc/*` bash calls: rule enforced (manual test via supervised_task)
- [ ] Strip-reasoning storm terminates ≤3 attempts (unit-proven)
- [ ] Two consecutive `Run`s on one Loop both emit `DoneEvent` (unit-proven)
- [ ] `grep -rn "approvalOverride\|extraOtelProcessors"` shows globals only in root.go/session_options.go
- [ ] `cmd/yaah` largest file < 260 lines (was trace.go at 518)
- [ ] `AGENTS.md` matches `ls cmd/yaah` exactly
