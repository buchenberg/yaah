package tui2

import (
	"strings"

	"github.com/buchenberg/yaah/internal/tui2/components/reasoning"
	"github.com/buchenberg/yaah/internal/tui2/components/subagent"
	"github.com/buchenberg/yaah/internal/tui2/components/toolblock"
)

// Conversation is the single source of truth for all content rendered in the
// messages pane. It replaces the previous plainMessages []string +
// conversationLog []convItem dual storage (a real drift bug).
type Conversation struct {
	items []convItem
}

// convItem is a single entry in the chronological conversation log.
// Exactly one of text/isMarkdown/toolBlock/subBlock/reasoningBlock is set.
type convItem struct {
	text       string
	isMarkdown bool // text is raw markdown, needs renderMarkdown at refresh time
	// When streaming, Conversation.Render appends pendingTokens after stored
	// items. Callers must flush pending tokens via TUI2.flushPendingTokens
	// before appending a non-text item during streaming to preserve order.

	toolBlock      *toolblock.Block
	subBlock       *subagent.Block
	reasoningBlock *reasoning.Block
}

// AppendText adds a plain-text (user/system/compaction) message.
func (c *Conversation) AppendText(text string) {
	c.items = append(c.items, convItem{text: text})
}

// AppendAssistant appends raw markdown text. It is rendered at refresh-time
// with the current pane width so that terminal resizes reflow correctly.
func (c *Conversation) AppendAssistant(md string) {
	if md == "" {
		return
	}
	c.items = append(c.items, convItem{text: md, isMarkdown: true})
}

// AppendTool adds a tool call block to the conversation.
func (c *Conversation) AppendTool(tb *toolblock.Block) {
	c.items = append(c.items, convItem{toolBlock: tb})
}

// AppendSubAgent adds a sub-agent block to the conversation.
func (c *Conversation) AppendSubAgent(sb *subagent.Block) {
	c.items = append(c.items, convItem{subBlock: sb})
}

// AppendReasoning adds a reasoning block to the conversation.
func (c *Conversation) AppendReasoning(rb *reasoning.Block) {
	c.items = append(c.items, convItem{reasoningBlock: rb})
}

// Clear resets the conversation to empty.
func (c *Conversation) Clear() {
	c.items = nil
}

// Len returns the number of conversation items.
func (c *Conversation) Len() int { return len(c.items) }

// Render builds the full tview-tagged content string for the messages
// TextView from all conversation items in chronological order.
func (c *Conversation) Render(ctx RenderCtx, streamingTokens, spinner string, thinkingVisible bool) string {
	var b strings.Builder

	for _, item := range c.items {
		switch {
		case item.text != "":
			b.WriteString("\n")
			if item.isMarkdown {
				b.WriteString(renderMarkdown(item.text, ctx.Width))
			} else {
				b.WriteString(item.text)
			}
			b.WriteString("\n\n")
		case item.toolBlock != nil:
			b.WriteString(item.toolBlock.Render())
			b.WriteString("\n")
		case item.subBlock != nil:
			b.WriteString(item.subBlock.Render())
			b.WriteString("\n")
		case item.reasoningBlock != nil:
			b.WriteString("\n")
			b.WriteString(item.reasoningBlock.Render())
			b.WriteString("\n\n")
		}
	}

	if streamingTokens != "" {
		b.WriteString(renderMarkdown(streamingTokens, ctx.Width))
		b.WriteString("\n")
	}

	if thinkingVisible && spinner != "" {
		b.WriteString("\n")
		b.WriteString(spinner)
		b.WriteString("\n")
	}

	return b.String()
}
