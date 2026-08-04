# ADR-003: Functional Options Pattern for Loop Configuration

> **Status:** Accepted
> **Date:** 2026-08-03
> **Author:** @buchenberg
> **Related:** ADR-001, ADR-002
> **Implementation:** PR #XX (functional options introduction)

## Context

The `Loop` struct in `internal/agent/agent.go` is the central orchestrator of the yaah agent. As features were added, the Loop's configuration grew to **30+ fields** spanning:

- Provider and model settings
- Context window and compaction parameters
- Tool execution limits and concurrency
- Approval and permission modes
- Observability and tracing toggles
- Middleware pipeline configuration
- Session and persistence settings
- Sub-agent concurrency limits

### The Problem

Before the functional options pattern, Loop instances were constructed using **direct struct literals**:

```go
// Before: Struct literal construction (problematic)
loop := &agent.Loop{
    Provider: provider,
    Registry: registry,
    Config: agent.LoopConfig{
        Model:                  modelName,
        SystemPrompt:          systemPrompt,
        ContextWindow:        contextWindow,
        CompactionThreshold:  compactionThreshold,
        RawCompactionThreshold: rawCompactionThreshold,
        CompactMaxMessages:   compactMaxMessages,
        MaxLoopCycles:        maxLoopCycles,
        MaxToolTurns:         maxToolTurns,
        MaxRetries:           maxRetries,
        RetryBackoff:         retryBackoff,
        ApprovalMode:         approvalMode,
        PermissionRules:      permissionRules,
        LoopDetectCount:      loopDetectCount,
        LoopDetectWindow:     loopDetectWindow,
        MaxToolConcurrency:   maxToolConcurrency,
        MaxSubAgentConcurrency: maxSubAgentConcurrency,
        StuckChildTimeout:    stuckChildTimeout,
        // ... 15+ more fields
    },
    State: agent.LoopState{
        Messages:   messages,
        // ... more fields
    },
    View:                view,
    Persister:          persister,
    Hooks:              hooks,
    FallbackProvider:   fallbackProvider,
    FallbackModel:      fallbackModel,
    CompactProvider:    compactProvider,
    CompactModel:       compactModel,
    Steer:              steerCh,
    FollowUps:          followupCh,
    ApproveFn:          approveFn,
    ConflictTracker:    tracker,
    CtxMgr:             ctxMgr,
}
```

**Problems with struct literals:**

1. **Verbosity**: 30+ fields to set, many with default values
2. **Duplication**: Same configuration code duplicated across multiple call sites (agent_frame.go, serve.go, tui.go, subagent_runner.go)
3. **Order Sensitivity**: Field order matters for readability but not semantics
4. **Hard to Extend**: Adding a new field requires updating all call sites
5. **Hard to Document**: No natural place for documentation of each option
6. **Error-Prone**: Easy to miss a field or set it incorrectly
7. **Testing Difficulty**: Hard to create Loop instances with specific configurations for tests

### Call Site Duplication

The same Loop construction pattern appeared in **4 different files**:

```text
cmd/yaah/agent_frame.go:runPrompt()      - Main session loop
cmd/yaah/serve.go:handleRequest()       - MCP server requests
cmd/yaah/tui.go:runTUI()               - TUI agent loop
cmd/yaah/subagent_runner.go:run()       - Sub-agent loops
```

Each with slightly different but largely overlapping configuration.

## Decision

Adopt the **Functional Options Pattern** (also known as the Options Pattern) popularized in Go by Rob Pike and others.

### Pattern Overview

```go
// 1. Define Option type
type Option func(*Loop)

// 2. Define constructor with variadic options
func NewLoop(provider Provider, registry *tools.Registry, opts ...Option) *Loop {
    l := &Loop{
        Provider: provider,
        Registry: registry,
        // Defaults set here
    }
    for _, opt := range opts {
        opt(l)
    }
    return l
}

// 3. Define option functions
func WithModel(model string) Option {
    return func(l *Loop) { l.Config.Model = model }
}

func WithSystemPrompt(prompt string) Option {
    return func(l *Loop) { l.Config.SystemPrompt = prompt }
}

// 4. Usage
loop := agent.NewLoop(provider, registry,
    agent.WithModel("deepseek-v4-pro"),
    agent.WithSystemPrompt(prompt),
    agent.WithView(view),
    agent.WithMessages(messages),
    agent.WithSessionID(sessionID),
)
```

