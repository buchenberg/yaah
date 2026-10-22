// session_options.go carries every CLI-flag contribution to agent
// construction as explicit data. The flag package variables in root.go
// are parse targets ONLY — sessionOptionsFromFlags is the single place
// they are read, so agent construction never depends on hidden package
// state (finding B1).
package yaah

import (
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/buchenberg/yaah/internal/mcp"
)

// SessionOptions carries everything the CLI flags (and serve mode)
// contribute to agent session construction.
type SessionOptions struct {
	// ApprovalOverride is --approval: "allow", "ask", or "deny"; empty
	// defers to env/config resolution.
	ApprovalOverride string

	// ResumeSessionID is --resume: restore a prior session by ID.
	ResumeSessionID string

	// Cwd is the session's working directory. Empty defers to the
	// process working directory. ACP sets it per session so several
	// sessions with different cwds can share one process without
	// chdir.
	Cwd string

	// MCPManifests are additional MCP servers supplied by the client
	// (ACP session/new or session/load). They are merged with the
	// config-declared servers for this session only.
	MCPManifests map[string]mcp.Manifest

	// DirectiveOverrides are --directive flags (repeatable), prepended
	// to config directives.
	DirectiveOverrides []string

	// WorkspaceRoot is --workspace: restrict file-accessing tools to
	// this directory; empty disables containment.
	WorkspaceRoot string

	// AllowHomeAccess is --allow-home: permit ~ expansion inside the
	// workspace validator.
	AllowHomeAccess bool

	// WorkspaceAsk is --workspace-ask: prompt instead of hard-denying
	// out-of-workspace access.
	WorkspaceAsk bool

	// OtelProcessors are extra span processors injected by serve mode
	// (the in-memory BufferingSpanProcessor). Empty for normal runs.
	OtelProcessors []sdktrace.SpanProcessor

	// OtelInMemoryOnly keeps tracing local (no OTLP endpoint, no
	// metrics) — set by serve mode.
	OtelInMemoryOnly bool

	// OneShot marks a non-interactive one-shot run (`yaah "prompt"`).
	// The session's prompt gains a non-interactivity notice and the
	// question tool is left out of the belt: there is no user to ask,
	// and a blocking question dead-ends the run (found in the
	// Terminal-Bench mini-suite: an agent that asks "which would you
	// like?" simply burns its whole budget).
	OneShot bool
}

// sessionOptionsFromFlags snapshots the parsed persistent flags into a
// SessionOptions. It is the ONLY reader of the flag package variables.
func sessionOptionsFromFlags() SessionOptions {
	return SessionOptions{
		ApprovalOverride:   approvalOverride,
		ResumeSessionID:    resumeSessionID,
		DirectiveOverrides: directiveOverrides,
		WorkspaceRoot:      workspaceRoot,
		AllowHomeAccess:    allowHomeAccess,
		WorkspaceAsk:       workspaceAsk,
	}
}
