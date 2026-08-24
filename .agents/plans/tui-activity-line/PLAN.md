# Plan: TUI activity line — tvxwidgets spinner, state machine, compaction gauge, error dialog

> Status: **Draft — ready for implementation**
> Owner: gbuch
> Branch: `feat/tui-activity-line`
> Skills grounding: `tview-tui-expert`, `tvxwidgets` (both loaded during review; tvxwidgets
> source of truth cloned at `.scratch/repos/tvxwidgets/`)
>
> Review finding that motivated this plan: `thinking.Indicator.Advance()` has **no
> production caller** — the spinner never animates. The indicator is a string baked into
> the Messages buffer, which is also why the append fast path is disabled while it is
> visible and why every thinking↔streaming transition forces a full re-render.

## Goals

1. **Dedicated, always-reserved activity line** — a single fixed row directly above the
   prompt input, rendered by a real widget (`tvxwidgets.Spinner`), never baked into the
   conversation buffer. Animation becomes O(1 glyph) instead of O(conversation).
2. **Dedicated prompt line** — the input is pinned as the top line of the dialog pane and
   never moves. Two dedicated lines total for activity + prompt; zero layout jumps.
3. **Always-on activity indication during a prompt** — from submit until `DoneEvent` the
   spinner is always visible and animating, with an explicit state machine that goes
   beyond Thinking/Reasoning (Responding, Running tool, Sub-agent, Compacting, Awaiting
   approval/input).
4. **`tvxwidgets.ActivityModeGauge`** for context compaction (indeterminate progress).
5. **`tvxwidgets.MessageDialog`** for error display (`control.Error`, `DoneEvent.Error`).
6. **Performance unlock** — remove the `thinkingText` plumbing so the incremental append
   fast path works while the agent is busy, and stop forcing `needsFullRender` on every
   thinking↔streaming transition.

## Non-goals

- The REPL/one-shot stderr spinner (`internal/spinner`) — different transport, stays.
- Approval, question, model-picker, and help modals stay hand-rolled — `MessageDialog`
  is a single-Enter-button info/error modal and cannot express them.
- No new agent event types — all states derive from existing events
  (`internal/agent/events/events.go` is untouched, so the exhaustive switch tests hold).
- No compaction progress plumbing (percentage gauge) — no mid-compaction events exist
  today; noted as future work.
- No theme overhaul; the activity line uses existing `colors.Theme` tags.

---

## Part 0 — Grounding: current state

| Finding | Location |
|---|---|
| Spinner frames never advance in production | `internal/tui/components/thinking/thinking.go:31` (`Advance()` called only from tests) |
| Doc comment references a ticker goroutine that does not exist | `thinking.go:46` |
| Indicator line is baked into Messages `SetText` | `internal/tui/scroll.go:122-125` |
| Append fast path disabled while indicator visible | `scroll.go:85` (`canAppend`) |
| `needsFullRender` forced on every thinking transition | `proxy.go:24,35,106`, `event_queue.go:86-89`, `thinking.go:11,23`, `control.go:80` |
| `messages.Format` carries a `thinkingText` param | `internal/tui/components/messages/messages.go:44` |
| Braille frames duplicated (REPL + TUI) | `internal/spinner/spinner.go:50`, `thinking.go:24` |
| `DoneEvent.Error` is silently dropped by the TUI | `proxy.go:100-128` (field exists at `events.go:121`) |
| `control.Error` renders as a plain log line only | `control.go:71-86` |
| Input is a 3-row bordered TextArea at the bottom | `internal/tui/components/input/input.go`, `view.go:84` |
| tview v0.42.0 has **no** `Box.Show/Hide` — visibility is done via `Flex.ResizeItem(p, 0, 0)` (established pattern in `control.go:131,153`) | verified via `go doc` |

**Concurrency model already in place (kept as-is):** QueueUpdateDraw-only field grouping
(`app.go:43`), bounded `uiEventCh` with drop + critical fallback, seq-counter
coalescing for thinking/context updates.

---

## Part 1 — Target layout

### Before

