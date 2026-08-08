package modelpicker

import (
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const modalPageName = "modelpicker_modal"

func Show(app *tview.Application, pages *tview.Pages, models []string, providerNames map[string]string, onSelect func(model string), focusAfter tview.Primitive) {
	dismiss := func() {
		pages.RemovePage(modalPageName)
		if focusAfter != nil {
			app.SetFocus(focusAfter)
		}
	}

	list := tview.NewList().
		ShowSecondaryText(true)

	for _, m := range models {
		model := m
		provider := ""
		if idx := strings.Index(model, "/"); idx >= 0 {
			provider = model[:idx]
		}
		if name, ok := providerNames[model]; ok {
			provider = name
		}
		desc := provider
		if desc == "" {
			desc = "model"
		}

		list.AddItem(model, desc, 0, func() {
			dismiss()
			if onSelect != nil {
				onSelect(model)
			}
		})
	}

	list.SetBorder(true).
		SetTitle(" Model Picker ").
		SetTitleColor(tcell.ColorYellow)

	list.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyEscape:
			dismiss()
			return nil
		}
		return ev
	})

	filter := tview.NewInputField().
		SetFieldBackgroundColor(tcell.ColorBlack).
		SetPlaceholder("filter models...").
		SetPlaceholderTextColor(tcell.ColorGray)

	type pair struct {
		model string
		desc  string
	}

	var allItems []pair
	for _, m := range models {
		p := ""
		if idx := strings.Index(m, "/"); idx >= 0 {
			p = m[:idx]
		}
		if name, ok := providerNames[m]; ok {
			p = name
		}
		if p == "" {
			p = "model"
		}
		allItems = append(allItems, pair{m, p})
	}

	filter.SetChangedFunc(func(text string) {
		list.Clear()
		text = strings.ToLower(text)
		for _, item := range allItems {
			if text == "" || strings.Contains(strings.ToLower(item.model), text) || strings.Contains(strings.ToLower(item.desc), text) {
				list.AddItem(item.model, item.desc, 0, func() {
					dismiss()
					if onSelect != nil {
						onSelect(item.model)
					}
				})
			}
		}
	})

	flex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(filter, 1, 0, false).
		AddItem(list, 0, 1, true)

	flex.SetBorder(true).
		SetTitle(" Model Picker ").
		SetTitleColor(tcell.ColorYellow)

	pages.AddPage(modalPageName, flex, true, true)
	app.SetFocus(list)
}
