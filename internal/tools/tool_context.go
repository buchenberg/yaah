package tools

import "context"

type toolCallIDKey struct{}

// WithToolCallID attaches the provider tool-call id to the dispatch context.
// Workspaces can use it to correlate a client-side terminal with the tool-call
// row the view already announced.
func WithToolCallID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, toolCallIDKey{}, id)
}

// ToolCallIDFromContext returns the dispatch tool-call id, or "".
func ToolCallIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(toolCallIDKey{}).(string)
	return id
}

type toolNameKey struct{}

// WithToolName attaches the executing tool's name to the context.
func WithToolName(ctx context.Context, name string) context.Context {
	if name == "" {
		return ctx
	}
	return context.WithValue(ctx, toolNameKey{}, name)
}

// ToolNameFromContext returns the executing tool's name, or "".
func ToolNameFromContext(ctx context.Context) string {
	name, _ := ctx.Value(toolNameKey{}).(string)
	return name
}
