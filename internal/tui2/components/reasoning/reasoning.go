package reasoning

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/tui2/colors"
	"github.com/buchenberg/yaah/internal/tui2/lolcat"
)

type Block struct {
	content  string
	expanded bool
	id       string
	seed     float64
	theme    *colors.Theme
}

func New(id, content string, seed float64, th *colors.Theme) *Block {
	return &Block{id: id, content: content, seed: seed, theme: th}
}

func (b *Block) Toggle()              { b.expanded = !b.expanded }
func (b *Block) IsExpanded() bool     { return b.expanded }
func (b *Block) ID() string           { return b.id }
func (b *Block) SetSeed(seed float64) { b.seed = seed }

func (b *Block) Render() string { return b.RenderCtx(colors.RenderCtx{Theme: b.theme}) }

func (b *Block) RenderCtx(ctx colors.RenderCtx) string {
	if !b.expanded {
		return b.renderCollapsed()
	}
	return b.renderExpanded(ctx.Width)
}

func (b *Block) renderCollapsed() string {
	label := lolcat.Rainbow("Reasoning...", b.seed)
	return fmt.Sprintf(`  %s▶ %s%s`, b.theme.DimTag(), label, b.theme.ResetTag())
}

func (b *Block) renderExpanded(width int) string {
	if width <= 0 {
		width = 60
	}
	label := lolcat.Rainbow("Reasoning...", b.seed)
	labelLen := len(lolcat.StripTags(label))
	dashLen := max(4, width-labelLen-4)
	header := fmt.Sprintf(`  %s▼ %s %s%s%s`, b.theme.DimTag(), label, b.theme.DimTag(),
		strings.Repeat("─", dashLen), b.theme.ResetTag())
	var bld strings.Builder
	bld.WriteString(header)
	bld.WriteString("\n")
	for _, line := range strings.Split(b.content, "\n") {
		bld.WriteString(fmt.Sprintf(`  %s│%s %s`, b.theme.DimTag(), b.theme.ResetTag(), line))
		bld.WriteString("\n")
	}
	bld.WriteString(fmt.Sprintf(`  %s╰%s%s`, b.theme.DimTag(), strings.Repeat("─", width), b.theme.ResetTag()))
	return bld.String()
}
