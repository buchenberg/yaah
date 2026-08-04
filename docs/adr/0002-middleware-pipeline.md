# ADR-002: Middleware Pipeline Pattern

> **Status:** Accepted
> **Date:** 2026-08-03
> **Author:** @buchenberg
> **Related:** ADR-001, ADR-003, ADR-004
> **Implementation:** Incremental across multiple PRs

## Context

As yaah evolved from a simple REPL agent to a sophisticated multi-tool, multi-consumer system, the agent loop accumulated **cross-cutting concerns** that needed to be addressed:

1. **Context Compaction**: When conversation history grows too large, it needs to be summarized to fit within model context windows
2. **Tool Approval**: Some tools require user approval before execution (e.g., file writes, shell commands)
3. **Path Permissions**: Tools should be restricted from accessing certain paths
4. **Loop Detection**: Prevent infinite loops from repeated tool calls
5. **Tool Concurrency**: Limit the number of concurrent tool executions
6. **Prompt Caching**: Add cache-control headers for Anthropic compatibility
7. **Staleness Detection**: Identify and handle stale context information
8. **Steering**: Inject mid-turn corrections or hints to the model
9. **Follow-ups**: Handle results from background sub-agents

### The Problem

Initially, these concerns were **scattered throughout the Loop's Run() method**:

```go
// Before: Monolithic Run() method with embedded concerns
func (l *Loop) Run(ctx context.Context, userInput string) (string, error) {
    // ... setup ...
    
    for iter := 0; iter < maxIterations; iter++ {
        // ⚠️ Context compaction logic embedded here
        if l.EstimatedTokens() > l.Config.ContextWindow {
            l.CompactContext(ctx)
        }
        
        // ⚠️ Loop detection embedded here
        if l.detectLoop() {
            return "", errors.New("loop detected")
        }
        
        // ⚠️ Tool approval embedded here
        if l.Config.ApprovalMode == "ask" && tool.NeedsApproval() {
            if !l.askForApproval() {
                return "", errors.New("denied")
            }
        }
        
        // ⚠️ Path permission check embedded here
        if !l.checkPathPermission(tool.Args) {
            return "", errors.New("permission denied")
        }
        
        // ... more embedded concerns ...
        
        // ⚠️ Tool execution
        result := l.executeTool(ctx, tool)
        
        // ⚠️ Post-tool processing embedded here
        if l.Config.PromptCaching {
            l.addCacheBreakpoint()
        }
    }
}
```

**Problems with this approach:**
- **Violates Single Responsibility Principle**: Run() does everything
- **Hard to test**: Cross-cutting concerns are tangled with core logic
- **Hard to extend**: Adding a new concern requires modifying Run()
- **Hard to disable**: Can't easily turn off a specific concern
- **Poor separation**: Business logic mixed with infrastructure concerns
- **Ordering issues**: Concerns execute in fixed order, can't be reordered

## Decision

Implement a **middleware pipeline pattern** inspired by HTTP middleware (e.g., gin, echo, net/http) and adapted for the agent loop lifecycle.

### Pipeline Architecture

The agent loop is divided into **three extension points** where middleware can intercept and modify behavior:

```
┌─────────────────────────────────────────────────────────────────┐
│                        AGENT LOOP CYCLE                          │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────────┐  │
│  │ PrepareStep  │───▶│  LLM Call   │───▶│   PostModel     │  │
│  │ (pre-flight) │    │ (core)      │    │ (post-LLM)      │  │
│  └─────────────┘    └─────────────┘    └────────┬────────┘  │
│                                                    │            │
│                              ┌─────────────────────▼────────────┐  │
│                              │        PostTool                      │  │
│                              │   (post-tool execution)             │  │
│                              └─────────────────────────────────────┘  │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Middleware Interface

```go
// internal/agent/pipeline/middleware.go
type Middleware interface {
    Name() string
    
    // Called before LLM call - can modify step state
    PrepareStep(ctx context.Context, step *Step) (*Step, error)
    
    // Called after LLM responds, before tool execution
    PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error)
    
    // Called after tool execution
    PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error)
}
```

### Step State

Middleware operates on a mutable `Step` struct that flows through the pipeline:

```go
type Step struct {
    Messages      []types.Message  // Conversation history
    Tools         []types.ToolDef  // Available tools
    Iteration     int              // Current iteration number
    MaxToolTurns  int              // Soft tool cap
    MaxLoopCycles int              // Hard iteration cap
    Model         string           // Current model
    SystemPrompt  string           // Current system prompt
}
```

### Pipeline Construction

```go
// internal/agent/pipeline/pipeline.go
type Pipeline struct {
    middleware []Middleware
}

