package tui

// banner.go — banner toggle and sizing.

func (t *App) toggleBanner() {
	t.showBanner = !t.showBanner
	if t.showBanner {
		t.Root.RemoveItem(t.Header)
		t.Root.AddItem(t.Header, t.headerHeight(), 0, false)
	} else {
		t.Root.RemoveItem(t.Header)
	}
}

func (t *App) headerHeight() int {
	if !t.showBanner {
		return 0
	}
	_, _, _, h := t.Banner.GetInnerRect()
	if h <= 0 {
		return 8
	}
	return h
}
