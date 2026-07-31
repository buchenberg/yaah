// Package tools implements the built-in tools yaah exposes to the
// model. Each tool implements the Tool interface so the agent loop
// can dispatch uniformly whether a tool is built-in or comes from an
// MCP server.
//
// # Adding a new tool
//
// A tool is a struct that implements the four-method Tool interface.
// Tools that need runtime wiring (database handles, process managers)
// are registered at startup in agent_frame.go; tools with no
// dependencies are registered here via the leafTools map.
//
// The canonical form for a simple leaf tool:
//
//	type MyTool struct{}
//
//	func (t *MyTool) Name() string        { return "mytool" }
//	func (t *MyTool) Description() string { return "Does something useful." }
//	func (t *MyTool) Schema() json.RawMessage {
//	    return json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}`)
//	}
//	func (t *MyTool) Execute(ctx context.Context, args string) (string, error) {
//	    var params struct{ X string `json:"x"` }
//	    if err := json.Unmarshal([]byte(args), &params); err != nil {
//	        return "", fmt.Errorf("mytool: invalid arguments: %w", err)
//	    }
//	    // ... do work ...
//	    return result, nil
//	}
//
// To make a tool require user approval, implement DangerClassifier:
//
//	func (t *MyTool) IsDangerous(argsJSON string) bool { return true }
//
// For argument-level classification (e.g. read vs write actions):
//
//	func (t *MyTool) IsDangerous(argsJSON string) bool {
//	    var params struct{ Action string `json:"action"` }
//	    json.Unmarshal([]byte(argsJSON), &params)
//	    return params.Action == "write"
//	}
//
// Then add the tool to the leafTools map (for zero-dependency tools)
// or register it at runtime in agent_frame.go (for tools needing wiring).
//
// For a tool that needs runtime wiring (e.g. a database handle):
//
//	type MyWiredTool struct{ DB *sql.DB }
//
//	func (t *MyWiredTool) Name() string        { return "mywired" }
//	func (t *MyWiredTool) Description() string  { return "Does something with the database." }
//	func (t *MyWiredTool) Schema() json.RawMessage { ... }
//	func (t *MyWiredTool) Execute(ctx context.Context, args string) (string, error) { ... }
//
// Then in agent_frame.go, after creating the database:
//
//	toolReg.Register(&MyWiredTool{DB: db})
//
// No leafTools entry is needed — the runtime-wired instance replaces
// the default one. Existing examples: MemorySearchTool, BackgroundProcessTool,
// TodoWriteTool, SkillTool, TaskTool.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
)

// Tool is the interface that all tools (built-in and MCP) must satisfy.
type Tool interface {
	// Name returns the tool name as it appears in the function call.
	Name() string

	// Description returns a short description of what the tool does.
	Description() string

	// Schema returns the JSON Schema for the tool's parameters.
	Schema() json.RawMessage

	// Execute runs the tool with the given JSON-encoded arguments.
	// The context is used for cancellation and timeouts.
	// Returns the result as a string (or an error if something went wrong).
	Execute(ctx context.Context, args string) (string, error)
}

// DangerClassifier is an optional interface that tools implement to provide
// argument-level danger classification. When a tool implements this,
// IsDangerous(argsJSON) is called to decide whether the specific action
// requires user approval. Tools that are never dangerous simply don't
// implement this interface.
type DangerClassifier interface {
	IsDangerous(argsJSON string) bool
}

// --- Tool Registry ---

// Registry holds all available tools and dispatches by name.
type Registry struct {
	tools map[string]Tool
	// generation increments on every Register so consumers (e.g. the agent
	// loop's tool-definition cache) can detect mutations cheaply without
	// re-reading the full tool set each turn.
	generation int
}

// leafTools is the single source of truth for the names and
// constructors of built-in leaf tools (those that need no runtime
// wiring). NewRegistry installs all of them; NewLeafTool hands out
// individual instances by name so sub-agent role profiles build
// curated registries from the same map and cannot silently diverge.
var leafTools = map[string]func() Tool{
	"read":        func() Tool { return &ReadTool{} },
	"write":       func() Tool { return &WriteTool{} },
	"edit":        func() Tool { return &EditTool{} },
	"patch":       func() Tool { return &PatchTool{} },
	"sed":         func() Tool { return &SedTool{} },
	"delete":      func() Tool { return &DeleteTool{} },
	"replace":     func() Tool { return &ReplaceTool{} },
	"json_query":  func() Tool { return &JSONQueryTool{} },
	"grep":        func() Tool { return &GrepTool{} },
	"glob":        func() Tool { return &GlobTool{} },
	"ls":          func() Tool { return &LsTool{} },
	"bash":        func() Tool { return &BashTool{} },
	"powershell":  func() Tool { return &PowerShellTool{} },
	"question":    func() Tool { return &QuestionTool{} },
	"webfetch":    func() Tool { return &WebFetchTool{} },
	"git":         func() Tool { return &GitTool{} },
	"http":        func() Tool { return &HTTPTool{} },
	"go_outline":  func() Tool { return &GoOutlineTool{} },
	"go_refactor": func() Tool { return &GoRefactorTool{} },
	"calculate":   func() Tool { return &CalculateTool{} },
	"file_info":   func() Tool { return &FileInfoTool{} },
	"go_test":     func() Tool { return NewGoTestTool() },
	"diff":        func() Tool { return NewDiffTool() },
	"staticcheck": func() Tool { return NewStaticcheckTool() },
	"go_mod":      func() Tool { return NewGoModTool() },
	"bisect":      func() Tool { return NewBisectTool() },
}

// NewRegistry creates a tool registry with yaah's built-in tools.
// Only the platform-appropriate shell tool is registered: powershell
// on Windows, bash elsewhere. Both implementations remain available
// via NewLeafTool for explicit registration.
func NewRegistry() *Registry {
	r := NewEmptyRegistry()
	for name, factory := range leafTools {
		if name == "bash" && runtime.GOOS == "windows" {
			continue
		}
		if name == "powershell" && runtime.GOOS != "windows" {
			continue
		}
		r.Register(factory())
	}
	return r
}

// NewEmptyRegistry returns a Registry with no tools pre-registered, so
// callers that want a curated tool set (e.g. sub-agent role profiles)
// can build it up via Register without inheriting every built-in tool.
func NewEmptyRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// NewLeafTool returns a fresh instance of the named built-in leaf tool,
// or nil if the name is unknown. It is backed by the same map as
// NewRegistry, so the two cannot drift.
func NewLeafTool(name string) Tool {
	if factory, ok := leafTools[name]; ok {
		return factory()
	}
	return nil
}

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	r.tools[t.Name()] = t
	r.generation++
}

// Generation returns a monotonically increasing counter that changes whenever
// the registry is mutated via Register. Consumers cache derived data (e.g.
// OpenAI tool definitions) keyed on this value to avoid rebuilding it every
// turn when the tool set is unchanged.
func (r *Registry) Generation() int {
	return r.generation
}

// Get returns the tool with the given name, or nil if not found.
func (r *Registry) Get(name string) Tool {
	return r.tools[name]
}

// Execute dispatches a tool call by name and returns the result.
func (r *Registry) Execute(ctx context.Context, name, args string) (string, error) {
	t := r.Get(name)
	if t == nil {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	return t.Execute(ctx, args)
}

// List returns all registered tool names.
func (r *Registry) List() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}
