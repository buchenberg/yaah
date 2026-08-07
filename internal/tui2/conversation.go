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
// Exactly one field is set.
type convItem struct {
	text           string
	toolBlock      *toolblock.Block
	subBlock       *subagent.Block
	reasoningBlock *reasoning.Block
}

// AppendText adds a plain-text (user/system/compaction) message.
func (c *Conversation) AppendText(text string) {
	c.items = append(c.items, convItem{text: text})
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
			b.WriteString(item.text)
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
