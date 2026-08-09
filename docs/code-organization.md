# Code Organization and File Structure Guidelines

This document outlines the **current state** of code organization in yaah and provides **guidelines for future improvements**, particularly around splitting large files into more maintainable pieces.

---

## Current State (as of August 2026)

yaah has a **well-structured codebase** with clear separation of concerns at the package level. However, some files have grown large and could benefit from splitting.

### Package Structure Overview

```
yaah/
├── cmd/yaah/                    # CLI commands (cobra-based)
│   ├── agent_frame.go           # Agent wiring and tool construction
│   ├── repl_loop.go             # REPL interaction
│   ├── wiring.go                # Dependency injection + session setup
│   └── ... (20+ files)
│
├── internal/
│   ├── acp/                     # ACP JSON-RPC server (wire types, view, dispatch)
│   ├── agent/                   # Core agent loop
│   │   ├── agent.go             # Loop type and turn processing
│   │   ├── options.go           # Functional options for Loop
│   │   ├── events.go            # Typed event system
│   │   ├── view.go              # View interface
│   │   ├── context/             # Pure context helpers (leaf, no agent imports)
│   │   │   ├── tokens.go        #   Token estimation and constants
│   │   │   ├── split.go         #   Turn splitting and preservation budget
│   │   │   ├── prune.go         #   Pre-compaction message pruning
│   │   │   ├── chunked.go       #   Chunked compaction
│   │   │   └── truncation.go    #   Tool-result truncation
│   │   ├── pipeline/            # Middleware pipeline
│   │   │   ├── middleware.go    # Middleware interface
│   │   │   ├── pipeline.go      # Pipeline execution
│   │   │   └── ... (15+ middleware files)
│   │   └── ... (25+ files total)
│   │
│   ├── jobs/                    # Background sub-agent jobs (manager, TaskRunner, I/O contract)
│   ├── memory/                  # SQLite persistence, FTS5, and vector embeddings
│   │
│   ├── tui/                     # Bubble Tea TUI
│   │   └── tui.go               # 1891 lines - Main TUI model and rendering
│   │
│   ├── tools/                   # Built-in tools (30+ tools)
│   │   ├── tools.go            # Tool interface and registry
│   │   ├── read.go              # File reading
│   │   ├── write.go             # File writing
│   │   └── ... (30+ tool files)
│   │
│   └── ... (15+ other packages)
```

### Large Files (> 500 lines)

| File | Lines | Primary Responsibilities |
|------|-------|--------------------------|
| `internal/tui/tui.go` | ~1891 | TUI Model struct, View rendering, Input handling, Event handling |
| `internal/agent/agent.go` | ~770 | Loop struct, Run() method, Turn processing, Compaction |
| `cmd/yaah/agent_frame.go` | ~350 | Loop construction, Tool wiring (session in session.go, wiring in wiring.go) |
| `internal/agent/agent_context.go` | ~200 | `*Loop` methods: `compactContext`, `trimContext`, `ForceCompact`, `EstimatedTokens` (pure helpers extracted to `agent/context/`) |
| `internal/agent/agent_tools.go` | ~250 | Tool execution, Result collection |

---

## File Size Guidelines

yaah follows these **general guidelines** for file organization:

| File Size | Status | Action |
|-----------|--------|--------|
| < 300 lines | ✅ Ideal | No action needed |
| 300-500 lines | ⚠️ Acceptable | Consider splitting if logical |
| 500-800 lines | ❌ Large | Should be split |
| > 800 lines | 🔴 Too Large | Must be split |

**Exceptions:**
- Test files can be larger (up to ~1000 lines)
- Generated code is exempt
- Files with many similar functions (e.g., middleware implementations) may be larger

---

## Recommended Splitting Strategy

### Principles for Splitting Files

1. **Group by Responsibility** - Not by size alone
2. **Minimize Cross-File Dependencies** - Avoid circular imports
3. **Keep Related Code Together** - Types and their methods in same file
4. **Preserve Readability** - Split only if it improves understanding
5. **Maintain Testability** - Ensure tests still work after splitting

### File Naming Conventions

- **`types.go`** - Type definitions (structs, interfaces, enums)
- **`<name>.go`** - Main implementation for a type or feature
- **`<name>_test.go`** - Tests for that type/feature
- **`<name>_<subfeature>.go`** - Sub-features (e.g., `agent_compact.go`)
- **`util.go`** or `helpers.go` - Utility functions (use sparingly)

---

## Proposed File Splits

### 1. `internal/tui/tui.go` → Split into 4-5 files

**Current:** 1891 lines with Model struct, View rendering, Input handling, Event handling, Utility functions

**Proposed Structure:**

