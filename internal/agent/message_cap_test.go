package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/buchenberg/yaah/internal/types"
)

func TestCapAssistantContent(t *testing.T) {
	huge := strings.Repeat("GO ", (maxAssistantContentBytes/3)+1000)

	t.Run("under the cap is untouched", func(t *testing.T) {
		msg := types.AssistantMsg("a short answer", nil)
		got := capAssistantContent(msg)
		if got.Content != msg.Content {
			t.Fatalf("content changed: %d bytes -> %d bytes", len(msg.Content), len(got.Content))
		}
	})

	t.Run("exactly at the cap is untouched", func(t *testing.T) {
		exact := strings.Repeat("x", maxAssistantContentBytes)
		got := capAssistantContent(types.AssistantMsg(exact, nil))
		if got.Content != exact {
			t.Fatalf("content changed at exactly the cap (%d -> %d bytes)", len(exact), len(got.Content))
		}
	})

	t.Run("over the cap is bounded and marked", func(t *testing.T) {
		got := capAssistantContent(types.AssistantMsg(huge, nil))
		if len(got.Content) > maxAssistantContentBytes {
			t.Fatalf("capped content = %d bytes; want <= %d", len(got.Content), maxAssistantContentBytes)
		}
		if !strings.Contains(got.Content, "assistant message truncated") {
			t.Fatal("no truncation marker in the capped content")
		}
		if !strings.Contains(got.Content, "bytes dropped") {
			t.Fatal("the marker does not report the dropped byte count")
		}
	})

	t.Run("head and tail survive", func(t *testing.T) {
		content := "HEAD-" + strings.Repeat("m", maxAssistantContentBytes) + "-TAIL"
		got := capAssistantContent(types.AssistantMsg(content, nil))
		if !strings.HasPrefix(got.Content, "HEAD-") {
			t.Errorf("head lost: %q", got.Content[:min(40, len(got.Content))])
		}
		if !strings.HasSuffix(got.Content, "-TAIL") {
			t.Errorf("tail lost: %q", got.Content[len(got.Content)-min(40, len(got.Content)):])
		}
	})

	t.Run("multibyte content stays valid utf8", func(t *testing.T) {
		content := strings.Repeat("é", maxAssistantContentBytes)
		got := capAssistantContent(types.AssistantMsg(content, nil))
		if !utf8.ValidString(got.Content) {
			t.Fatal("capped content is not valid UTF-8 — a cut split a rune")
		}
		if len(got.Content) > maxAssistantContentBytes {
			t.Errorf("capped content = %d bytes; want <= %d", len(got.Content), maxAssistantContentBytes)
		}
	})

	t.Run("capping is idempotent", func(t *testing.T) {
		once := capAssistantContent(types.AssistantMsg(huge, nil))
		twice := capAssistantContent(once)
		if twice.Content != once.Content {
			t.Fatal("a second cap changed an already-capped message")
		}
	})

	t.Run("tool calls are preserved", func(t *testing.T) {
		calls := []types.ToolCall{{ID: "call-1", Function: types.ToolCallFn{Name: "read", Arguments: `{}`}}}
		got := capAssistantContent(types.AssistantMsg(huge, calls))
		if len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "call-1" {
			t.Fatalf("tool calls lost: %+v", got.ToolCalls)
		}
	})
}
