package acp

import (
	"encoding/json"

	"github.com/buchenberg/yaah/internal/types"
)

// editArgs mirrors the wire shape of the edit tool's arguments. Only
// the fields that translate into diff blocks matter here; the tool's
// own parsing rules (fuzzy fallback, escape decoding) stay in
// internal/tools — a diff block that fails to derive is simply omitted
// and the plain result text continues to be shown.
type editArgs struct {
	FilePath  string          `json:"filePath"`
	OldString string          `json:"oldString"`
	NewString string          `json:"newString"`
	Edits     []editWireEntry `json:"edits"`
}

type editWireEntry struct {
	OldString string `json:"oldString"`
	NewString string `json:"newString"`
}

// writeArgs mirrors the write tool's arguments.
type writeArgs struct {
	FilePath string `json:"filePath"`
	Content  string `json:"content"`
}

// diffWalker identifies tool calls whose arguments describe file
// modifications the client can render as diffs and jump-to-file
// locations. patch and replace are deliberately not covered: replace
// is multi-file (no single path to locate), and patch works in hunk
// space rather than whole-file old/new text, so neither produces an
// honest DiffBlock — their result text is still shown as plain
// content.
func diffWalker(name, rawArgs string) ([]ToolCallLocation, []UpdateContent) {
	if rawArgs == "" || !json.Valid([]byte(rawArgs)) {
		return nil, nil
	}
	switch name {
	case "write":
		var a writeArgs
		if err := json.Unmarshal([]byte(rawArgs), &a); err != nil || a.FilePath == "" {
			return nil, nil
		}
		return []ToolCallLocation{{Path: a.FilePath}}, []UpdateContent{{
			Type:    "diff",
			Path:    a.FilePath,
			NewText: a.Content,
		}}
	case "edit":
		var a editArgs
		if err := json.Unmarshal([]byte(rawArgs), &a); err != nil || a.FilePath == "" {
			return nil, nil
		}
		locs := []ToolCallLocation{{Path: a.FilePath}}
		pairs := a.Edits
		var diffs []UpdateContent
		if len(pairs) == 0 {
			if a.OldString == a.NewString {
				return nil, nil
			}
			pairs = []editWireEntry{{OldString: a.OldString, NewString: a.NewString}}
		}
		for _, e := range pairs {
			if e.OldString == e.NewString {
				continue
			}
			old := e.OldString
			diffs = append(diffs, UpdateContent{
				Type:    "diff",
				Path:    a.FilePath,
				OldText: &old,
				NewText: e.NewString,
			})
		}
		if len(diffs) == 0 {
			return nil, nil
		}
		return locs, diffs
	default:
		return nil, nil
	}
}

// usageMeta renders the engine's end-of-turn tally as the usage_update
// _meta bag. Only non-zero token counts are included so the bag stays
// compact; finishReason and responseModel are the turn's raw engine
// values (not the mapped ACP stopReason, which the session/prompt
// response already carries).
func usageMeta(usage types.Usage, finishReason, responseModel string) json.RawMessage {
	m := map[string]any{}
	if usage.PromptTokens > 0 {
		m["promptTokens"] = usage.PromptTokens
	}
	if usage.CompletionTokens > 0 {
		m["completionTokens"] = usage.CompletionTokens
	}
	if usage.CachedPromptTokens > 0 {
		m["cachedPromptTokens"] = usage.CachedPromptTokens
	}
	if usage.TotalTokens > 0 {
		m["totalTokens"] = usage.TotalTokens
	}
	if finishReason != "" {
		m["finishReason"] = finishReason
	}
	if responseModel != "" {
		m["responseModel"] = responseModel
	}
	if len(m) == 0 {
		return nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return raw
}
