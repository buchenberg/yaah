package tools

import "context"

// dispatchWorkspaceKey is the context key for a per-dispatch workspace. It is
// an unexported empty struct so no other package can collide with it, and the
// accessors below are the only way in or out.
type dispatchWorkspaceKey struct{}

// WithDispatchWorkspace returns a context carrying the workspace a single
// dispatch runs in (yaah-ihfh.3). The retained lane owns the world — it
// provisions the container and tears it down — while the runner owns the tool
// registry that must run inside it. The context is what crosses that boundary
// without the tools package importing the runner (or the reverse).
//
// A nil workspace is a no-op: the dispatch keeps the session's substrate.
func WithDispatchWorkspace(ctx context.Context, ws Workspace) context.Context {
	if ws == nil {
		return ctx
	}
	return context.WithValue(ctx, dispatchWorkspaceKey{}, ws)
}

// DispatchWorkspace returns the workspace carried by ctx, or nil when the
// dispatch runs on the session's own substrate. A nil return means "no
// override", never "the process cwd".
func DispatchWorkspace(ctx context.Context) Workspace {
	ws, _ := ctx.Value(dispatchWorkspaceKey{}).(Workspace)
	return ws
}