```
┌─────────────────────────────────────────────┐
│ Banner (header grid)                        │
├──────────────────────────────┬──────────────┤
│                              │ Info pane    │
│ Messages (TextView)          │ Subagents    │
│  … "⠋ Thinking…" line is     │ Todo pane    │
│  baked into this buffer …    │              │
├──────────────────────────────┴──────────────┤
│ ┌ Input (3-row bordered TextArea) ┐         │  ← jumps when indicator appears
└─┴─────────────────────────────────┴─────────┘
```

### After

```
┌─────────────────────────────────────────────┐
│ Banner (header grid)                        │
├──────────────────────────────┬──────────────┤
│                              │ Info pane    │
│ Messages (TextView)          │ Subagents    │
│  (no indicator text, ever)   │ Todo pane    │
├──────────────────────────────┴──────────────┤
│ ⠋ Reasoning · 12s · if we look at…   [▓▓░░] │  ← activity line (1 row, ALWAYS)
├─────────────────────────────────────────────┤
│ ❯ type here…                                │  ← prompt line (1 row, sticks)
└─────────────────────────────────────────────┘
```

Bottom of `Root` becomes **two dedicated rows**:

```go
t.Root.AddItem(t.Header, headerRows, 0, false)
t.Root.AddItem(body, 0, 1, true)
t.Root.AddItem(t.activityLine, 1, 0, false)   // dedicated row 1: activity
t.Root.AddItem(t.prompt, 1, 0, true)          // dedicated row 2: prompt (focus)
```

### Layout decisions (explicit, cheap to flip)

- **D1 — Activity line is above the prompt line.** Reading of "Thinking/Reasoning render
  in the very first line above the prompt component". If the intent was the inverse
  order, it is a two-line swap in `buildUI`.
- **D2 — Prompt becomes a single borderless line** (`❯` glyph + 1-row `TextArea`),
  satisfying "two dedicated lines for the activity indication and for the prompt". The
  `TextArea` keeps cursor movement, selection, placeholder, and `MaxLength`; multi-line
  content scrolls *inside* the single row instead of growing the layout. `t.Input`
  remains the `*tview.TextArea` so `submitInput`/`doClear`/focus code is unchanged.
  Fallback if this UX regresses: restore a 3-row bordered TextArea — the activity line
  and everything else in this plan is unaffected.
- **D3 — The activity row is never removed from the layout.** When idle it renders
  blank (spinner collapsed via `ResizeItem`, gauge collapsed, label empty) or shows the
  ephemeral status toast dimly. Reserving the row permanently is what guarantees the
  prompt never moves.

---

## Part 2 — Activity state machine

New package `internal/tui/components/activity`.

### States

| State | Entered on | Label | Extras |
|---|---|---|---|
| `Idle` | submit-to-done end (`DoneEvent`, `control.Error`, `OnAbort`, `OnStop`) | *(blank, or dim ephemeral toast)* | spinner collapsed |
| `Thinking` | `OnSubmit` (turn start), restore after tool/sub-agent/compaction ends | `Thinking…` | |
| `Reasoning` | `ThinkingEvent` (engine reasoning tokens) | `Reasoning` | trailing preview of the reasoning text (clipped) |
| `Responding` | first `TokenDeltaEvent` of a segment | `Responding` | |
| `Tool` | `ToolStartEvent` | `Running <name>…` | restores previous state on `ToolEndEvent` |
| `SubAgent` | `SubAgentStartEvent` | `Sub-agent <role>` (+ count when >1) | restores when last active sub-agent ends |
| `Compacting` | `CompactionStartedEvent` | `Compacting 12.3K→4.0K…` | **gauge replaces spinner**; restores on `CompactionDoneEvent` |
| `Approving` | `control.Approval` / `control.Continue` modal shown | `Awaiting approval…` | restores when answered |
| `Asking` | `control.Question` modal shown | `Awaiting input…` | restores when answered |

### Transition rules

- A depth-1 **restore stack** (`prev State, prevDetail string`) covers
  Tool/SubAgent/Compacting/Approving/Asking → return to Thinking/Reasoning/Responding.
- `DoneEvent`, `control.Error`, `OnAbort`, `OnStop` always clear to `Idle` and empty the
  stack.