```
internal/tui/
├── model.go              # Model struct definition (~200 lines)
│                           # - Type definition
│                           # - Config struct
│                           # - New() constructor
│                           # - Basic getters/setters
│
├── view.go               # View rendering (~500 lines)
│                           # - View() method (main render)
│                           # - Helper rendering functions
│                           # - Layout calculations
│
├── messages.go           # Message handling (~400 lines)
│                           # - AddMessage()
│                           # - AddAssistantMessage()
│                           # - AddToolResult()
│                           # - Message formatting
│
├── input.go              # Input handling (~300 lines)
│                           # - handleKeyPress()
│                           # - handleMouseClick()
│                           # - Command mode handling
│                           # - Search mode handling
│
├── events.go             # Agent event handling (~400 lines)
│                           # - HandleEvent() method
│                           # - Event-specific handlers
│                           # - Control message handling
│
├── utils.go              # Utility functions (~200 lines)
│                           # - osc8Link()
│                           # - injectHyperlinks()
│                           # - splitRow()
│                           # - isWideRune()
│                           # - Tree rendering helpers
│
└── theme.go              # Already separate - styling
```

**Dependencies:** All files in same package, no circular dependencies

**Migration Steps:**
1. Create new files with appropriate code sections
2. Update imports in each file (none needed - same package)
3. Run `go build ./internal/tui/` to verify
4. Run tests: `go test ./internal/tui/...`
5. Commit each split as separate PR for easier review

### 2. `internal/agent/agent.go` → Split into 5-6 files

**Current:** 770 lines with Loop struct, Run() method, Turn processing, Compaction

**Proposed Structure:**

```
internal/agent/
├── types.go              # Type definitions (~150 lines)
│                           # - Provider type aliases
│                           # - ToolInfo struct
│                           # - SubAgentInfo struct
│                           # - ToolsLevel enum
│                           # - LoopConfig struct
│                           # - LoopState struct
│
├── loop.go               # Main Loop type and core methods (~300 lines)
│                           # - Loop struct definition
│                           # - NewLoop() constructor (already in options.go)
│                           # - Run() method
│                           # - runMiddleware() method
│                           # - buildPipeline()
│                           # - toPipelineConfig()
│
├── turn.go               # Turn processing (~250 lines)
│                           # - buildTurnRequest()
│                           # - guardContextBeforeCall()
│                           # - executeToolPhase()
│                           # - injectWrapUpNotice()
│
├── lifecycle.go          # Lifecycle methods (~150 lines)
│                           # - initMessages()
│                           # - publishDone()
│                           # - teardown()
│                           # - ctxMgr()
│                           # - applyDefaults()
│
├── compact.go            # Context compaction (~100 lines)
│                           # - Compact() method
│                           # - llmCompact()
│                           # - llmTrim()
│
└── tools.go              # Tool-related methods (~100 lines)
                                # - buildToolsForLevel()
                                # - agentTools()
                                # - addUsage()
```

**Note:** The `options.go` file already contains the functional options pattern for Loop construction, which is well-separated.

**Dependencies:** All files in same package, Loop struct is central

**Migration Steps:**
1. Move type definitions to `types.go` first
2. Move turn processing methods to `turn.go`
3. Move lifecycle methods to `lifecycle.go`
4. Move compaction methods to `compact.go`
5. Keep core Loop and Run() in `loop.go`
6. Run `go build ./internal/agent/` and tests after each step

### 3. `cmd/yaah/agent_frame.go` → Split into 3-4 files

**Current:** 990 lines with Session interface, agentSession struct, Session management, Loop construction, Tool wiring

**Proposed Structure:**

```
cmd/yaah/
├── session.go            # Session interface and core management (~400 lines)
│                           # - Session interface
│                           # - agentSession struct
│                           # - newAgentSession()
│                           # - Session methods (Close, Compact, Steer, FollowUp, etc.)
│                           # - SetView, SetCtrlCh, SetApproveFn, SetModel
│
├── wiring.go             # Dependency wiring (~300 lines)
│                           # - Provider resolution
│                           # - Model resolution
│                           # - Tool registry setup
│                           # - MCP client setup
│                           # - Sub-agent role loading
│                           # - Prompt assembly
│
├── loop_builder.go       # Loop construction (~200 lines)
│                           # - runPrompt() method
│                           # - Loop option assembly
│                           # - AgentConfig construction
│
└── tools.go              # Tool wiring (~100 lines)
                                # - Task tool creation
                                # - ListSubAgents tool
                                # - Tool quick reference building
```

**Dependencies:** Some cross-file dependencies, but all in same package

**Migration Steps:**
1. Move Session interface and agentSession struct to `session.go`
2. Move wiring-related code (provider resolution, tool setup) to `wiring.go`
3. Move Loop construction logic to `loop_builder.go`
4. Run `go build ./cmd/yaah/` and tests after each step

---

## Code Organization Best Practices

### 1. Package-Level Organization

- **Single Responsibility**: Each package should have one clear purpose
- **Minimal Dependencies**: Packages should depend on as few other packages as possible
- **No Circular Dependencies**: Use interfaces to break dependency cycles
- **Internal for Implementation**: Use `internal/` for implementation details not meant for external use

### 2. File-Level Organization

- **One File, One Concern**: Each file should have a single primary responsibility
- **Group Related Types**: Keep a type and its methods together
- **Co-locate Tests**: Keep test files next to the code they test
- **Alphabetical Order**: Not required, but helps with navigation

### 3. Function-Level Organization