### Implementation Details

#### Option Type and Constructor

```go
// internal/agent/options.go
package agent

import (
    "time"
    "github.com/buchenberg/yaah/internal/agent/pipeline"
    "github.com/buchenberg/yaah/internal/tools"
    "github.com/buchenberg/yaah/internal/types"
)

// Option configures a Loop via the functional options pattern.
type Option func(*Loop)

// NewLoop creates a Loop with the required provider and registry,
// applying any optional configuration. This replaces the 30+ field
// struct literal previously duplicated across agent_frame.go, serve.go,
// tui.go, and subagent_runner.go.
func NewLoop(provider Provider, registry *tools.Registry, opts ...Option) *Loop {
    l := &Loop{
        Provider: provider,
        Registry: registry,
    }
    for _, opt := range opts {
        opt(l)
    }
    return l
}
```

#### Option Functions

```go
// Core options
func WithModel(model string) Option {
    return func(l *Loop) { l.Config.Model = model }
}

func WithSystemPrompt(prompt string) Option {
    return func(l *Loop) { l.Config.SystemPrompt = prompt }
}

func WithView(v View) Option {
    return func(l *Loop) { l.View = v }
}

func WithMessages(msgs []types.Message) Option {
    return func(l *Loop) { l.State.Messages = msgs }
}

func WithSessionID(id string) Option {
    return func(l *Loop) { l.Config.SessionID = id }
}

// Provider options
func WithFallback(provider Provider, model string) Option {
    return func(l *Loop) {
        l.FallbackProvider = provider
        l.FallbackModel = model
    }
}

func WithCompactProvider(provider Provider, model string) Option {
    return func(l *Loop) {
        l.CompactProvider = provider
        l.Config.CompactModel = model
    }
}

// Middleware options
func WithPipeline(enabled, disabled []string) Option {
    return func(l *Loop) {
        l.Config.PipelineNames = enabled
        l.Config.PipelineDisabled = disabled
    }
}

// Concurrency options
func WithSubAgentConcurrency(max int, stuckTimeout time.Duration, stuckTimeouts map[string]time.Duration) Option {
    return func(l *Loop) {
        l.Config.MaxSubAgentConcurrency = max
        l.Config.StuckChildTimeout = stuckTimeout
        l.Config.StuckChildTimeouts = stuckTimeouts
    }
}

// Grouped configuration
func WithAgentConfig(cfg AgentConfig) Option {
    return func(l *Loop) {
        l.Config.MaxLoopCycles = cfg.MaxLoopCycles
        l.Config.MaxToolTurns = cfg.MaxToolTurns
        // ... map all AgentConfig fields
    }
}
```

#### AgentConfig for Grouped Options

```go
// AgentConfig holds the full set of tuning parameters typically derived
// from config.yaml. Use WithAgentConfig to apply them all at once.
type AgentConfig struct {
    MaxLoopCycles          int
    MaxToolTurns           int
    MaxRetries             int
    RetryBackoffSecs       int
    ContextWindow          int
    CompactionThreshold    float64
    RawCompactionThreshold float64
    CompactMaxMessages     int
    EstimateFactor         float64
    QualityGates           map[string][]string
    LoopDetectCount        int
    LoopDetectWindow       int
    MaxToolConcurrency     int
    WrapUpThreshold        int
    MaxInlineToolsPerTurn  int
    PromptCaching          bool
    ReasoningProtectTurns  int
    ToolResultMaxLines     int
    ToolResultMaxBytes     int
    PruneProtectTokens     int
    PruneMinReclaim        int
    PruneMinTurns          int
    JSONMode               bool
}

func WithAgentConfig(cfg AgentConfig) Option {
    return func(l *Loop) {
        l.Config.MaxLoopCycles = cfg.MaxLoopCycles
        l.Config.MaxToolTurns = cfg.MaxToolTurns
        if cfg.RetryBackoffSecs > 0 {
            l.Config.RetryBackoff = time.Duration(cfg.RetryBackoffSecs) * time.Second
        }
        // ... apply all fields
    }
}
```

