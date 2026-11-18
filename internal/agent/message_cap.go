package agent

import (
	"fmt"
	"log/slog"
	"unicode/utf8"

	"github.com/buchenberg/yaah/internal/types"
)

// maxAssistantContentBytes is the ceiling on a single assistant message's
// text, applied where the message joins the conversation.
//
// Without a ceiling a model that loses the plot writes whatever its output
// budget allows, and the message that lands is unbounded: one real session
// stored a 2,784,115-byte assistant message (a repeated-token loop of 334,144
// "GO" tokens). The damage is not the row — it is that the row is a prompt:
// every later turn re-sends it, so a single runaway answer poisons the rest of
// the session and the store. 256 KiB is far above any legitimate answer
// (roughly 4x a full output budget's worth of text) and far below the size at
// which the message stops being a conversation.
const maxAssistantContentBytes = 256 << 10

// assistantTruncationMarker names the drop in-band. It is deliberately not
// prose-shaped: a reader (or the model) must be able to tell the message is
// incomplete without mistaking the note for output.
const assistantTruncationMarker = "\n…[yaah: assistant message truncated, %d bytes dropped]\n"

// CapAssistantContent bounds an assistant message's content, keeping the head
// and the tail and replacing the middle with a marker naming the dropped byte
// count.
//
// Both ends survive because both carry meaning: the head holds what the answer
// set out to say, the tail holds how it concluded. The message is capped at
// creation, before it is appended and persisted, so the copy that re-enters the
// next prompt is the capped one — a cap applied only at persistence would leave
// a poisoned conversation in memory.
//
// It is exported because creation is not the only way a message enters a
// conversation: a resumed session rebuilds its transcript from rows that may
// predate the cap (see cmd/yaah/resume.go), and that replay path needs the same
// bound or a session poisoned once stays poisoned for every future resume.
//
// Scope: the content field only. ReasoningContent and tool-call arguments are
// not capped here — they are not what this guard was written for, and both are
// bounded by the provider's output budget in the ordinary case.
//
// A message already within the cap is returned unchanged (same string, no
// copy), so the common path costs one length comparison.
func CapAssistantContent(msg types.Message) types.Message {
	if len(msg.Content) <= maxAssistantContentBytes {
		return msg
	}
	original := len(msg.Content)
	marker := fmt.Sprintf(assistantTruncationMarker, original)
	// The marker is part of the message, so it is paid for out of the cap
	// rather than added on top of it. The count it names is an upper bound
	// (it is computed from the full length before the rune-safe cuts), which
	// is the honest direction for a size report to err in.
	budget := maxAssistantContentBytes - len(marker)
	if budget < 0 {
		budget = 0
	}
	head := budget * 3 / 4
	tail := budget - head

	keptHead := headPrefix(msg.Content, head)
	keptTail := tailSuffix(msg.Content, tail)
	dropped := original - len(keptHead) - len(keptTail)

	msg.Content = keptHead + fmt.Sprintf(assistantTruncationMarker, dropped) + keptTail
	slog.Warn("assistant message truncated to keep the session promptable",
		"original_bytes", original,
		"kept_bytes", len(msg.Content),
		"dropped_bytes", dropped,
		"cap_bytes", maxAssistantContentBytes,
		"tool_calls", len(msg.ToolCalls))
	return msg
}

// headPrefix returns at most n bytes from the start of s without splitting a
// UTF-8 sequence.
func headPrefix(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// tailSuffix returns at most n bytes from the end of s on a character
// boundary.
func tailSuffix(s string, n int) string {
	if n <= 0 || n >= len(s) {
		if n <= 0 {
			return ""
		}
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}
