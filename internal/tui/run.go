package tui

// Run starts the tview event loop.
func (t *App) Run() error {
	done := t.startBackgroundLoops()
	t.startUIEventLoop(done)
	t.startControlLoop(done)
	t.App.SetFocus(t.Input)
	t.renderInfoPane()
	t.renderTodoPane()
	err := t.App.Run()
	t.stopBackgroundLoops()
	return err
}

// Stop gracefully shuts down the TUI.
func (t *App) Stop() {
	t.stopBackgroundLoops()
	t.App.Stop()
}

func (t *App) startBackgroundLoops() <-chan struct{} {
	t.bgMu.Lock()
	defer t.bgMu.Unlock()
	if t.bgDone != nil {
		close(t.bgDone)
	}
	t.bgDone = make(chan struct{})
	t.uiEventCh = make(chan uiEvent, uiEventQueueSize)
	return t.bgDone
}

func (t *App) stopBackgroundLoops() {
	t.bgMu.Lock()
	defer t.bgMu.Unlock()
	if t.bgDone == nil {
		return
	}
	close(t.bgDone)
	t.bgDone = nil
	t.uiEventCh = nil
}

func (t *App) startControlLoop(done <-chan struct{}) {
	go func() {
		for {
			select {
			case <-done:
				return
			case msg, ok := <-t.ControlCh:
				if !ok {
					return
				}
				t.App.QueueUpdateDraw(func() {
					t.handleControlMsg(msg)
				})
			}
		}
	}()
}