- **Group by Feature**: Related functions should be near each other
- **Public First**: Export public functions before private ones
- **Logical Order**: Functions should flow logically (setup → main → teardown)

### 4. Type Organization

- **Type Definition First**: Define the type before its methods
- **Constructor Near Type**: Keep NewXXX() near the type definition
- **Methods Grouped**: Keep all methods for a type together
- **Receiver Consistency**: Use consistent receiver names (e.g., `l` for Loop, `m` for Model)

---

## Existing Good Examples

The yaah codebase already has **excellent examples** of well-organized code:

### 1. `internal/agent/pipeline/` - Middleware Pattern

```
internal/agent/pipeline/
├── middleware.go        # Middleware interface
├── pipeline.go          # Pipeline execution
├── config.go            # Pipeline configuration
├── compaction.go        # Compaction middleware
├── approval.go          # Approval middleware
├── permission.go        # Permission middleware
├── loopdetect.go        # Loop detection middleware
└── ... (15+ files)
```

**Why it works:**
- Each middleware is in its own file
- Clear interface definition
- Easy to add new middleware
- Easy to test individual middleware

### 2. `internal/tools/` - Tool Registry Pattern

```
internal/tools/
├── tools.go             # Tool interface and registry
├── read.go               # Read tool
├── write.go              # Write tool
├── edit.go               # Edit tool
├── bash.go               # Bash tool
├── powershell.go         # PowerShell tool
└── ... (30+ tool files)
```

**Why it works:**
- Each tool is self-contained
- Easy to add new tools
- Tools don't depend on each other
- Clear separation of concerns

### 3. `internal/providers/` - Provider Pattern

```
internal/providers/
├── providers.go         # OpenAI-compatible client
├── anthropic.go         # Anthropic-specific client
├── stream.go            # Streaming support
├── modelinfo.go         # Model metadata
└── wire.go              # Provider factory/wiring
```

**Why it works:**
- Each provider has its own file
- Common functionality in shared files
- Easy to add new providers

---

## Migration Strategy

### For Existing Large Files

1. **Start Small**: Split one file at a time
2. **Test Incrementally**: Run tests after each change
3. **Use Git**: Commit each split as a separate commit
4. **Review Carefully**: Large file splits can introduce bugs

### For New Code

1. **Follow the Pattern**: Use the organization patterns from existing code
2. **Start Small**: Create new files for new features rather than adding to existing large files
3. **Group Logically**: Put related code together
4. **Review File Size**: Check file size before committing

---

## File Size Monitoring

Add this to your pre-commit checklist:

```bash
# Check for files > 500 lines
find internal -name "*.go" -not -name "*_test.go" -exec wc -l {} + | awk '$1 > 500'

# Check for files > 800 lines (must be split)
find internal -name "*.go" -not -name "*_test.go" -exec wc -l {} + | awk '$1 > 800'
```

Or use this PowerShell command:

```powershell
Get-ChildItem -Recurse -Filter "*.go" -Exclude "*_test.go" | 
    Where-Object { (Get-Content $_.FullName | Measure-Object -Line).Lines -gt 500 } | 
    Select-Object FullName, @{Name="Lines";Expression={(Get-Content $_.FullName | Measure-Object -Line).Lines}}
```

---

## Tools for Code Organization

### 1. `go tool compile -e`

Check for export issues after splitting:

```bash
go build ./...
```

### 2. `go vet`

Check for potential issues:

```bash
go vet ./...
```

### 3. `staticcheck`

More thorough static analysis:

```bash
staticcheck ./...
```

### 4. `golangci-lint`

Comprehensive linting:

```bash
golangci-lint run ./...
```

---

## Future Work

### High Priority (Should Do)

1. ✅ **Documentation** - ADRs and code organization guidelines (DONE)
2. ✅ **Extract `internal/agent/context/`** — Pure helpers moved to a leaf package (DONE, Phase 2A)
3. ⏳ **Split `internal/tui/tui.go`** - Largest file, most in need of splitting
4. ⏳ **Split `internal/agent/agent.go`** - Core file, but well-structured
5. ⏳ **Split `cmd/yaah/agent_frame.go`** - Already partially split (session in session.go, wiring in wiring.go)

### Medium Priority (Nice to Have)

6. **Review `internal/agent/pipeline/config.go`** - Could be split
7. **Review test files** - Some test files are large (> 500 lines)

### Low Priority (Optional)

8. **Create file splitting script** - Automate file splitting
9. **Add file size to CI** - Fail builds if files exceed limits
10. **Add code organization to onboarding** - Document in CONTRIBUTING.md

---

## References

- [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments) - Official Go style guide
- [Effective Go](https://go.dev/doc/effective_go) - Go best practices
- [Go Project Layout](https://github.com/golang-standards/project-layout) - Standard project layout
- [Architecture Overview](../architecture.md) - Architecture background
- [CONTRIBUTING.md](../CONTRIBUTING.md) - Contribution guidelines

---

*This document provides guidance for maintaining and improving code organization in yaah. Follow these guidelines when adding new code or refactoring existing code.*