func NewPipeline(middleware ...Middleware) *Pipeline {
    return &Pipeline{middleware: middleware}
}

func (p *Pipeline) RunPrepareStep(ctx context.Context, step *Step) (*Step, error) {
    var err error
    for _, mw := range p.middleware {
        step, err = mw.PrepareStep(ctx, step)
        if err != nil {
            return step, fmt.Errorf("%s: %w", mw.Name(), err)
        }
    }
    return step, nil
}

// RunPostModel and RunPostTool follow the same pattern
```

### Built-in Middleware

| Name | Hook | Purpose |
|------|------|---------|
| `steer` | PrepareStep | Inject mid-turn steering text |
| `followup` | PrepareStep | Inject follow-up messages from background sub-agents |
| `compaction` | PrepareStep, PostTool | Trigger context compaction when thresholds are exceeded |
| `approval` | PostModel | Prompt user for tool approval when needed |
| `permission` | PostModel | Enforce path-based permission rules |
| `tool_concurrency` | PostModel | Limit concurrent tool execution |
| `loop_detection` | PostTool | Detect and prevent infinite loops |
| `prompt_caching` | PostModel | Add cache-control headers for Anthropic |
| `soft_prune` | PrepareStep, PostTool | Aggressively prune stale tool results |
| `staleness` | PrepareStep | Detect stale context information |
| `sub_agent` | PostTool | Handle sub-agent tool results |

### Configuration

Middleware can be **enabled, disabled, and ordered** via configuration:

```yaml
# config.yaml
agents:
  middleware:
    enabled:
      - compaction
      - approval
      - permission
      - loop_detection
      - tool_concurrency
    disabled:
      - staleness  # Disable staleness detection
      - soft_prune
```

Or programmatically:

```go
pipeline.NewPipeline(
    &pipeline.CompactionMiddleware{...},
    &pipeline.ApprovalMiddleware{...},
    &pipeline.PermissionMiddleware{...},
)
```

### Default Pipeline

```go
// internal/agent/pipeline/config.go
var defaultPipelineNames = []string{
    "steer",
    "followup",
    "compaction",
    "soft_prune",
    "approval",
    "tool_concurrency",
    "loop_detection",
    "staleness",
}
```

## Alternatives Considered

### Alternative 1: Decorator Pattern

Wrap the Loop with decorators that add functionality.

**Rejected because:**
- Harder to compose multiple concerns
- Can lead to "decorator hell" with deep nesting
- Less explicit about execution order
- More complex to test individual concerns

### Alternative 2: Plugin/Extension System

Load middleware from external plugins at runtime.

**Rejected because:**
- Adds complexity for a feature not yet needed
- Security implications of loading arbitrary code
- Build and distribution complexity
- yaah philosophy: minimal core, extensible via composition not plugins

### Alternative 3: Event/Listener System

Emit events at key points and allow listeners to react.

**Rejected because:**
- More suited for notification than interception/modification
- Doesn't provide a clean way to modify step state
- Harder to ensure ordering of listeners
- Can lead to spaghetti code with many listeners

### Alternative 4: Strategy Pattern

Define different strategies for each concern and allow switching between them.

**Rejected because:**
- Doesn't address the composition problem
- Still requires modifications to Loop for new strategies
- Less flexible than pipeline approach

## Consequences

### Positive

1. **Separation of Concerns**: Each middleware handles exactly one concern
2. **Extensibility**: New middleware can be added without modifying core Loop
3. **Testability**: Each middleware can be tested in isolation
4. **Configurability**: Middleware can be enabled/disabled via config
5. **Ordering Control**: Middleware execution order can be controlled
6. **Composability**: Middleware can be combined in any order
7. **Maintainability**: Code is easier to understand and modify
8. **Error Isolation**: Errors in one middleware don't affect others (with proper error handling)

### Negative

1. **Learning Curve**: New contributors need to understand the pipeline pattern
2. **Debugging Complexity**: Tracing through multiple middleware can be harder
3. **Performance Overhead**: Each middleware adds a small overhead (function call, error check)
4. **State Management**: Step struct needs to carry all necessary state

## Migration Notes

### Adding New Middleware

```go
// 1. Define the middleware type
package pipeline

