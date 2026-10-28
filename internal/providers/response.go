package providers

import (
	"encoding/json"
	"log/slog"

	"github.com/buchenberg/yaah/internal/types"
)

// wireResponse is the OpenAI-compatible non-streaming response body, decoded
// with one deliberate difference from types.ChatResponse: message.content is
// kept raw so the array-of-blocks shape some endpoints return is normalized
// instead of failing the unmarshal. zai/GLM sends the same typed blocks
// non-streamed as it streams.
type wireResponse struct {
	ID      string           `json:"id"`
	Model   string           `json:"model"`
	Choices []wireRespChoice `json:"choices"`
	Usage   types.Usage      `json:"usage,omitempty"`
}

// wireRespChoice mirrors types.Choice with lenient message decoding.
type wireRespChoice struct {
	Index        int         `json:"index"`
	Msg          wireRespMsg `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// wireRespMsg decodes a message by embedding types.Message and shadowing only
// the two content fields that need normalizing. Embedding (rather than
// hand-mirroring every field) means a field added to types.Message later still
// reaches the non-streaming path instead of being silently dropped here — the
// shallow Content/ReasoningContent below win the encoding/json field
// resolution, and decodeChatResponse writes the normalized values back.
type wireRespMsg struct {
	types.Message
	Content          json.RawMessage `json:"content"`
	ReasoningContent json.RawMessage `json:"reasoning_content"`
}

// decodeChatResponse unmarshals a non-streaming /chat/completions body into
// types.ChatResponse, normalizing array-shaped message content the same way
// the streaming decoder does (thinking blocks -> ReasoningContent, text blocks
// -> Content).
func decodeChatResponse(body []byte) (*types.ChatResponse, error) {
	var env wireResponse
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}

	out := &types.ChatResponse{ID: env.ID, Model: env.Model, Usage: env.Usage}
	if len(env.Choices) > 0 {
		out.Choices = make([]types.Choice, len(env.Choices))
	}
	for i, ch := range env.Choices {
		text, thinking, err := decodeContentValue(ch.Msg.Content)
		if err != nil {
			// A content shape this decoder cannot read costs the message
			// its content and reasoning, never its tool calls or the rest
			// of the response — the streaming path degrades the same way.
			// Logged so a wire-shape change stays diagnosable.
			slog.Warn("provider: unreadable message content shape", "choice", i, "error", err)
			text, thinking = "", ""
		}
		msg := ch.Msg.Message // the embedded types.Message, carried field for field
		msg.Content = text
		// reasoning_content is normally a plain string but some endpoints send
		// it as typed blocks too; a shape this decoder cannot read must not
		// cost the response its visible content, so the block-derived thinking
		// from content is kept either way. mergeReasoning drops the duplicate
		// when both sources carry the same reasoning.
		reasoning := thinking
		if rText, rThinking, rErr := decodeContentValue(ch.Msg.ReasoningContent); rErr == nil {
			reasoning = mergeReasoning(rText+rThinking, thinking)
		}
		msg.ReasoningContent = reasoning
		out.Choices[i] = types.Choice{
			Index:        ch.Index,
			Message:      msg,
			FinishReason: ch.FinishReason,
		}
	}
	return out, nil
}
