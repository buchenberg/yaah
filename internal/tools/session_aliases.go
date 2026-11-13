package tools

// Compatibility seam for the session-workspace extraction (bead yaah-5D85).
//
// The container session world — the session's sandbox lifecycle, the D1
// copy-in bootstrap, apply-out, the image-contract probe, and the per-dispatch
// workspace carried on a context — now lives in internal/tools/substrate, so
// the retained dispatch lane (internal/dispatch) reaches it without importing
// this package. The aliases and forwarders below keep existing callers reading
// tools.X; new code should import internal/tools/substrate directly.

import (
	"context"

	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/tools/substrate"
)

// SessionSandbox is the session's isolated workspace sandbox, which owns one
// container from before the first tool dispatch until session teardown. See
// substrate.SessionSandbox.
type SessionSandbox = substrate.SessionSandbox

// SandboxOpener constructs one unprovisioned sandbox from the workspace
// config. See substrate.SandboxOpener.
type SandboxOpener = substrate.SandboxOpener

// StartSessionSandbox probes the image contract, then creates the session's
// sandbox and runs the D1 bootstrap when src is non-empty.
func StartSessionSandbox(ctx context.Context, w config.WorkspaceConfig, open SandboxOpener, src string, deny []string) (*SessionSandbox, error) {
	return substrate.StartSessionSandbox(ctx, w, open, src, deny)
}

// OpenContainerdWorkspaceSandbox resolves the container workspace backend
// through goatherd-runtime's sandbox registry.
func OpenContainerdWorkspaceSandbox(w config.WorkspaceConfig) (goatherdsandbox.Sandbox, error) {
	return substrate.OpenContainerdWorkspaceSandbox(w)
}

// WithDispatchWorkspace returns a context carrying the workspace a single
// dispatch runs in.
func WithDispatchWorkspace(ctx context.Context, ws Workspace) context.Context {
	return substrate.WithDispatchWorkspace(ctx, ws)
}

// DispatchWorkspace returns the workspace a dispatch runs in, or nil when the
// dispatch runs on the session's own substrate.
func DispatchWorkspace(ctx context.Context) Workspace { return substrate.DispatchWorkspace(ctx) }

// newSessionSandbox wraps an already-created backend in a session sandbox;
// tests use it where wiring calls StartSessionSandbox.
func newSessionSandbox(sb goatherdsandbox.Sandbox, root string) *SessionSandbox {
	return substrate.NewSessionSandbox(sb, root)
}
