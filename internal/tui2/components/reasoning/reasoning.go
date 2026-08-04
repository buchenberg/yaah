// Package reasoning renders expandable chain-of-thought reasoning blocks
// for the TUI2 message stream. The header uses lolcat rainbow coloring.
package reasoning

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/tui2/colors"
	"github.com/buchenberg/yaah/internal/tui2/lolcat"
)

// Block is a collapsible reasoning section in the message stream.
type Block struct {
	content  string
	expanded bool
	id       string
	seed     float64 // seed for lolcat rainbow, advanced each frame
}

// New creates a reasoning block with the given content and unique ID.
func New(id, content string, seed float64) *Block {
	return &Block{id: id, content: content, seed: seed}
}

// Toggle switches between expanded and collapsed.
func (b *Block) Toggle() {
	b.expanded = !b.expanded
}

// IsExpanded reports whether the block is currently expanded.
func (b *Block) IsExpanded() bool { return b.expanded }

// ID returns the block's region identifier.
func (b *Block) ID() string { return b.id }

// SetSeed advances the lolcat seed for the flowing rainbow effect.
func (b *Block) SetSeed(seed float64) { b.seed = seed }

// Render returns the full tview-tagged text for the block,
// suitable for insertion into a tview.TextView region.
func (b *Block) Render() string {
	if !b.expanded {
		return b.renderCollapsed()
	}
	return b.renderExpanded()
}

func (b *Block) renderCollapsed() string {
	label := lolcat.Rainbow("Reasoning...", b.seed)
	return fmt.Sprintf(`  [%s]▶ %s[-]`, colors.Dim, label)
}

func (b *Block) renderExpanded() string {
	label := lolcat.Rainbow("Reasoning...", b.seed)
	labelLen := len(lolcat.StripTags(label))
	dashLen := max(4, 60-labelLen-4) // 4 chars for "  ▼ "
	header := fmt.Sprintf(`  [%s]▼ %s [%s]%s[-]`, colors.Dim, label, colors.Dim,
		strings.Repeat("─", dashLen))
	var bld strings.Builder
	bld.WriteString(header)
	bld.WriteString("\n")
	for _, line := range strings.Split(b.content, "\n") {
		bld.WriteString(fmt.Sprintf(`  [%s]│[-] %s`, colors.Dim, line))
		bld.WriteString("\n")
	}
	bld.WriteString(fmt.Sprintf(`  [%s]╰%s[-]`, colors.Dim, strings.Repeat("─", 58)))
	return bld.String()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
