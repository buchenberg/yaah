// Package observability provides OpenTelemetry tracing and metrics
// for the yaah agent harness.
package observability

import (
	"context"
	"sort"
	"sync"
	"time"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// RecordedSpan is a serializable snapshot of a completed span.
type RecordedSpan struct {
	Name       string                   `json:"name"`
	TraceID    string                   `json:"trace_id"`
	SpanID     string                   `json:"span_id"`
	ParentID   string                   `json:"parent_id,omitempty"`
	Start      time.Time                `json:"start"`
	End        time.Time                `json:"end"`
	DurationMs int64                    `json:"duration_ms"`
	Attributes map[string]any           `json:"attributes,omitempty"`
	Events     []RecordedEvent          `json:"events,omitempty"`
	Status     string                   `json:"status,omitempty"`
	StatusMsg  string                   `json:"status_message,omitempty"`
}

// RecordedEvent is a serializable span event.
type RecordedEvent struct {
	Name       string         `json:"name"`
	Time       time.Time      `json:"time"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// SpanNode is a tree node for hierarchical trace display.
type SpanNode struct {
	Span     RecordedSpan `json:"span"`
	Children []*SpanNode  `json:"children,omitempty"`
}

// maxBufferedSpans caps the in-memory span ring buffer to prevent
// unbounded growth in long-running serve sessions.
const maxBufferedSpans = 10000

// BufferingSpanProcessor implements sdktrace.SpanProcessor and collects
// completed spans in-memory so they can be queried programmatically
// without requiring Jaeger or any other external trace backend.
type BufferingSpanProcessor struct {
	mu    sync.Mutex
	spans []RecordedSpan
}

// NewBufferingSpanProcessor creates a new BufferingSpanProcessor.
func NewBufferingSpanProcessor() *BufferingSpanProcessor {
	return &BufferingSpanProcessor{}
}

// OnStart is a no-op.
func (b *BufferingSpanProcessor) OnStart(_ context.Context, _ sdktrace.ReadWriteSpan) {}

// OnEnd converts the completed span to a RecordedSpan and appends it
// to the in-memory buffer.
func (b *BufferingSpanProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	rs := recordSpan(s)
	b.mu.Lock()
	if len(b.spans) >= maxBufferedSpans {
		b.spans = b.spans[1:]
	}
	b.spans = append(b.spans, rs)
	b.mu.Unlock()
}

// Shutdown is a no-op.
func (b *BufferingSpanProcessor) Shutdown(_ context.Context) error {
	return nil
}

// ForceFlush is a no-op.
func (b *BufferingSpanProcessor) ForceFlush(_ context.Context) error {
	return nil
}

// Traces returns a copy of all buffered spans. The caller may safely
// iterate the returned slice without synchronization.
func (b *BufferingSpanProcessor) Traces() []RecordedSpan {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make([]RecordedSpan, len(b.spans))
	copy(result, b.spans)
	return result
}

// TraceTree builds a parent-child tree for the given traceID. Returns
// roots sorted by start time.
func (b *BufferingSpanProcessor) TraceTree(traceID string) []*SpanNode {
	b.mu.Lock()
	defer b.mu.Unlock()

	nodeMap := make(map[string]*SpanNode)
	for _, s := range b.spans {
		if s.TraceID != traceID {
			continue
		}
		nodeMap[s.SpanID] = &SpanNode{Span: s}
	}

	var roots []*SpanNode
	for _, node := range nodeMap {
		if pid := node.Span.ParentID; pid != "" {
			if parent, ok := nodeMap[pid]; ok {
				parent.Children = append(parent.Children, node)
			} else {
				roots = append(roots, node)
			}
		} else {
			roots = append(roots, node)
		}
	}

	sort.Slice(roots, func(i, j int) bool {
		return roots[i].Span.Start.Before(roots[j].Span.Start)
	})
	for _, node := range nodeMap {
		sort.Slice(node.Children, func(i, j int) bool {
			return node.Children[i].Span.Start.Before(node.Children[j].Span.Start)
		})
	}

	return roots
}

// Reset clears all buffered spans. Useful for between benchmark runs.
func (b *BufferingSpanProcessor) Reset() {
	b.mu.Lock()
	b.spans = nil
	b.mu.Unlock()
}

// recordSpan converts an sdktrace.ReadOnlySpan to a RecordedSpan.
func recordSpan(s sdktrace.ReadOnlySpan) RecordedSpan {
	rs := RecordedSpan{
		Name:    s.Name(),
		TraceID: s.SpanContext().TraceID().String(),
		SpanID:  s.SpanContext().SpanID().String(),
		Start:   s.StartTime(),
		End:     s.EndTime(),
	}

	rs.DurationMs = rs.End.Sub(rs.Start).Milliseconds()

	if s.Parent().IsValid() {
		rs.ParentID = s.Parent().SpanID().String()
	}

	// Copy attributes.
	if attrs := s.Attributes(); len(attrs) > 0 {
		rs.Attributes = make(map[string]any, len(attrs))
		for _, kv := range attrs {
			rs.Attributes[string(kv.Key)] = kv.Value.AsInterface()
		}
	}

	// Copy events.
	if events := s.Events(); len(events) > 0 {
		rs.Events = make([]RecordedEvent, 0, len(events))
		for _, e := range events {
			re := RecordedEvent{
				Name: e.Name,
				Time: e.Time,
			}
			if attrs := e.Attributes; len(attrs) > 0 {
				re.Attributes = make(map[string]any, len(attrs))
				for _, kv := range attrs {
					re.Attributes[string(kv.Key)] = kv.Value.AsInterface()
				}
			}
			rs.Events = append(rs.Events, re)
		}
	}

	// Copy status.
	status := s.Status()
	switch status.Code {
	case codes.Ok:
		rs.Status = "ok"
	case codes.Error:
		rs.Status = "error"
	}
	if status.Description != "" {
		rs.StatusMsg = status.Description
	}

	return rs
}
