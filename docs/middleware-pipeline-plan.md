# Middleware Pipeline Plan

Refactors `internal/agent/agent.go` from a monolithic inline loop into a
composable middleware pipeline, modeled on deepagents' `AgentMiddleware`
and crush's `PrepareStep` callback pattern. All existing behavior is
preserved via default middleware — no breaking changes.

## Relationship to `docs/agent-loop-comparison.md`

The middleware pipeline does **not** supersede the feature recommendations
in the comparison doc. It is the *implementation mechanism* for several of
them. The comparison doc lists *what* to build; this doc describes *how*
to structure it.

| Comparison doc recommendation | Maps to middleware | |
|---|---|---|
| (#1) Path-pattern permissions | `PermissionMiddleware` | New |
| (#2) Prompt caching | `PromptCachingMiddleware` | New |
| (#3) Context-limit → auto-compact | `CompactionMiddleware` (extends existing) | Enhanced |
| (#5) Sub-agent lifecycle | `SubAgentMiddleware` (extends existing) | Enhanced |
| (#8) Tool concurrency cap | `ToolExecutionMiddleware` | New |
| (#9) Tool-pair summarization | `CompactionMiddleware` sub-feature | New |
| (#10) Preflight compaction | `CompactionMiddleware` pre-step | New |
| (#4) Proper token counting | Independent infra — used by middleware | Separate |
| (#6) Session persistence | Independent infra — hooks into middleware | Separate |
| (#7) Error classification + fallback | Independent infra — wraps provider | Separate |

## Difficulty

**Low.** Go's middleware pattern is well-understood (same as `http.Handler`
chaining). The existing `Loop.Run()` is ~100 lines with clear boundaries.
The real work is extracting 5 independent middleware implementations, not
inventing new abstractions. For the developer: 2-3 days end to end.

## Config-driven compatibility

Add to `~/.yaah/config.yaml`:

```yaml
agent:
  mode: middleware                # "legacy" for current inline behavior
  middleware:
    enabled:
      - steer
      - followup
      - compaction
      - approval
      - loop_detection
    disabled: []
```

- `legacy` mode calls the current `Run()` verbatim — zero risk.
- `middleware` mode with the default set of: steer, followup, compaction,
  approval, loop_detection reproduces the exact current behavior.
- Users can add/remove middleware names to customize the pipeline.
- Middleware not listed in `enabled` simply doesn't run.
- The default is `middleware` once stable; `legacy` is a deprecation path.

## Architecture

### Middleware interface

```go
// internal/agent/middleware.go

// Step is the mutable state passed through the pipeline at each iteration.
type Step struct {
    Messages   []types.Message
    Tools      []types.ToolDef
    Iteration  int
    Model      string
    SystemPrompt string
}

// Middleware intercepts the agent loop at well-defined points.
// Each method receives the current step and returns a (possibly modified) step.
// Returning an error halts the loop; returning nil for postTool result is ignored.
type Middleware interface {
    Name() string

    // PrepareStep is called before each model call. The middleware may:
    // - Inject/remove messages (steer, followup, compaction)
    // - Filter tools (permissions, tool exclusion)
    // - Add system prompt fragments (memory, skills)
    // - Short-circuit by modifying messages to skip the model call
    PrepareStep(ctx context.Context, step *Step) (*Step, error)

    // PostModel is called after the model responds, before tool execution.
    // The message is the raw assistant response. Middleware may:
    // - Inspect for truncation / finish reasons
    // - Modify the message content
    // - Inject additional messages
    PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error)

    // PostTool is called after all tools in this iteration have executed.
    // results is nil if the model returned no tool calls.
    // Middleware may:
    // - Check for loops (loop detection)
    // - Track tool usage stats
    // - Trigger compaction based on token counts
    PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error)
}

type ToolResult struct {
    Name     string
    Args     string
    Result   string
    Error    error
    Duration time.Duration
}
```

Alternative: a simpler single-hook interface matching crush/deepagents:

```go
type Middleware interface {
    Name() string
    PrepareStep(ctx context.Context, step *Step) (*Step, error)
}
```

Everything else (post-model, post-tool) becomes tool-side hooks or
internal loop logic. For yaah, the three-hook interface above is
recommended — it mirrors the existing inlined structure precisely and
avoids scattering logic across tools.

### Pipeline runner

```go
// internal/agent/pipeline.go

type Pipeline struct {
    middleware []Middleware
}

func NewPipeline(middleware ...Middleware) *Pipeline {
    return &Pipeline{middleware: middleware}
}

func (p *Pipeline) Run(ctx context.Context, step *Step) (*Step, error) {
    var err error
    for _, mw := range p.middleware {
        step, err = mw.PrepareStep(ctx, step)
        if err != nil {
            return step, fmt.Errorf("%s: %w", mw.Name(), err)
        }
    }
    return step, nil
}
```

### Refactored Loop

```go
// internal/agent/agent.go

func (l *Loop) Run(ctx context.Context, userInput string) (string, error) {
    l.applyDefaults()

    messages := l.initMessages(userInput)

    pipeline := l.buildPipeline()

    for iter := 0; iter < l.MaxIterations; iter++ {
        if ctx.Err() != nil {
            l.Messages = messages
            return "", ctx.Err()
        }

        step := &Step{
            Messages:     messages,
            Tools:        l.buildToolDefs(),
            Iteration:    iter,
            Model:        l.Model,
            SystemPrompt: l.SystemPrompt,
        }

        step, err := pipeline.Run(ctx, step)
        if err != nil {
            l.Messages = messages
            return "", err
        }

        req := types.ChatRequest{
            Model:    l.Model,
            Messages: step.Messages,
            Tools:    step.Tools,
        }

        msg, streamed, err := l.getAssistantMessage(ctx, req)
        if err != nil {
            l.Messages = messages
            return "", fmt.Errorf("provider error: %w", err)
        }
        messages = append(messages, msg)

        if len(msg.ToolCalls) == 0 {
            l.Messages = messages
            return msg.Content, nil
        }

        if streamed && msg.Content != "" && l.OnFlush != nil {
            l.OnFlush(msg.Content)
        }

        toolResults := l.executeAndCollect(ctx, msg.ToolCalls, &messages)

        for _, mw := range pipeline.middleware {
            step, err = mw.PostTool(ctx, toolResults, step)
            if err != nil {
                l.Messages = messages
                return "", err
            }
        }
    }

    l.Messages = messages
    return "", fmt.Errorf("max iterations (%d) reached", l.MaxIterations)
}

func (l *Loop) buildPipeline() *Pipeline {
    if len(l.Middleware) > 0 {
        return NewPipeline(l.Middleware...)
    }
    return NewPipeline(
        &SteerMiddleware{ch: l.Steer, compact: l.compactContext},
        &FollowupMiddleware{ch: l.FollowUps},
        &CompactionMiddleware{
            window:     l.ContextWindow,
            provider:   l.Provider,
            compactProv: l.CompactProvider,
            compactModel: l.CompactModel,
        },
        &ApprovalMiddleware{mode: l.ApprovalMode},
        &LoopDetectionMiddleware{
            history: &l.loopHistory,
            count:   l.LoopDetectCount,
            window:  l.LoopDetectWindow,
        },
    )
}
```

The existing `Run()` becomes a thin orchestrator. The loop body shrinks
from ~100 lines to ~50. Each behavioral concern lives in its own file.

## Existing behavior → middleware mapping

### `SteerMiddleware` — replaces lines 166–177

```go
// internal/agent/middleware_steer.go

type SteerMiddleware struct {
    ch      <-chan string
    compact func(ctx context.Context) // optional hook on injection
}

func (m *SteerMiddleware) Name() string { return "steer" }

func (m *SteerMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
    if m.ch == nil {
        return step, nil
    }
    select {
    case msg, ok := <-m.ch:
        if ok && msg != "" {
            step.Messages = append(step.Messages, types.UserMsg("[STEER] "+msg))
            if m.compact != nil {
                m.compact(ctx)
            }
        }
    default:
    }
    return step, nil
}
```

### `FollowupMiddleware` — replaces lines 179–188

```go
// internal/agent/middleware_followup.go

type FollowupMiddleware struct {
    ch <-chan string
}

func (m *FollowupMiddleware) Name() string { return "followup" }

func (m *FollowupMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
    if m.ch == nil {
        return step, nil
    }
    select {
    case msg, ok := <-m.ch:
        if ok && msg != "" {
            step.Messages = append(step.Messages, types.UserMsg(msg))
        }
    default:
    }
    return step, nil
}
```

### `CompactionMiddleware` — replaces lines 217–219 and extends

Triggers on `PrepareStep` (preflight) and `PostTool` (post-iteration).
Uses the same `compactContext` logic from the current code.

### `ApprovalMiddleware` — replaces per-tool approval in `executeToolsParallel`

Moves the approval check from inside the tool execution function into
`PostModel` — inspects tool calls and either approves, denies, or
prompts. Result is injected as a synthetic tool result before execution.

### `LoopDetectionMiddleware` — replaces hash tracking in `executeToolsParallel`

Moves hash computation and sliding-window check from the tool execution
function into `PostTool`. The `executeToolsParallel` function no longer
needs to track or detect loops — it just executes tools.

## Implementation phases

### Phase 1: Interface + pipeline (no behavior change)

1. Create `internal/agent/middleware.go` with the `Middleware` interface
   and `Step` struct.
2. Create `internal/agent/pipeline.go` with `Pipeline` type.
3. Create 5 middleware files, each wrapping the existing inline logic
   verbatim.
4. Add `Middleware []Middleware` field to `Loop`.
5. Add `buildPipeline()` that constructs the default set.
6. Refactor `Run()` to call `pipeline.Run()` instead of inline code.
7. Add `agent.mode: legacy` config gate — if legacy, call old `runLegacy()`
   (the current `Run()` method renamed).
8. Run existing tests. All should pass since behavior is identical.

### Phase 2: Config wiring

1. Add `agent.middleware.enabled` / `agent.middleware.disabled` to config.
2. Resolve middleware names to constructors in `buildPipeline()`.
3. Default set = steer, followup, compaction, approval, loop_detection.
4. Drop `agent.mode` once phase 2 is stable in a release.

### Phase 3: New middleware (feature work)

Each of these is a standalone PR:

1. `PermissionMiddleware` — path-pattern allow/ask/deny. Filters tools
   in `PrepareStep`. (#1 from comparison doc)
2. `PromptCachingMiddleware` — injects Anthropic cache-control breakpoints
   in `PrepareStep`. (#2)
3. `CompactionMiddleware` enhancements: preflight check, token-aware
   threshold, tool-pair summarization. (#3, #4, #9, #10)
4. `SubAgentMiddleware` — extends the `task` tool with roles, depth caps,
   timeouts, parallel batch. (#5)
5. `ToolExecutionMiddleware` — adds concurrency cap via semaphore. (#8)

### Phase 4: Remove legacy path

Once phase 2 has been stable for one release, remove `agent.mode: legacy`
and the old inline `Run()` method.

## Backward compatibility guarantee

During phases 1-3:
- `agent.mode: legacy` (or absence of `agent.mode` key) uses the
  existing inline `Run()` with zero code path changes.
- `agent.mode: middleware` with all 5 default middleware enabled
  produces identical behavior to legacy — same messages, same tool
  results, same error handling.
- Tests cover both paths.

The switch is per-session — a REPL session can start with either mode.
No data migration. No config format change beyond adding the new keys.

## What stays the same

- `Loop` struct fields (Provider, Registry, SystemPrompt, Model, etc.)
  are unchanged. Middleware receives them via the loop reference, not
  through the interface (they're implementation details of each middleware).
- `getAssistantMessage()` (streaming + retry) is unchanged.
- `executeToolsParallel()` is simplified — it no longer does approval
  or loop detection, just execution and result collection.
- `buildToolDefs()` is unchanged.
- Callbacks (`OnToken`, `OnTool`, `OnThinking`, `OnFlush`) are unchanged.
- The `agentSession` in `cmd/yaah/root_cmd.go` is unchanged until Phase 3
  when sub-agent lifecycle is enhanced.

## Files to create

```
internal/agent/
├── middleware.go              # Middleware interface + Step struct
├── pipeline.go               # Pipeline runner
├── middleware_steer.go        # SteerMiddleware
├── middleware_followup.go     # FollowupMiddleware
├── middleware_compaction.go   # CompactionMiddleware
├── middleware_approval.go     # ApprovalMiddleware
├── middleware_loopdetect.go   # LoopDetectionMiddleware
├── middleware_test.go         # Pipeline integration tests
├── agent.go                  # [MODIFIED] Refactored Run(), buildPipeline()
└── agent_test.go             # [MODIFIED] Tests for both legacy + middleware
```

No changes to `cmd/yaah/`, `internal/tools/`, `internal/types/`,
`internal/providers/`, or any other package.