### Migration Example

#### Before

```go
// cmd/yaah/agent_frame.go (old)
loop := &agent.Loop{
    Provider:         prov,
    Registry:        s.toolReg,
    Config: agent.LoopConfig{
        Model:            mName,
        SystemPrompt:    s.mainPrompt,
        MaxLoopCycles:   s.cfg.Agent.Default.MaxLoopCycles,
        MaxToolTurns:    s.cfg.Agent.Default.MaxToolTurns,
        // ... 20+ more fields
    },
    State: agent.LoopState{
        Messages: s.messages,
    },
    View:              v,
    Persister:        persister,
    Hooks:            hooks,
    FallbackProvider: fallbackProvider,
    // ... 10+ more fields
}
```

#### After

```go
// cmd/yaah/agent_frame.go (current)
loop := agent.NewLoop(prov, s.toolReg,
    agent.WithModel(mName),
    agent.WithSystemPrompt(s.mainPrompt),
    agent.WithView(v),
    agent.WithMessages(s.messages),
    agent.WithSessionID(s.sessionID),
    agent.WithPersister(persister),
    agent.WithHooks(hooks),
    agent.WithFallback(fallbackProvider, fallbackModel),
    agent.WithCompactProvider(compactProvider, compactModel),
    agent.WithPipeline(s.cfg.Agent.Middleware.Enabled, s.cfg.Agent.Middleware.Disabled),
    agent.WithSteer(s.steerCh),
    agent.WithFollowUps(s.followupCh),
    agent.WithConflictTracker(s.tracker),
    agent.WithToolsLevel(agent.FullTools),
    agent.WithOtel(s.cfg.Observability.Otel.Enabled, s.cfg.Observability.Otel.Verbose),
    agent.WithSubAgentConcurrency(
        s.cfg.Agent.SubAgent.MaxConcurrency,
        time.Duration(s.cfg.Agent.SubAgent.StuckChildTimeout)*time.Second,
        buildStuckChildTimeouts(s.cfg.Agent.SubAgent),
    ),
    agent.WithAgentConfig(agent.AgentConfig{
        MaxLoopCycles:          s.cfg.Agent.Default.MaxLoopCycles,
        MaxToolTurns:           s.cfg.Agent.Default.MaxToolTurns,
        // ...
    }),
    agent.WithApprovalMode(resolveApproval(s.cfg)),
)
```

## Alternatives Considered

### Alternative 1: Builder Pattern

```go
loop := agent.NewLoopBuilder(provider, registry)
    .WithModel(model)
    .WithSystemPrompt(prompt)
    .WithView(view)
    .Build()
```

**Rejected because:**
- More boilerplate (need to define builder struct and all methods)
- Less idiomatic in Go
- Builder object needs to be kept in sync with Loop struct
- Less flexible for one-off customizations

### Alternative 2: Config Struct + Merge

```go
config := agent.LoopConfig{
    Model: model,
    // ...
}
loop := agent.NewLoopWithConfig(provider, registry, config)
```

**Rejected because:**
- Still requires struct literal for config
- Doesn't solve duplication across call sites
- Less extensible
- Harder to document individual options

### Alternative 3: Map-Based Configuration

```go
loop := agent.NewLoop(provider, registry, map[string]interface{}{
    "model": model,
    "system_prompt": prompt,
    // ...
})
```

**Rejected because:**
- No type safety
- No compile-time checking
- Hard to document
- Easy to make typos
- Performance overhead of reflection

### Alternative 4: Method Chaining on Loop

```go
loop := agent.NewLoop(provider, registry)
    .WithModel(model)
    .WithSystemPrompt(prompt)
    .WithView(view)
```