- `ThinkingEvent.Text` is the **full streamed reasoning text**, not a short label
  (verified at `events.go:39-41`). The label shows only a clipped trailing preview
  (last ~60 runes, newlines → spaces, no wrap — the TextView clips at row width).
- Elapsed time: `enteredAt time.Time` per state; the ticker renders `` · 12s`` when
  busy. (Precedent: `subagent.Block.Elapsed()`.)
- `agentActive` (info pane "active") derives from `state != Idle` — keep the field,
  set it in the same place the state changes.

### Core API sketch

```go
package activity

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

// Row is the dedicated activity line: spinner (or gauge) + label.
// Not focusable anywhere (tvxwidgets gauges/spinner have no-op Focus).
type Row struct {
    *tview.Flex
    spinner  *tvxwidgets.Spinner
    gauge    *tvxwidgets.ActivityModeGauge
    label    *tview.TextView // 1 row, wrap OFF, dynamic colors ON
    state    State
    detail   string
    preview  string
    prev     State
    prevDetail string
    enteredAt time.Time
    subAgentN int
}

func NewRow(th *colors.Theme) *Row
func (r *Row) SetState(s State, detail string)   // snapshots prev (unless Idle), stamps enteredAt
func (r *Row) Restore()                          // pop depth-1 stack
func (r *Row) SetPreview(text string)            // clipped reasoning preview
func (r *Row) SetSubAgentCount(n int)
func (r *Row) Pulse() bool                       // advance spinner OR gauge; false when idle
func (r *Row) State() State
func (r *Row) Busy() bool                        // state != Idle
func (r *Row) SetEphemeral(msg string)           // dim toast shown while Idle
```

Internal layout — visibility via the repo's existing `ResizeItem` pattern (no
`Show/Hide` in tview v0.42.0):

```go
r.Flex = tview.NewFlex().
    AddItem(r.spinner, 3, 0, false).   // collapsed to (0,0) when Idle or Compacting
    AddItem(r.gauge, 0, 0, false).     // expanded to (gaugeW, 0) only when Compacting
    AddItem(r.label, 0, 1, false)
```

Label rendering (theme-tagged, single line, clipped by the TextView):

```
[state label][ · 12s][ · <preview>]        when busy
[dim ephemeral toast]                      when idle
```

Spinner style: `tvxwidgets.SpinnerDotsCircling` — the same braille frames as today, so
visual behavior is unchanged.

---

## Part 3 — Animation ticker

One goroutine started in `startBackgroundLoops` (`run.go`), stopped via the existing
`done` channel. Follows the tvxwidgets concurrency rule (mutate + redraw on the app
goroutine via `QueueUpdateDraw`):

```go
func (t *App) startActivityTicker(done <-chan struct{}) {
    go func() {
        tick := time.NewTicker(100 * time.Millisecond)
        defer tick.Stop()
        for {
            select {
            case <-done:
                return
            case <-tick.C:
                t.queueUpdateDraw(func() {   // bounded queue; animation is droppable
                    if t.activityLine.Pulse() {
                        t.activityLine.TickElapsed() // refresh "· Ns" once per second
                    }
                })
            }
        }
    }()
}
```

