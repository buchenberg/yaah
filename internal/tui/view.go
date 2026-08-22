package tui2

import (
	"strings"

	"github.com/buchenberg/yaah/internal/tui2/components/banner"
	"github.com/buchenberg/yaah/internal/tui2/components/infopane"
	"github.com/buchenberg/yaah/internal/tui2/components/input"
	localTodo "github.com/buchenberg/yaah/internal/tui2/components/todo"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// buildUI wires the component tree and layout.
func (t *TUI2) buildUI() {
	var bannerLines int
	bannerLines, t.Banner = banner.Build(t.Theme.Dim)
	t.Messages = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(true).
		SetWordWrap(true)
	t.Messages.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		switch action {
		case tview.MouseScrollUp:
			t.userScrolled = true
		case tview.MouseScrollDown:
			_, _, _, h := t.Messages.GetInnerRect()
			row, _ := t.Messages.GetScrollOffset()
			totalLines := len(strings.Split(t.Messages.GetText(true), "\n"))
			if row+h >= totalLines {
				t.userScrolled = false
			}
		}
		return action, event
	})
	t.Input = input.Build(t.Theme)
	t.InfoPane = infopane.Build(t.Theme.InfoPaneBorder)
	t.TodoPane = localTodo.Build(nil, t.Theme.TasksPaneBorder)
	t.BackgroundJobsPane = tview.NewTextView().
		SetDynamicColors(true).
		SetWordWrap(true)
	t.BackgroundJobsPane.SetBorder(true)
	t.BackgroundJobsPane.SetTitle(" Subagents ")
	t.BackgroundJobsPane.SetBorderColor(tcell.GetColor(t.Theme.SubAgentsBorder))
	t.BackgroundJobsPane.SetTitleColor(tcell.GetColor(t.Theme.SubAgentsBorder))

	headerRows := bannerLines
	totalRows := headerRows
	rows := make([]int, totalRows)
	for i := range rows {
		rows[i] = 1
	}

	t.Header = tview.NewGrid().
		SetRows(rows...).
		SetColumns(-1).
		SetBorders(false)

	t.Header.AddItem(t.Banner, 0, 0, headerRows, 1, 0, 0, false)

	messagesCol := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.Messages, 0, 1, false)

	rightPane := tview.NewFlex().
		SetDirection(tview.FlexRow)
	rightPane.AddItem(t.InfoPane, 0, 3, false)
	rightPane.AddItem(t.BackgroundJobsPane, 0, 0, false)
	rightPane.AddItem(t.TodoPane, 0, 1, false)
	t.rightPane = rightPane

	body := tview.NewFlex().
		SetDirection(tview.FlexColumn).
		AddItem(messagesCol, 0, 4, true).
		AddItem(rightPane, 0, 1, false)

	t.Root = tview.NewFlex().
		SetDirection(tview.FlexRow).
		SetFullScreen(true)

	t.Root.AddItem(t.Header, headerRows, 0, false)
	t.Root.AddItem(body, 0, 1, true)
	t.Root.AddItem(t.Input, 3, 0, false)

	t.Pages = tview.NewPages()
	t.Pages.AddPage("main", t.Root, true, true)
	t.App.SetRoot(t.Pages, true).EnableMouse(true)

	t.App.SetInputCapture(t.globalInputCapture)

	tview.Styles.PrimitiveBackgroundColor = tcell.ColorDefault
	tview.Styles.PrimaryTextColor = tcell.ColorWhite
	tview.Styles.BorderColor = tcell.ColorGray
	tview.Styles.TitleColor = tcell.ColorYellow
	tview.Styles.ContrastBackgroundColor = tcell.ColorDarkCyan
	tview.Styles.MoreContrastBackgroundColor = tcell.ColorDarkCyan
	tview.Styles.ContrastSecondaryTextColor = tcell.ColorWhite
}
