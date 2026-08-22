package subagent

import (
	"fmt"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/tui2/colors"
)

type Block struct {
	id           string
	role         string
	displayName  string
	specialty    string
	task         string
	model        string
	state        State
	expanded     bool
	startTime    time.Time
	endTime      time.Time
	err          string
	blinkVisible bool
	spinnerFrame int
	theme        *colors.Theme
}

type State int

const (
	Active State = iota
	Done
	Error
)

func New(id, role, specialty, task, model string, th *colors.Theme) *Block {
	name := subagent.RoleDisplayName(subagent.SubAgentRole(role))
	label := name
	if name != role {
		label = fmt.Sprintf("%s (%s)", name, role)
	}
	return &Block{
		id:           id,
		role:         role,
		displayName:  label,
		specialty:    specialty,
		task:         task,
		model:        model,
		state:        Active,
		startTime:    time.Now(),
		blinkVisible: true,
		theme:        th,
	}
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (b *Block) ID() string             { return b.id }
func (b *Block) Role() string           { return b.role }
func (b *Block) S() State               { return b.state }
func (b *Block) Task() string           { return b.task }
func (b *Block) DisplayName() string    { return b.displayName }
func (b *Block) Elapsed() time.Duration { return time.Since(b.startTime) }
func (b *Block) IsExpanded() bool       { return b.expanded }
func (b *Block) Toggle()                { b.expanded = !b.expanded }
func (b *Block) ToggleBlink()           { b.blinkVisible = !b.blinkVisible }

func (b *Block) AdvanceSpinner() {
	if b.state == Active {
		b.spinnerFrame = (b.spinnerFrame + 1) % len(spinnerFrames)
	}
}

func (b *Block) Complete() {
	b.endTime = time.Now()
	b.state = Done
}

func (b *Block) Fail(err string) {
	b.endTime = time.Now()
	b.state = Error
	b.err = err
}

func (b *Block) Render() string { return b.RenderCtx(colors.RenderCtx{Theme: b.theme}) }

func (b *Block) RenderCtx(ctx colors.RenderCtx) string {
	if !b.expanded {
		return b.renderCollapsed()
	}
	return b.renderExpanded(ctx.Width)
}

func (b *Block) robot() string {
	if b.state != Active {
		return "🤖"
	}
	if b.blinkVisible {
		return "🤖"
	}
	return " "
}

func (b *Block) roleHex() string { return b.theme.RoleHex(b.role) }

func (b *Block) durationStr() string {
	d := b.endTime.Sub(b.startTime)
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func (b *Block) renderCollapsed() string {
	hex := b.roleHex()
	switch b.state {
	case Active:
		return fmt.Sprintf(`  %s%s %s %s%s %s· %s%s`,
			b.theme.ColorTag(hex), spinnerFrames[b.spinnerFrame], b.robot(), b.displayName, b.theme.ResetTag(),
			b.theme.SecondaryTag(), b.task, b.theme.ResetTag())
	case Done:
		return fmt.Sprintf(`  %s✓ %s %s%s %s· %s (%s)%s`,
			b.theme.ColorTag(hex), b.robot(), b.displayName, b.theme.ResetTag(),
			b.theme.SecondaryTag(), b.task, b.durationStr(), b.theme.ResetTag())
	case Error:
		return fmt.Sprintf(`  %s✗ %s %s%s %s· %s (%s)%s`,
			b.theme.ColorTag(hex), b.robot(), b.displayName, b.theme.ResetTag(), b.theme.DimTag(), b.theme.Tag(b.theme.Error, b.task), b.durationStr(), b.theme.ResetTag())
	default:
		return ""
	}
}

func (b *Block) renderExpanded(width int) string {
	if width <= 0 {
		width = 58
	}
	hex := b.roleHex()
	var bld strings.Builder

	var header string
	switch b.state {
	case Active:
		header = fmt.Sprintf(`  %s%s %s %s%s`, b.theme.ColorTag(hex), spinnerFrames[b.spinnerFrame], b.robot(), b.displayName, b.theme.ResetTag())
	case Done:
		header = fmt.Sprintf(`  %s✓ %s %s%s`, b.theme.ColorTag(hex), b.robot(), b.displayName, b.theme.ResetTag())
	case Error:
		header = fmt.Sprintf(`  %s✗ %s %s%s`, b.theme.ColorTag(hex), b.robot(), b.displayName, b.theme.ResetTag())
	}
	bld.WriteString(header)
	fill := max(2, width-colors.TaggedStringWidth(header)-2)
	bld.WriteString(fmt.Sprintf(`%s %s%s`, b.theme.DimTag(), strings.Repeat("─", fill), b.theme.ResetTag()))
	bld.WriteString("\n")

	if b.specialty != "" {
		bld.WriteString(fmt.Sprintf(`  %s│%s Specialty: %s`, b.theme.DimTag(), b.theme.ResetTag(), b.specialty))
		bld.WriteString("\n")
	}
	bld.WriteString(fmt.Sprintf(`  %s│%s Task:      %s`, b.theme.DimTag(), b.theme.ResetTag(), b.task))
	bld.WriteString("\n")
	if b.model != "" {
		bld.WriteString(fmt.Sprintf(`  %s│%s Model:     %s`, b.theme.DimTag(), b.theme.ResetTag(), b.model))
		bld.WriteString("\n")
	}

	if b.state != Active {
		bld.WriteString(fmt.Sprintf(`  %s│%s Duration:  %s`, b.theme.DimTag(), b.theme.ResetTag(), b.durationStr()))
		bld.WriteString("\n")
	}

	if b.state == Error && b.err != "" {
		bld.WriteString(fmt.Sprintf(`  %s│%s %s`, b.theme.DimTag(), b.theme.ResetTag(), b.theme.Tag(b.theme.Error, b.err)))
		bld.WriteString("\n")
	}

	bld.WriteString(fmt.Sprintf(`  %s╰%s%s`, b.theme.DimTag(), strings.Repeat("─", width), b.theme.ResetTag()))
	return bld.String()
}
