// Package activity renders the dedicated activity line for the TUI:
// a tvxwidgets.Spinner (or ActivityModeGauge during compaction) plus a
// state label, all in a single reserved Flex row that is never removed
// from the layout.
package activity

import (
	"fmt"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/tui/colors"
	"github.com/gdamore/tcell/v2"
	"github.com/navidys/tvxwidgets"
	"github.com/rivo/tview"
)

// State represents the current activity state.
type State int

const (
	Idle State = iota
	Thinking
	Reasoning
	Responding
	Tool
	SubAgent
	Compacting
	Approving
	Asking
)

// overlay reports whether this state sits on top of a base state and
// should be restored when the overlay ends.
func (s State) overlay() bool {
	switch s {
	case Tool, SubAgent, Compacting, Approving, Asking:
		return true
	}
	return false
}

const (
	spinnerW   = 3
	previewLen = 60
)

// Row is the dedicated activity line: spinner (or gauge) + label.
// Not focusable anywhere (tvxwidgets gauges/spinner have no-op Focus).
type Row struct {
	*tview.Flex
	spinner *tvxwidgets.Spinner
	gauge   *tvxwidgets.ActivityModeGauge
	label   *tview.TextView

	state      State
	detail     string
	preview    string
	ephemeral  string
	prev       State
	prevDetail string
	enteredAt  time.Time
	lastLabel  string
	th         *colors.Theme
}

// NewRow creates the activity line widget. The Flex is always present in
// the layout (never removed); when Idle the spinner/gauge are collapsed
// via ResizeItem(p, 0, 0).
func NewRow(th *colors.Theme) *Row {
	sp := tvxwidgets.NewSpinner()
	sp.SetStyle(tvxwidgets.SpinnerDotsCircling)

	gauge := tvxwidgets.NewActivityModeGauge()
	gauge.SetPgBgColor(tcell.GetColor(th.Heading))
	gauge.SetBorder(false)

	lbl := tview.NewTextView().
		SetDynamicColors(true).
		SetWrap(false).
		SetWordWrap(false)
	lbl.SetBackgroundColor(tcell.ColorDefault)

	flex := tview.NewFlex().
		AddItem(sp, spinnerW, 0, false).
		AddItem(gauge, 0, 0, false).
		AddItem(lbl, 0, 1, false)
	flex.SetBackgroundColor(tcell.ColorDefault)

	r := &Row{
		Flex:    flex,
		spinner: sp,
		gauge:   gauge,
		label:   lbl,
		th:      th,
	}
	r.applyChrome()
	return r
}

// SetState transitions to a new state. Overlay states snapshot the
// current state on a depth-1 restore stack; base states replace it.
// Setting Idle clears the stack.
func (r *Row) SetState(s State, detail string) {
	if s == Idle {
		r.prev, r.prevDetail = Idle, ""
		r.preview = ""
		r.ephemeral = ""
	} else if r.state != Idle && r.state.overlay() && s.overlay() {
		// overlay → overlay: replace top, keep existing base prev
	} else {
		r.prev, r.prevDetail = r.state, r.detail
	}
	r.state = s
	r.detail = detail
	r.enteredAt = time.Now()
	if s != Reasoning {
		r.preview = ""
	}
	r.applyChrome()
	r.renderLabel()
}

// Restore pops the depth-1 restore stack. No-op when the current state
// is not an overlay.
func (r *Row) Restore() {
	if !r.state.overlay() {
		return
	}
	s, d := r.prev, r.prevDetail
	r.prev, r.prevDetail = Idle, ""
	r.state = s
	r.detail = d
	r.enteredAt = time.Now()
	if s != Reasoning {
		r.preview = ""
	}
	r.applyChrome()
	r.renderLabel()
}

// SetPreview updates the trailing reasoning text preview (clipped to
// previewLen runes, newlines replaced with spaces). Only rendered when
// the current state is Reasoning.
func (r *Row) SetPreview(text string) {
	runes := []rune(strings.ReplaceAll(text, "\n", " "))
	if len(runes) > previewLen {
		runes = runes[len(runes)-previewLen:]
	}
	r.preview = string(runes)
	if r.state == Reasoning {
		r.renderLabel()
	}
}

