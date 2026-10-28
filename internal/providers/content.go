package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// contentBlock is one entry of the array-shaped content some
// OpenAI-compatible providers emit in place of a plain string. ZAI's GLM
// models, for example, stream reasoning as
//
//	{"delta":{"content":[{"type":"thinking",
//	                      "thinking":[{"type":"text","text":"The"}],
//	                      "closed":true}]}}
//
// so a thinking block nests its text one level deeper, while visible text
// arrives as {"type":"text","text":"..."}. Both block shapes are legal on
// the wire for these endpoints, so the decoder accepts either.
type contentBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking json.RawMessage `json:"thinking"`
	Content  json.RawMessage `json:"content"`
}

// decodeContentValue normalizes a wire content value into its visible text and
// its reasoning text. The value is either a plain JSON string (the OpenAI
// shape) or an array of typed blocks (zai/GLM). The streaming and
// non-streaming paths share this decoder because the same endpoint sends the
// same block array on both.
//
// A top-level shape that is neither is an error, and the caller turns it into
// one warning per turn: silently dropping the chunk is how lost reasoning
// stays invisible.
func decodeContentValue(raw json.RawMessage) (text, thinking string, err error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", "", nil
	}

	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", "", fmt.Errorf("decode content string: %w", err)
		}
		return s, "", nil

	case '[':
		var blocks []contentBlock
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return "", "", fmt.Errorf("decode content blocks: %w", err)
		}
		var visible, reasoning strings.Builder
		for _, b := range blocks {
			switch b.Type {
			case "thinking", "reasoning", "thought", "analysis", "summary":
				// A reasoning block's payload is nested text blocks rather than
				// inlined text. The type names beyond "thinking" are a
				// recognized-reasoning set: endpoints that name their reasoning
				// differently are routed here so hidden text cannot be replayed
				// into history as the assistant's visible words (review 616).
				if len(b.Thinking) == 0 {
					// Some endpoints inline the thinking text instead of
					// nesting text blocks.
					reasoning.WriteString(b.Text)
					continue
				}
				inner, _, err := decodeContentValue(b.Thinking)
				if err != nil {
					return "", "", err
				}
				reasoning.WriteString(inner)
			case "text":
				if b.Text != "" {
					visible.WriteString(b.Text)
					continue
				}
				inner, _, err := decodeContentValue(b.Content)
				if err != nil {
					return "", "", err
				}
				visible.WriteString(inner)
			default:
				// Unknown block type: skip it only when it carries no user
				// text (images, opaque metadata). Text of a type this decoder
				// does not know is recovered verbatim — dropping it before its
				// text is read is how a provider adding a new visible block
				// type would cost the response its content. The recovery assumes
				// visibility only for types not named like reasoning: those are
				// gated into the reasoning channel above, so hidden text cannot
				// be replayed into history as the assistant's words.
				if b.Text != "" {
					visible.WriteString(b.Text)
				}
			}
		}
		return visible.String(), reasoning.String(), nil
	}

	// Only the shape is reported, never the payload: this error travels to a
	// log line, and the payload can be model output the user would see.
	return "", "", fmt.Errorf("unsupported content shape (starts with %q, %d bytes)", raw[0], len(raw))
}

// mergeReasoning joins the two places a reasoning-capable endpoint may put
// reasoning in one response: the reasoning_content field and thinking blocks
// carried in content. Endpoints that send both echo the same text twice, so
// equal halves collapse to one copy; unequal halves both survive, since the
// two places can hold complementary segments.
func mergeReasoning(field, blocks string) string {
	switch {
	case field == "":
		return blocks
	case blocks == "" || field == blocks:
		return field
	default:
		return field + blocks
	}
}