- Spinner `Pulse()` every tick (100 ms ≈ today's intended cadence); gauge `Pulse()` on
  the same tick (≈ width/10 s per sweep — fine for compaction).
- Dropped ticks under load are acceptable (non-critical queue path) — worst case the
  animation stutters, it never blocks or crashes.
- Cost per tick: tcell diffs cells, so only the spinner glyph and the elapsed-seconds
  cells hit the terminal. The conversation buffer is untouched.

---

## Part 4 — Wiring changes (per file)

### `internal/tui/view.go` (buildUI)
- Build `t.activityLine = activity.NewRow(t.Theme)`; build the new single-line prompt
  (see Part 6); add both rows to `Root` per Part 1.
- `t.Input` still points at the inner `*tview.TextArea`.

### `internal/tui/app.go`
- Replace field `thinkingInd *thinking.Indicator` with `activityLine *activity.Row`.
- Delete `thinking` import; add `activity` import.
- Add exported test hook `ActivityState() activity.State` (used by tests; harmless in prod).

### `internal/tui/thinking.go` → rewrite as `activity_state.go`
- `ShowThinking()` → `setActivity(activity.Thinking, "")` (+ `agentActive = true`).
- `HideThinking()` → `setActivity(activity.Idle, "")` (+ `agentActive = false`).
- Keep the exported `ShowThinking`/`HideThinking` names as thin wrappers so
  `cmd/yaah/tui.go:134,138,155` needs **no** changes.
- No `needsFullRender` anywhere in this file anymore.

### `internal/tui/proxy.go` (HandleEvent)
- `TokenDeltaEvent`: drop the `thinkingInd.Hide()`/`needsFullRender` block; on first
  delta of a segment (`!t.isStreaming.Load()` before set) →
  `setActivity(activity.Responding, "")`.
- `ThinkingEvent`: `queueThinkingUpdate(e.Text)` still coalesces bursts, but
  `runThinkingUpdate` now calls `activityLine.SetState(Reasoning, "")` +
  `SetPreview(label)` — **no `needsFullRender`, no `Show()`**.
- `ToolStartEvent`: drop the hide block; `setActivity(activity.Tool, e.Name)` (skip for
  `spawn_subagent` as today). `ToolEndEvent`: `activityLine.Restore()`.
- `SubAgentStartEvent/EndEvent`: maintain `subAgentN`; state `SubAgent` with role; on
  last end `Restore()`.
- `CompactionStartedEvent`: `setActivity(activity.Compacting, fmt.Sprintf("%0.1fK→%0.1fK", …))`.
  `CompactionDoneEvent`: `Restore()`.
- `DoneEvent`: `setActivity(activity.Idle, "")`; **new:** if `e.Error != ""` show the
  error dialog (Part 7).

### `internal/tui/event_queue.go`
- `runThinkingUpdate`: remove the `thinkingInd.Show()`/`needsFullRender` lines; the
  seq-coalescing machinery itself stays (reasoning text streams in bursts).

### `internal/tui/control.go`
- `Approval`/`Continue`: `setActivity(activity.Approving, "")` on show; `Restore()` in
  the answer callback.
- `Question`: `setActivity(activity.Asking, "")` / `Restore()` likewise.
- `Error`: replace the `thinkingInd.Hide()` block with `setActivity(activity.Idle, "")`;
  keep the conversation log line **and** show the error dialog (Part 7).

### `internal/tui/scroll.go` — the perf payoff
- `canAppend`: delete `&& !t.thinkingInd.Visible()` → fast path now works during the
  whole turn (streaming, tools, reasoning).
- `refreshMessages`: delete the `thinkingText` block (lines 122-125).
- `messages.Format(items, ctx)` — drop the `thinkingText` parameter and the trailing
  indicator write (`messages.go:44,68-70`).

### Deletions after wiring is green
- `internal/tui/components/thinking/` (package + tests — superseded by `activity`).
- `internal/tui/thinking.go` old body (rewritten in place as `activity_state.go`).

---

## Part 5 — Compaction gauge (`ActivityModeGauge`)

- Constructed once in `activity.NewRow`: `tvxwidgets.NewActivityModeGauge()`, border
  off, `SetPgBgColor(tcell.GetColor(th.Theme.Accent))` (or Secondary — pick at
  implementation, keep it theme-driven).
- On entering `Compacting`: expand with
  `ResizeItem(gauge, gaugeWidth, 0)` where `gaugeWidth = min(20, rowWidth/3)` (row
  width from `r.label.GetRect()` after layout or `app.Screen().Size()`); collapse the
  spinner. On leaving: collapse gauge `(0,0)`, restore spinner `(3,0)`.
- `Pulse()` routes to gauge when `Compacting`.
- **Gotchas honored** (from the tvxwidgets skill): gauge/spinner are not focusable —
  always `focus=false` in `AddItem`; mutators are unsynchronized — every mutation
  happens inside `QueueUpdateDraw`.
- Future (not in this plan): if the pipeline ever emits progress, swap to
  `PercentageModeGauge` with `SetMaxValue(beforeTokens)` + `SetValue(before-now)` — the
  Row API isolates this change to one widget.

---

## Part 6 — Prompt line redesign (`components/input`)

New `Prompt` wrapper; `t.Input` remains the `*tview.TextArea`:

```go
type Prompt struct {
    *tview.Flex       // [glyph TextView 2w][TextArea 0,1]
    Area *tview.TextArea
}

func BuildPrompt(th *colors.Theme) *Prompt {
    glyph := tview.NewTextView().SetDynamicColors(true).
        SetText(th.Tag(th.User, "❯ "))   // matches user-message accent
    area := tview.NewTextArea().
        SetPlaceholder("Type a message… (Enter to send · Ctrl+P commands)").
        SetMaxLength(10000)
    // no border; theme text style as today
}
```

- `Root.AddItem(prompt, 1, 0, true)` — one row, focused, **never resized**.
- Key handling is untouched: `ActionSend`/`ActionCancel` in `internal/tui/input.go`
  operate on `t.Input` and the global capture, neither of which changes.
- Placeholder shortened to fit one line.
- Rollback note (D2): reverting is deleting `Prompt` and restoring
  `AddItem(t.Input, 3, 0, true)` + the old bordered `Build`.

---

## Part 7 — Error dialog (`tvxwidgets.MessageDialog`)

New `internal/tui/components/errdialog`:

```go
// Show displays a tvxwidgets error dialog. Note the upstream misspelling:
// tvxwidgets.ErrorDailog (not ErrorDialog).
func Show(app *tview.Application, pages *tview.Pages, title, msg string, onDone func()) {
    d := tvxwidgets.NewMessageDialog(tvxwidgets.ErrorDailog)
    d.SetTitle(title)
    d.SetMessage(clip(msg))            // cap ~30 lines / 1200 chars; dialog auto-centers
    d.SetDoneFunc(func() {
        pages.RemovePage(pageName)
        app.SetFocus(/* restored by caller closure */)
        if onDone != nil { onDone() }
    })
    pages.AddPage(pageName, modal.Wrap(d), false, true) // reuse components/modal sizing
    app.SetFocus(d)
}
```

Call sites:
1. `control.Error` (control.go) — dialog in addition to the existing conversation log
   line (log line remains the durable record).
2. `DoneEvent.Error` (proxy.go) — currently dropped; now surfaced.

Focus bookkeeping: set `t.focus = focusModal` while open, restore `focusNormal` +
`SetFocus(t.Input)` in the done func (same pattern as `ShowHelp`, `modals.go:32-41`).
While an error dialog is open mid-turn the activity line shows `Thinking`/idle as
appropriate — no coupling needed.

---

## Part 8 — Phases

Each phase leaves the build green and is independently revertible. File a bead
(`bd add`) per phase at execution time per repo convention.

### Phase 0 — Dependencies & spike
- `go get github.com/navidys/tvxwidgets@latest` → expect tcell bump v2.8.1 → v2.13.x
  (tvxwidgets baseline: tview v0.42.0 + tcell v2.13.10 — exactly our tview pin).
- `go build ./... && go test ./...` — tcell bump is the risk; run the full suite, not
  just tui packages. Check `internal/tui/colors` theme detection still passes.
- Dead-simple spike (deleted before commit): spinner in a scratch Flex, confirm
  `Pulse()` + `QueueUpdateDraw` animates in a real terminal.

### Phase 1 — `components/activity` package (no wiring)
- Files: `activity.go` (State + Row + label formatting), `activity_test.go`.
- Unit tests: state transitions incl. restore stack, Idle clears stack, preview
  clipping, elapsed formatting, sub-agent counting, label text per state (pure logic).
- Widget smoke test with `tcell.NewSimulationScreen`: draw → assert spinner glyph
  changes across `Pulse()` calls; assert gauge collapsed/expanded via `ResizeItem`.

### Phase 2 — Layout, prompt line, ticker
- `view.go`: new bottom rows; `components/input`: add `BuildPrompt`.
- `app.go`: `activityLine` field; `run.go`: `startActivityTicker` in
  `startBackgroundLoops` lifecycle.
- Manual smoke: `go run . tui` — prompt pinned, no jump when banner hides, spinner
  blank when idle, typing/wrapping/Enter all work.

### Phase 3 — Event & control wiring + perf unlock + deletions
- All changes from Part 4 (proxy, event_queue, control, scroll, messages, thinking→
  activity_state). Delete `components/thinking`.
- Update tests: `proxy_test.go` (assert via `ActivityState()`), `messages_test.go`
  (Format signature), `append_render_test.go` (add case: append fast path **while**
  busy), port any still-relevant `thinking_test.go` cases into `activity_test.go`.
- Verify with OTel: `tui.refresh` spans during a long streaming turn show
  `appended=true` throughout (yaah-jaeger / yaah-dev-loop skill).

### Phase 4 — Compaction gauge
- Part 5. Test: sim-screen draw in `Compacting` state shows gauge cells; `Restore()`
  collapses it.

### Phase 5 — Error dialog
- Part 7. Test: `errdialog` page add/remove; `control.Error` path in
  `approval_test.go`-style async test; `DoneEvent.Error` in `proxy_test.go`.

### Phase 6 — Docs & gates
- Update `docs/tui-components.md` (activity row, prompt line, errdialog) and the
  consumer table in `AGENTS.md` only if it names removed items (it does not).
- Gates: `gofmt -l .` empty, `go vet ./...`, `staticcheck ./...`, `go test ./...`,
  cross-compile matrix from AGENTS.md.
- Manual smoke checklist: submit prompt → Thinking animates → reasoning preview →
  Responding during stream → tool run/restore → approval modal state → compaction
  gauge (trigger via tiny `context_window` config) → Done → Idle blank; abort via
  Ctrl+C mid-turn → Idle; error turn → dialog + log line.

---

## Part 9 — Test matrix

| Test | Type | File |
|---|---|---|
| State machine transitions + restore stack | unit | `components/activity/activity_test.go` |
| Preview clipping, elapsed, sub-agent count | unit | ditto |
| Spinner Pulse advances glyph; gauge toggle | sim-screen | ditto |
| Events → states (Thinking/Responding/Tool/…) | async (existing `time.After` pattern) | `tui/proxy_test.go` |
| Append fast path stays on while busy | async | `tui/append_render_test.go` |
| `messages.Format` new signature | unit | `components/messages/messages_test.go` |
| Error dialog show/dismiss + focus restore | async | `tui/errdialog_test.go` |
| Exhaustive event switches | existing | `internal/agent/events/exhaustive_test.go` (unchanged — no new events) |

---

## Part 10 — Risks & mitigations

| Risk | Mitigation |
|---|---|
| tcell v2.8.1 → v2.13.x bump breaks colors/input | Phase 0 runs the **full** suite before any TUI change; tvxwidgets baseline matches our tview pin exactly |
| Single-line prompt regresses multi-line editing UX | D2 rollback is a 2-line change; `TextArea` still holds full text (Enter sends all rows) |
| No `Show/Hide` in tview v0.42.0 | Everything uses the existing `ResizeItem(p, 0, 0)` collapse pattern (same as TodoPane/SubagentsPane) |
| tvxwidgets focus gotcha (gauges/spinner no-op `Focus`) | Never `focus=true` on spinner/gauge items; only the prompt row takes focus |
| `SetCustomStyle`/`ErrorDailog` API misspellings | Compile-time; the skill notes both — code uses `ErrorDailog` |
| 100 ms `QueueUpdateDraw` under modal/queue saturation | Animation uses the droppable non-critical queue path; a dropped tick only stutters |
| Reasoning text floods the label | Clipped trailing preview, wrap off, single row — TextView clips at width |
| Layout math with gauge width on narrow terminals | `gaugeWidth = min(20, rowWidth/3)`; gauge collapses entirely under ~24 cols |
| Done event races ticker after `Stop()` | Ticker exits on the existing `done` channel; `Pulse` after stop is impossible (queue consumer also stops) |

## Success criteria

1. During any turn the spinner is **always** animating with a correct state label —
   verified by pausing a real provider trace and watching states change
   Thinking→Reasoning→Responding→Tool→Done.
2. `tui.refresh` spans show `appended=true` for the entire turn (no full rebuilds
   except block mutations).
3. Prompt line never moves — terminal resize, banner hide, compaction, modals all
   leave the bottom two rows fixed.
4. Compaction shows the sweeping gauge; errors show the dialog.
5. All gates green; `components/thinking` deleted.
