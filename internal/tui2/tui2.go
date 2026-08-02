package tui2

import (
	"github.com/buchenberg/yaah/internal/tui2/components/banner"
	"github.com/buchenberg/yaah/internal/tui2/components/infobar"
	"github.com/buchenberg/yaah/internal/tui2/components/infopane"
	"github.com/buchenberg/yaah/internal/tui2/components/input"
	"github.com/buchenberg/yaah/internal/tui2/components/mcpinfo"
	"github.com/buchenberg/yaah/internal/tui2/components/messages"
	"github.com/buchenberg/yaah/internal/tui2/components/provider"
	"github.com/buchenberg/yaah/internal/tui2/components/separator"
	"github.com/buchenberg/yaah/internal/tui2/components/statusbar"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// TUI2 is the tview-based terminal UI prototype.
type TUI2 struct {
	App  *tview.Application
	Root *tview.Flex

	// Header sub-components
	Banner       *tview.TextView
	BannerSep    *tview.TextView // dim separator rule below the tagline
	ProviderInfo *tview.TextView

	// Body components
	InfoBar  *tview.TextView
	Messages *tview.TextView
	Input    *tview.TextArea
	InfoPane *tview.TextView // right-side info panel

	// Footer
	StatusBar *tview.TextView

	// Layout containers
	Header *tview.Grid

	// State
	McpServers []mcpinfo.Server
	PlanInfo   string
	StatusInfo string
}

// New creates a new TUI2 application.
func New() *TUI2 {
	t := &TUI2{
		App: tview.NewApplication(),
	}
	t.buildUI()

	// Populate sample data for visual testing
	t.populateSampleData()

	return t
}

// Run starts the tview event loop.
func (t *TUI2) Run() error {
	return t.App.Run()
}

// Stop gracefully shuts down the TUI.
func (t *TUI2) Stop() {
	t.App.Stop()
}

// buildUI wires the component tree and layout.
func (t *TUI2) buildUI() {
	// --- Leaf primitives (from sub-packages) ---
	var bannerLines int
	bannerLines, t.Banner = banner.Build()
	t.ProviderInfo = provider.Build()
	t.InfoBar, t.PlanInfo = infobar.Build()
	t.Messages = messages.Build()
	t.Input = input.Build()
	t.InfoPane = infopane.Build()
	t.StatusBar, t.StatusInfo = statusbar.Build()

	// --- Header: two-column grid (banner left | provider right) ---
	headerRows := bannerLines
	if headerRows < 10 {
		headerRows = 10
	}

	// One extra row for the bottom separator
	totalRows := headerRows + 1
	rows := make([]int, totalRows)
	for i := range rows {
		rows[i] = 1
	}

	t.Header = tview.NewGrid().
		SetRows(rows...).
		SetColumns(-2, -1, -1). // -X = proportional (2:1:1 → left 2/3, right 1/3)
		SetBorders(false)
	t.Header.SetBorder(true).
		SetBorderColor(tcell.ColorGray).
		SetTitle(" Header ")

	// Banner spans all content rows in col 0 (left portion of width)
	t.Header.AddItem(t.Banner, 0, 0, headerRows, 1, 0, 0, false)

	// Provider info: cols 1-2, all rows (MCP moved to InfoPane)
	t.Header.AddItem(t.ProviderInfo, 0, 1, headerRows, 2, 0, 0, false)

	// Dim separator rule below the tagline (own grid row, no newlines)
	t.BannerSep = separator.Build()
	t.Header.AddItem(t.BannerSep, headerRows, 0, 1, 3, 0, 0, false)

	// --- Body: horizontal split — messages+input (3/4) | infopane (1/4) ---
	bodyLeft := tview.NewFlex().
		SetDirection(tview.FlexRow)
	bodyLeft.AddItem(t.Messages, 0, 1, false) // messages fills (no focus)
	bodyLeft.AddItem(t.Input, 3, 0, true)      // input fixed 3 (focused)

	body := tview.NewFlex().
		SetDirection(tview.FlexColumn)
	body.AddItem(bodyLeft, 0, 5, true)         // ~85% width, focus → bodyLeft → Input
	body.AddItem(t.InfoPane, 0, 1, false)       // ~15% width

	// --- Overall layout: header-sticky-top / body-fills / footer-sticky-bottom ---
	t.Root = tview.NewFlex().
		SetDirection(tview.FlexRow).
		SetFullScreen(true)

	t.Root.AddItem(t.Header, totalRows, 0, false) // header: dynamic, no grow
	t.Root.AddItem(t.InfoBar, 1, 0, false)          // infobar: fixed 1
	t.Root.AddItem(body, 0, 1, true)                 // body: fills remaining, focus → Input
	t.Root.AddItem(t.StatusBar, 1, 0, false)         // footer: fixed 1

	// --- Application setup ---
	t.App.SetRoot(t.Root, true).
		EnableMouse(true).
		EnablePaste(true)

	// Global keybindings
	t.App.SetInputCapture(t.globalInputCapture)

	// Style the application
	tview.Styles.PrimitiveBackgroundColor = tcell.ColorDefault
	tview.Styles.PrimaryTextColor = tcell.ColorWhite
	tview.Styles.BorderColor = tcell.ColorGray
	tview.Styles.TitleColor = tcell.ColorYellow
}