**Rejected because:**
- Modifies the Loop after construction (mutable)
- Less idiomatic in Go
- Can lead to nil pointer issues if NewLoop returns nil
- Methods would need to return *Loop for chaining

## Consequences

### Positive

1. **Reduced Duplication**: Configuration code is no longer duplicated across call sites
2. **Type Safety**: All options are type-checked at compile time
3. **Self-Documenting**: Option function names clearly indicate what they configure
4. **Extensibility**: New options can be added without breaking existing code
5. **Flexibility**: Options can be composed in any order
6. **Testability**: Easy to create Loop instances with specific configurations for tests
7. **Documentation**: Each option function has its own godoc comment
8. **Grouping**: Related options can be grouped into config structs (e.g., AgentConfig)
9. **Defaults**: Defaults are set once in the constructor, not at each call site

### Negative

1. **Boilerplate**: Each option requires a function definition
2. **Indirection**: Slightly harder to trace what's being configured
3. **Learning Curve**: New contributors need to understand the pattern
4. **Error Messages**: Errors in options are harder to debug (wrong option passed, etc.)

## Performance Considerations

- **Memory**: Each option is a function closure, but these are small (typically just capturing a value)
- **CPU**: Each option adds one function call overhead
- **Allocation**: No additional allocations beyond the Loop struct itself

The pattern is **highly efficient** - the overhead is negligible compared to the benefits.

## Best Practices

### 1. Always Set Required Fields in Constructor

```go
// Good: Required fields set in NewLoop
func NewLoop(provider Provider, registry *tools.Registry, opts ...Option) *Loop {
    l := &Loop{
        Provider: provider,  // Required
        Registry: registry, // Required
        // Defaults for optional fields
        Config: LoopConfig{
            MaxLoopCycles: 50,  // Default
            // ...
        },
    }
    // ... apply options
}
```

### 2. Group Related Options

```go
// Good: Related options grouped
type AgentConfig struct {
    MaxLoopCycles     int
    MaxToolTurns      int
    CompactionThreshold float64
    // ... all agent tuning parameters
}

func WithAgentConfig(cfg AgentConfig) Option
```

### 3. Document Each Option

```go
// WithModel sets the model name for the LLM.
// This overrides any model set in the provider configuration.
func WithModel(model string) Option {
    return func(l *Loop) { l.Config.Model = model }
}
```

### 4. Use Sensible Defaults

```go
// In NewLoop constructor
l := &Loop{
    // ...
    Config: LoopConfig{
        MaxLoopCycles:    50,     // Sensible default
        MaxToolTurns:     25,     // Sensible default
        ApprovalMode:     "ask",  // Sensible default
        // ...
    },
}
```

## References

- [Functional Options Pattern - Dave Cheney](https://dave.cheney.net/2014/10/17/functional-options-for-friendly-apis)
- [Self-referential functions and the design of options - Rob Pike](https://commandcenter.blogspot.com/2014/01/self-referential-functions-and-design.html)
- [internal/agent/options.go](internal/agent/options.go) - Implementation
- [internal/agent/agent.go:NewLoop()](internal/agent/agent.go) - Constructor
- [cmd/yaah/agent_frame.go:runPrompt()](cmd/yaah/agent_frame.go) - Usage example

## Comparison with Other Patterns

| Pattern | Type Safety | Extensibility | Readability | Boilerplate | Go Idiomatic |
|---------|-------------|---------------|-------------|------------|--------------|
| Functional Options | ✅ Yes | ✅ High | ✅ High | ⚠️ Medium | ✅ Yes |
| Builder | ✅ Yes | ✅ High | ✅ High | ❌ High | ❌ No |
| Struct Literal | ✅ Yes | ❌ Low | ⚠️ Medium | ✅ Low | ✅ Yes |
| Map-based | ❌ No | ✅ High | ❌ Low | ✅ Low | ❌ No |
| Method Chaining | ✅ Yes | ✅ High | ✅ High | ⚠️ Medium | ❌ No |

---

*This ADR documents an implemented pattern. The functional options pattern is used throughout yaah for Loop configuration.*
