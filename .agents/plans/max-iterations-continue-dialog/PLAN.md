---
name: max-iterations-continue-dialog
description: Add a ContinueAfterMaxIter callback to the agent loop so that when max iterations is reached, a dialog prompts the user to continue instead of showing a hard error. Works across REPL, TUI, TUI2, Web, and ACP.
status: completed
---

## Goal

When the agent hits `MaxLoopCycles` (default 50), instead of showing a hard error that requires typing "continue", show a dialog/question asking the user "Max iterations reached. Continue?" with a Yes/No choice.

## Design

Mirror the existing `ApproveFn` pattern: add a `ContinueAfterMaxIter func() bool` callback on the `Loop` struct. In `runMiddleware`, when max iterations are reached, call this callback before returning the error. If it returns `true`, reset counters and continue. If `false` or nil, return the error as before.

The callback is wired differently per UI:
- **REPL**: Reads a line from stdin
- **TUI/TUI2**: Sends a `CtrlContinue` control message; the TUI shows a modal/dialog
- **Web**: Sends an SSE `ctrl.continue` event; the web view handles it
- **ACP**: Sends through the ACP channel

## Files to modify

1. `internal/types/control.go` — Add `CtrlContinue` type with `MaxIter` and `AnswerCh chan bool`
2. `internal/agent/loop.go` — Add `ContinueAfterMaxIter` field; wrap inner for-loop in outer for-loop in `runMiddleware`
3. `cmd/yaah/build_loop.go` — Wire the callback; REPL stdin prompt vs CtrlContinue based on view mode
4. `internal/tui/events.go` — Handle `CtrlContinue`; show a continue modal
5. `internal/tui/view.go` — Add `continueModal` and `continueMode` to Model; render the dialog
6. `internal/tui/input.go` — Handle yes/no key presses in continue mode
7. `cmd/yaah/web_view.go` — Handle `CtrlContinue`; send SSE `ctrl.continue` event; wire answer channel
8. `cmd/yaah/acp.go` — Handle `CtrlContinue` for ACP protocol

## Steps

1. Add `CtrlContinue` type to `internal/types/control.go`
2. Add `ContinueAfterMaxIter` to `Loop` and modify `runMiddleware` in `loop.go`
3. Wire the callback in `build_loop.go` (REPL stdin vs CtrlContinue)
4. Handle `CtrlContinue` in TUI: modal + key handling
5. Handle `CtrlContinue` in Web: SSE event + answer channel
6. Handle `CtrlContinue` in ACP
7. Test with REPL flow
