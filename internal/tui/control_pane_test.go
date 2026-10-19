package tui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// newPaneSim initialises a simulation screen and returns it together with
// the paint helpers bound to it.
func newPaneSim(t *testing.T, width, height int) tcell.SimulationScreen {
	t.Helper()
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatalf("init simulation screen: %v", err)
	}
	t.Cleanup(sim.Fini)
	sim.SetSize(width, height)
	return sim
}

// drawPane draws tv full-size inside a single-row flex on sim and flushes
// the simulated screen so GetContents reports the painted grid.
func drawPane(t *testing.T, sim tcell.SimulationScreen, tv *tview.TextView, width, height int) {
	t.Helper()
	root := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(tv, 0, 1, false)
	root.SetRect(0, 0, width, height)
	root.Draw(sim)
	sim.Show()
}

// paintedRows returns the screen contents as trimmed strings, one per row.
func paintedRows(sim tcell.SimulationScreen, width, height int) []string {
	cells, w, _ := sim.GetContents()
	if w != width {
		width = w
	}
	rows := make([]string, 0, height)
	for y := 0; y < height && y*width < len(cells); y++ {
		var b strings.Builder
		for x := 0; x < width; x++ {
			i := y*width + x
			if i >= len(cells) || len(cells[i].Runes) == 0 {
				b.WriteRune(' ')
				continue
			}
			b.WriteRune(cells[i].Runes[0])
		}
		rows = append(rows, strings.TrimRight(b.String(), " "))
	}
	return rows
}

// countPainted counts how many rows contain at least one non-space rune.
func countPainted(rows []string) int {
	n := 0
	for _, r := range rows {
		if strings.TrimSpace(r) != "" {
			n++
		}
	}
	return n
}

func TestSetPaneText(t *testing.T) {
	t.Run("preserves row, clears column", func(t *testing.T) {
		tv := tview.NewTextView().SetDynamicColors(true).SetWordWrap(true)
		tv.ScrollTo(7, 3)

		setPaneText(tv, "alpha\nbeta\ngamma")

		row, col := tv.GetScrollOffset()
		if row != 7 {
			t.Errorf("row offset: got %d, want 7", row)
		}
		if col != 0 {
			t.Errorf("column offset: got %d, want 0", col)
		}
	})

	t.Run("clamps to shorter content after draw", func(t *testing.T) {
		const width, height = 40, 10
		tv := tview.NewTextView().SetDynamicColors(true).SetWordWrap(true)
		setPaneText(tv, "alpha\nbeta\ngamma")

		// Shrinking the content leaves the stored offset beyond the end of
		// the new text; Draw must clamp it back into range.
		shortText := "only\nlines"
		setPaneText(tv, shortText)

		sim := newPaneSim(t, width, height)
		drawPane(t, sim, tv, width, height)

		row, _ := tv.GetScrollOffset()
		displayLines := strings.Count(shortText, "\n") + 1
		if row < 0 {
			t.Fatalf("row offset after draw: got %d, want >= 0", row)
		}
		if row >= displayLines {
			t.Fatalf("row offset after draw: got %d, want < %d display lines", row, displayLines)
		}

		rows := paintedRows(sim, width, height)
		if painted := countPainted(rows); painted == 0 {
			t.Fatalf("screen is blank after draw; rows: %q", rows)
		}
		if !strings.Contains(strings.Join(rows, "\n"), "only") {
			t.Fatalf("clamp pushed content off-screen; rows: %q", rows)
		}
	})

	t.Run("fresh pane starts at top", func(t *testing.T) {
		tv := tview.NewTextView().SetDynamicColors(true).SetWordWrap(true)

		setPaneText(tv, "alpha\nbeta")

		row, col := tv.GetScrollOffset()
		if row > 0 {
			t.Errorf("row offset on fresh pane: got %d, want <= 0", row)
		}
		if col != 0 {
			t.Errorf("column offset on fresh pane: got %d, want 0", col)
		}
	})
}