// SetEphemeral sets a dim toast shown while Idle. Empty clears it.
func (r *Row) SetEphemeral(msg string) {
	r.ephemeral = msg
	if r.state == Idle {
		r.renderLabel()
	}
}

// Pulse advances the spinner or gauge frame and refreshes the label
// (including elapsed time). Returns false when Idle (nothing to
// animate).
func (r *Row) Pulse() bool {
	if r.state == Idle {
		return false
	}
	if r.state == Compacting {
		r.gauge.Pulse()
		r.applyChrome() // recompute gauge width if layout changed
	} else {
		r.spinner.Pulse()
	}
	r.renderLabel()
	return true
}

// State returns the current activity state.
func (r *Row) State() State { return r.state }

// Busy reports whether the agent is actively working (state != Idle).
func (r *Row) Busy() bool { return r.state != Idle }

// applyChrome shows/hides the spinner and gauge via ResizeItem (the
// repo's established pattern — tview v0.42.0 has no Box.Show/Hide).
func (r *Row) applyChrome() {
	switch r.state {
	case Compacting:
		r.ResizeItem(r.spinner, 0, 0)
		gw := r.gaugeWidth()
		r.ResizeItem(r.gauge, gw, 0)
	case Idle:
		r.ResizeItem(r.spinner, 0, 0)
		r.ResizeItem(r.gauge, 0, 0)
	default:
		r.ResizeItem(r.gauge, 0, 0)
		r.ResizeItem(r.spinner, spinnerW, 0)
	}
}

// gaugeWidth computes the gauge width as min(20, rowWidth/3), with a
// minimum of 4. Returns 0 when the row has not been laid out yet.
func (r *Row) gaugeWidth() int {
	_, _, w, _ := r.GetRect()
	if w <= 0 {
		return 0
	}
	gw := w / 3
	if gw > 20 {
		gw = 20
	}
	if gw < 4 {
		gw = 4
	}
	return gw
}

// renderLabel rebuilds the label text. Only calls SetText when the
// content changed to avoid unnecessary redraws.
func (r *Row) renderLabel() {
	var text string
	switch r.state {
	case Idle:
		if r.ephemeral != "" {
			text = r.th.Tag(r.th.Dim, r.ephemeral)
		}
	default:
		text = r.formatBusyLabel()
	}
	if text != r.lastLabel {
		r.lastLabel = text
		r.label.SetText(text)
	}
}

func (r *Row) formatBusyLabel() string {
	var b strings.Builder

	// State label
	b.WriteString(r.th.Tag(r.th.Heading, r.stateLabel()))

	// Elapsed
	elapsed := time.Since(r.enteredAt).Truncate(time.Second)
	if elapsed > 0 {
		b.WriteString(r.th.Tag(r.th.Dim, " · "+elapsed.String()))
	}

	// Reasoning preview
	if r.state == Reasoning && r.preview != "" {
		b.WriteString(r.th.Tag(r.th.Dim, " · "+r.preview))
	}

	return b.String()
}

func (r *Row) stateLabel() string {
	switch r.state {
	case Thinking:
		return "Thinking…"
	case Reasoning:
		return "Reasoning"
	case Responding:
		return "Responding"
	case Tool:
		if r.detail != "" {
			return "Running " + r.detail + "…"
		}
		return "Running tool…"
	case SubAgent:
		if r.detail != "" {
			return "Sub-agent " + r.detail
		}
		return "Sub-agent"
	case Compacting:
		if r.detail != "" {
			return "Compacting " + r.detail
		}
		return "Compacting…"
	case Approving:
		return "Awaiting approval…"
	case Asking:
		return "Awaiting input…"
	default:
		return ""
	}
}

// FormatSubAgentDetail builds the detail string for the SubAgent state.
func FormatSubAgentDetail(role string, count int) string {
	if count > 1 {
		return fmt.Sprintf("%s ×%d", role, count)
	}
	return role
}
