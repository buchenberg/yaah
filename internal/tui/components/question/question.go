// Package question renders interactive question modals in TUI2.
//
// Dispatched from CtrlQuestion events, the modal shows a question header,
// body text, and a list of selectable options. Answers are returned via
// a callback.
package question

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/tui2/components/modal"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Answer contains the user's response to a question.
type Answer struct {
	Selected []string
	Multiple bool
}

// Modal wraps a tview modal for asking structured questions.
type Modal struct {
	app      *tview.Application
	pages    *tview.Pages
	flex     *tview.Flex
	list     *tview.List
	onAnswer func(answer Answer)
	options  []option
	header   string
	question string
	multiple bool
	selected map[int]bool
}

type option struct {
	Label       string
	Description string
}

const modalPageName = "question_modal"

// Show displays the question modal and returns answers via callback.
func Show(app *tview.Application, pages *tview.Pages, header, questionText string, opts []struct{ Label, Description string }, multiple bool, onAnswer func(Answer)) {
	m := &Modal{
		app:      app,
		pages:    pages,
		header:   header,
		question: questionText,
		multiple: multiple,
		onAnswer: onAnswer,
		selected: make(map[int]bool),
	}
	for _, o := range opts {
		m.options = append(m.options, option{Label: o.Label, Description: o.Description})
	}

	m.build()
	pages.AddPage(modalPageName, m.flex, true, true)
	app.SetFocus(m.list)
}

func (m *Modal) build() {
	titleText := fmt.Sprintf("[yellow]%s[-]\n\n%s", m.header, m.question)
	if m.multiple {
		titleText += "\n[dim](Space to select, Enter to confirm)[-]"
	} else {
		titleText += "\n[dim](Enter to select, Esc to cancel)[-]"
	}
	title := tview.NewTextView().
		SetDynamicColors(true).
		SetText(titleText)

	m.list = tview.NewList().
		ShowSecondaryText(true)
	m.list.SetMainTextColor(tcell.ColorWhite).
		SetSecondaryTextColor(tcell.ColorGray).
		SetSelectedTextColor(tcell.ColorWhite).
		SetSelectedBackgroundColor(tcell.ColorDarkCyan)

	for i, o := range m.options {
		idx := i
		label := o.Label
		desc := o.Description
		if m.multiple {
			label = "☐ " + label
		}
		m.list.AddItem(label, desc, 0, func() {
			if m.multiple {
				m.toggle(idx)
			} else {
				m.dismiss([]string{o.Label})
			}
		})
	}

	m.list.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyEscape:
			m.dismiss(nil)
			return nil
		case tcell.KeyEnter:
			if m.multiple {
				m.dismiss(nil)
				return nil
			}
		case tcell.KeyRune:
			if ev.Rune() == ' ' && m.multiple {
				idx := m.list.GetCurrentItem()
				m.toggle(idx)
				return nil
			}
		}
		return ev
	})

	inner := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(title, 0, 1, false).
		AddItem(m.list, 0, 3, true)
	inner.SetBorder(true).
		SetTitle(" Question ").
		SetTitleColor(tcell.ColorYellow)

	m.flex = modal.Wrap(inner)
}

func (m *Modal) toggle(idx int) {
	if m.selected[idx] {
		delete(m.selected, idx)
	} else {
		m.selected[idx] = true
	}
	o := m.options[idx]
	label := "☐ " + o.Label
	if m.selected[idx] {
		label = "☑ " + o.Label
	}
	m.list.SetItemText(idx, label, o.Description)
}

func (m *Modal) dismiss(labels []string) {
	m.pages.RemovePage(modalPageName)
	if m.onAnswer != nil {
		ans := Answer{Multiple: m.multiple}
		if labels != nil {
			ans.Selected = labels
		} else if m.multiple {
			var sel []string
			for i, o := range m.options {
				if m.selected[i] {
					sel = append(sel, o.Label)
				}
			}
			ans.Selected = sel
		}
		m.onAnswer(ans)
	}
}
