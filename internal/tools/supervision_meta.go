package tools

import "context"

// Supervision metadata is tool-observed context the advisory rules read
// from a declaration's payload. Tools attach facts they are uniquely
// positioned to observe — the write tool knows whether its target file
// existed before the call — and the agent loop copies the map into the
// ToolResult so the trace middleware publishes it alongside the
// declaration. A tool without observations attaches nothing; the rule
// layer treats a missing field as unknown and stays conservative.
type supervisionMetaKey struct{}

// WithSupervisionMeta returns a context carrying a metadata sink for one
// tool execution. The caller owns the pointed-to map and reads it after
// the call returns.
func WithSupervisionMeta(ctx context.Context, sink *map[string]any) context.Context {
	return context.WithValue(ctx, supervisionMetaKey{}, sink)
}

// SetSupervisionMeta records one fact on the context's sink. Without a
// sink (direct tool use outside a supervised loop) it is a no-op, so
// tools never branch on the supervision layer's presence.
func SetSupervisionMeta(ctx context.Context, key string, value any) {
	sink, ok := ctx.Value(supervisionMetaKey{}).(*map[string]any)
	if !ok || sink == nil {
		return
	}
	if *sink == nil {
		*sink = map[string]any{}
	}
	(*sink)[key] = value
}
