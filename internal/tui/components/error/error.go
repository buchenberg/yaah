// Package error provides the error overlay modal for displaying errors in the TUI.
package error

import (
	"fmt"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/tui/colors"
	"github.com/rivo/tview"
)

// Error represents an error to be displayed in the overlay.
type Error struct {
	Title     string
	Detail    string
	Retryable bool
}

// Manager manages a stack of error modals.
type Manager struct {
	app       *tview.Application
	pages     *tview.Pages
	errors    []Error
	theme     *colors.Theme
	onDismiss func()
	timer     *time.Timer // For auto-dismiss of transient errors
}

// New creates a new error modal manager.
func New(app *tview.Application, pages *tview.Pages, theme *colors.Theme) *Manager {
	return &Manager{
		app:    app,
		pages:  pages,
		theme:  theme,
		errors: make([]Error, 0),
	}
}

// Show displays an error modal.
func (m *Manager) Show(err Error) {
	m.errors = append(m.errors, err)
	m.render()
}

// Showf displays an error modal with formatted detail.
func (m *Manager) Showf(title, detail string, retryable bool) {
	m.Show(Error{
		Title:     title,
		Detail:    detail,
		Retryable: retryable,
	})
}

// Dismiss removes the top error from the stack.
func (m *Manager) Dismiss() {
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	if len(m.errors) > 0 {
		m.errors = m.errors[:len(m.errors)-1]
		m.render()
	}
}

// DismissAll removes all errors.
func (m *Manager) DismissAll() {
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	m.errors = make([]Error, 0)
	m.render()
}

// Count returns the number of errors in the stack.
func (m *Manager) Count() int {
	return len(m.errors)
}

// HasErrors returns true if there are errors to display.
func (m *Manager) HasErrors() bool {
	return len(m.errors) > 0
}

// render updates the error overlay display.
func (m *Manager) render() {
	// Remove existing error page if any
	if m.pages.HasPage("error") {
		m.pages.RemovePage("error")
	}

	if len(m.errors) == 0 {
		// Return focus to the main page
		if m.pages.HasPage("main") {
			m.app.SetFocus(m.pages.GetPage("main"))
		}
		return
	}

	// Get the top error
	err := m.errors[len(m.errors)-1]

	// Build the error modal using a Flex layout
	flex := tview.NewFlex().SetDirection(tview.FlexRow)

	// Title
	title := tview.NewTextView()
	title.SetTextAlign(tview.AlignCenter)
	title.SetDynamicColors(true)
	title.SetText(m.theme.TagBold(m.theme.Error, err.Title))
	title.SetBorder(false)

	// Detail
	detail := tview.NewTextView()
	detail.SetDynamicColors(true)
	detail.SetWordWrap(true)
	detail.SetText(m.theme.Tag(m.theme.System, err.Detail))
	detail.SetTextAlign(tview.AlignLeft)
	detail.SetBorder(false)

	// Button row
	buttonRow := tview.NewFlex().SetDirection(tview.FlexRow)

	// Dismiss button (always present)
	dismissBtn := tview.NewButton("[Dismiss]")
	dismissBtn.SetSelectedFunc(func() {
		m.Dismiss()
	})

	buttonRow.AddItem(dismissBtn, 0, 1, false)

	// Retry button (if retryable)
	if err.Retryable {
		retryBtn := tview.NewButton("[Retry]")
		retryBtn.SetSelectedFunc(func() {
			if m.onDismiss != nil {
				m.onDismiss()
			}
			m.Dismiss()
		})
		buttonRow.AddItem(retryBtn, 0, 1, false)
	}

	// Count indicator (if multiple errors)
	if len(m.errors) > 1 {
		countBtn := tview.NewButton(fmt.Sprintf("[%d more errors]", len(m.errors)-1))
		countBtn.SetSelectedFunc(func() {
			m.DismissAll()
		})
		buttonRow.AddItem(countBtn, 0, 1, false)
	}

	// Layout: use the existing flex
	flex.AddItem(title, 1, 0, false)
	flex.AddItem(detail, 0, 1, true)
	flex.AddItem(buttonRow, 1, 0, false)

	// Wrap in a Frame for border
	frame := tview.NewFrame(flex)
	frame.SetBorders(1, 1, 1, 1, 0, 0)
	frame.SetTitle(" Error ")
	frame.SetBorder(true)

	// Add to pages
	m.pages.AddPage("error", frame, true, true)
	m.app.SetFocus(frame)

	// Auto-dismiss after 5 seconds for transient errors.
	// Stop any existing timer first to avoid leaks.
	if m.timer != nil {
		m.timer.Stop()
	}
	if !err.Retryable {
		m.timer = time.AfterFunc(5*time.Second, func() {
			m.app.QueueUpdateDraw(func() {
				if m.HasErrors() {
					m.Dismiss()
				}
			})
		})
	}
}

// SetOnDismiss sets the callback for when errors are dismissed.
func (m *Manager) SetOnDismiss(fn func()) {
	m.onDismiss = fn
}

// FormatError formats an error with title and detail for display.
func FormatError(err error, extraDetail string) Error {
	if err == nil {
		return Error{Title: "No Error", Detail: "No error occurred"}
	}

	// Split error into title (first line) and detail (rest)
	parts := strings.SplitN(err.Error(), "\n", 2)
	title := parts[0]
	detail := ""
	if len(parts) > 1 {
		detail = parts[1]
	}
	if extraDetail != "" {
		if detail != "" {
			detail += "\n\n" + extraDetail
		} else {
			detail = extraDetail
		}
	}

	return Error{
		Title:     title,
		Detail:    detail,
		Retryable: false, // Default to non-retryable
	}
}
