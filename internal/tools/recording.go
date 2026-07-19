package tools

import (
	"context"
	"encoding/json"
)

type contextKey string

const conflictLabelKey contextKey = "yaah-conflict-label"

func WithConflictLabel(ctx context.Context, label string) context.Context {
	return context.WithValue(ctx, conflictLabelKey, label)
}

type RecordingTool struct {
	inner   Tool
	tracker *ConflictTracker
}

func NewRecordingTool(inner Tool, tracker *ConflictTracker) *RecordingTool {
	return &RecordingTool{inner: inner, tracker: tracker}
}

func (rt *RecordingTool) Name() string            { return rt.inner.Name() }
func (rt *RecordingTool) Description() string     { return rt.inner.Description() }
func (rt *RecordingTool) Schema() json.RawMessage { return rt.inner.Schema() }

func (rt *RecordingTool) Execute(ctx context.Context, args string) (string, error) {
	if rt.tracker != nil {
		if label, ok := ctx.Value(conflictLabelKey).(string); ok && label != "" {
			var p struct {
				FilePath string `json:"filePath"`
			}
			if json.Unmarshal([]byte(args), &p) == nil && p.FilePath != "" {
				rt.tracker.Record(label, p.FilePath, rt.inner.Name())
			}
		}
	}
	return rt.inner.Execute(ctx, args)
}
