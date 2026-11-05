package types

import "encoding/json"

// The streaming wire shapes live in this leaf package so both sides of the
// stream seam can name them without importing each other: the provider
// clients produce chunks, the engine's LLM layer consumes them, and neither
// has to own the other's package to say so (yaah-9RCG).

// StreamChunk represents a single SSE chunk from a streaming response.
type StreamChunk struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []StreamChoice `json:"choices"`
	Usage   *Usage         `json:"usage,omitempty"`
}

// StreamChoice represents a choice within a stream chunk.
type StreamChoice struct {
	Index        int         `json:"index"`
	Delta        StreamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

// StreamDelta contains the incremental content for a stream chunk.
type StreamDelta struct {
	Role               string     `json:"role,omitempty"`
	Content            string     `json:"content,omitempty"`
	ReasoningContent   string     `json:"reasoning_content,omitempty"`
	ReasoningSignature string     `json:"reasoning_signature,omitempty"`
	ToolCalls          []ToolCall `json:"tool_calls,omitempty"`

	// contentShapeErr records an unreadable content shape so the producer's
	// SendStream can report it once per stream; the delta degrades to empty
	// content rather than failing the chunk, keeping its tool calls. Never
	// serialized. Read it through ContentShapeErr: the field stays private
	// so the wire shape cannot be mutated to smuggle decode state.
	contentShapeErr error
}

// ContentShapeErr reports the content shape this delta could not decode, or
// nil when the delta decoded cleanly. The producer warns once per stream from
// it; the delta itself already degraded to empty content.
func (d StreamDelta) ContentShapeErr() error { return d.contentShapeErr }

// UnmarshalJSON decodes a delta that may carry content in either wire shape:
// the OpenAI plain string, or an array of typed blocks as zai/GLM streams it.
// Thinking blocks land in ReasoningContent and text blocks in Content;
// tool_calls and the signature fields pass through unchanged. A content shape
// that is neither degrades the delta to empty content — its tool calls
// survive, matching the response path — and the shape is recorded for
// SendStream's once-per-stream warning instead of dropped silently.
//
// reasoning_content goes through the same normalizer: some endpoints send
// typed blocks there too, and a shape this decoder cannot read must cost the
// delta only its reasoning, never its visible content or tool calls.
func (d *StreamDelta) UnmarshalJSON(data []byte) error {
	var w struct {
		Role               string          `json:"role"`
		Content            json.RawMessage `json:"content"`
		ReasoningContent   json.RawMessage `json:"reasoning_content"`
		ReasoningSignature string          `json:"reasoning_signature"`
		ToolCalls          []ToolCall      `json:"tool_calls"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	text, thinking, err := DecodeContentValue(w.Content)
	if err != nil {
		// Degrade instead of failing the chunk: an unreadable content shape
		// costs the delta its content and reasoning, never its tool calls —
		// the same contract as the response path. The shape is recorded so
		// SendStream can report it once per stream.
		text, thinking = "", ""
		d.contentShapeErr = err
	}
	reasoning, rThinking, rErr := DecodeContentValue(w.ReasoningContent)
	if rErr != nil {
		// Degrade instead of failing the chunk: dropping the whole delta is
		// how the visible content in the same chunk gets lost.
		reasoning, rThinking = "", ""
	}
	d.Role = w.Role
	d.Content = text
	// reasoning_content and content-embedded thinking can carry the same
	// reasoning twice; MergeReasoning drops the duplicate.
	d.ReasoningContent = MergeReasoning(reasoning+rThinking, thinking)
	d.ReasoningSignature = w.ReasoningSignature
	d.ToolCalls = w.ToolCalls
	return nil
}
