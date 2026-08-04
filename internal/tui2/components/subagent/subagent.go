// Package subagent renders stateful sub-agent blocks in the TUI2 message
// stream. A single block is created on SubAgentStartEvent and transitions
// from active (blinking 🤖) to done/error on SubAgentEndEvent.
//
// The robot icon blinks while the sub-agent is running — toggled by
// calling ToggleBlink on a timer and re-rendering the messages view.
package subagent

import (
	"fmt"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/tui2/colors"
)

// Block is a stateful sub-agent block in the message stream.
// Created on start, updated on end. There is no separate start/end component.
type Block struct {
	id           string
	role         string // e.g. "checker"
	displayName  string // e.g. "Chuck (checker)"
	specialty    string // e.g. "Finds and gathers information"
	task         string // the prompt description
	model        string
	state        State
	expanded     bool
	startTime    time.Time
	endTime      time.Time
	err          string
	blinkVisible bool // toggled by external timer for active blink
	spinnerFrame int  // advances on each ticker tick while active
}

// State is the current state of a sub-agent block.
type State int

const (
	Active State = iota
	Done
	Error
)

// New creates a sub-agent block in Active state.
func New(id, role, specialty, task, model string) *Block {
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
	}
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (b *Block) ID() string       { return b.id }
func (b *Block) Role() string     { return b.role }
func (b *Block) S() State         { return b.state }
func (b *Block) IsExpanded() bool { return b.expanded }
func (b *Block) Toggle()          { b.expanded = !b.expanded }
func (b *Block) ToggleBlink()     { b.blinkVisible = !b.blinkVisible }

// AdvanceSpinner advances the spinner frame for active sub-agents.
func (b *Block) AdvanceSpinner() {
	if b.state == Active {
		b.spinnerFrame = (b.spinnerFrame + 1) % len(spinnerFrames)
	}
}

// Complete transitions the block to Done.
func (b *Block) Complete() {
	b.endTime = time.Now()
	b.state = Done
}

// Fail transitions the block to Error.
func (b *Block) Fail(err string) {
	b.endTime = time.Now()
	b.state = Error
	b.err = err
}

// Render returns the full tview-tagged text for the block.
func (b *Block) Render() string {
	if !b.expanded {
		return b.renderCollapsed()
	}
	return b.renderExpanded()
}

func (b *Block) robot() string {
	if b.state != Active {
		return "🤖"
	}
	if b.blinkVisible {
		return "🤖"
	}
	return " " // invisible during blink-off phase (same width)
}

func (b *Block) roleHex() string { return colors.RoleHex(b.role) }

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
		return fmt.Sprintf(`  [%s]%s %s %s[-] [%s]· %s[-]`,
			hex, spinnerFrames[b.spinnerFrame], b.robot(), b.displayName, colors.Dim, b.task)
	case Done:
		return fmt.Sprintf(`  [%s]✓ %s %s[-] [%s]· %s (%s)[-]`,
			hex, b.robot(), b.displayName, colors.Dim, b.task, b.durationStr())
	case Error:
		return fmt.Sprintf(`  [%s]✗ %s %s[-] [red]· %s (%s)[-]`,
			hex, b.robot(), b.displayName, b.task, b.durationStr())
	default:
		return ""
	}
}

func (b *Block) renderExpanded() string {
	hex := b.roleHex()
	width := 58
	var bld strings.Builder

	// Header.
	switch b.state {
	case Active:
		bld.WriteString(fmt.Sprintf(`  [%s]%s %s %s[-]`, hex, spinnerFrames[b.spinnerFrame], b.robot(), b.displayName))
	case Done:
		bld.WriteString(fmt.Sprintf(`  [%s]✓ %s %s[-]`, hex, b.robot(), b.displayName))
	case Error:
		bld.WriteString(fmt.Sprintf(`  [%s]✗ %s %s[-]`, hex, b.robot(), b.displayName))
	}
	fill := max(2, width-len(b.displayName)-2)
	bld.WriteString(fmt.Sprintf(`[%s] %s[-]`, colors.Dim, strings.Repeat("─", fill)))
	bld.WriteString("\n")

	// Details.
	if b.specialty != "" {
		bld.WriteString(fmt.Sprintf(`  [%s]│[-] Specialty: %s`, colors.Dim, b.specialty))
		bld.WriteString("\n")
	}
	bld.WriteString(fmt.Sprintf(`  [%s]│[-] Task:      %s`, colors.Dim, b.task))
	bld.WriteString("\n")
	if b.model != "" {
		bld.WriteString(fmt.Sprintf(`  [%s]│[-] Model:     %s`, colors.Dim, b.model))
		bld.WriteString("\n")
	}

	// Duration.
	if b.state != Active {
		bld.WriteString(fmt.Sprintf(`  [%s]│[-] Duration:  %s`, colors.Dim, b.durationStr()))
		bld.WriteString("\n")
	}

	// Error.
	if b.state == Error && b.err != "" {
		bld.WriteString(fmt.Sprintf(`  [%s]│[-] [red]%s[-]`, colors.Dim, b.err))
		bld.WriteString("\n")
	}

	// Footer.
	bld.WriteString(fmt.Sprintf(`  [%s]╰%s[-]`, colors.Dim, strings.Repeat("─", width)))
	return bld.String()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