type MyCustomMiddleware struct {
    // Configuration fields
    myConfig string
}

func (m *MyCustomMiddleware) Name() string {
    return "my_custom"
}

func (m *MyCustomMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
    // Modify step as needed
    return step, nil
}

func (m *MyCustomMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
    // No-op for this middleware
    return step, nil
}

func (m *MyCustomMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
    // No-op for this middleware
    return step, nil
}

// 2. Register in builtinBuilders (optional)
var builtinBuilders = map[string]func(PipelineConfig) Middleware{
    "my_custom": func(cfg PipelineConfig) Middleware {
        return &MyCustomMiddleware{myConfig: "default"}
    },
    // ...
}
```

### Custom Pipeline

```go
// Build a custom pipeline for a specific use case
customPipeline := pipeline.NewPipeline(
    &pipeline.CompactionMiddleware{...},
    &pipeline.ApprovalMiddleware{mode: "allow"},
    &MyCustomMiddleware{...},
)

loop := agent.NewLoop(provider, registry,
    agent.WithPipeline([]string{"compaction", "approval", "my_custom"}, nil),
)
```

## Performance Considerations

- **Memory**: Each middleware holds a reference to Step (pointer), so memory overhead is minimal
- **CPU**: Each middleware adds ~2-3 function calls per hook point
- **Ordering**: Middleware order matters for some combinations (e.g., compaction should run before approval)

The pipeline is designed to be **efficient** - middleware that don't need to act on a particular hook simply return the step unchanged.

## References

- [internal/agent/pipeline/](internal/agent/pipeline/) - Pipeline implementation
- [internal/agent/pipeline/pipeline.go](internal/agent/pipeline/pipeline.go) - Core Pipeline type
- [internal/agent/pipeline/middleware.go](internal/agent/pipeline/middleware.go) - Middleware interface
- [internal/agent/agent.go:buildPipeline()](internal/agent/agent.go) - Pipeline construction in Loop
- [internal/agent/pipeline/config.go](internal/agent/pipeline/config.go) - Built-in middleware builders
- [config.yaml reference](docs/configuration.md) - Middleware configuration

## Built-in Middleware Details

| Middleware | PrepareStep | PostModel | PostTool | Config Options |
|------------|-------------|-----------|----------|----------------|
| `steer` | ✅ Inject steering | ❌ | ❌ | `steer` channel |
| `followup` | ✅ Inject follow-ups | ❌ | ❌ | `followups` channel |
| `compaction` | ✅ Compact context | ❌ | ✅ Compact after tools | `ContextWindow`, `CompactionThreshold` |
| `approval` | ❌ | ✅ Check approval | ❌ | `ApprovalMode` |
| `permission` | ❌ | ✅ Check permissions | ❌ | `PermissionRules` |
| `tool_concurrency` | ❌ | ✅ Acquire semaphore | ✅ Release semaphore | `MaxToolConcurrency` |
| `loop_detection` | ❌ | ❌ | ✅ Detect loops | `LoopDetectCount`, `LoopDetectWindow` |
| `prompt_caching` | ❌ | ✅ Add cache headers | ❌ | `PromptCaching` |
| `soft_prune` | ✅ Mark stale | ❌ | ✅ Prune results | `Pruner`, `PruneHooks` |
| `staleness` | ✅ Detect stale | ❌ | ❌ | None |
| `sub_agent` | ❌ | ❌ | ✅ Handle results | None |

---

*This ADR documents an implemented pattern. The middleware pipeline is a core architectural element of yaah.*
