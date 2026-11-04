package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

// TestRefreshMessages_AppendsNewTailOnly verifies tail-only rendering:
// appending items renders just the new content without disturbing what
// is already painted, and advances the rendered cursor to the whole log.
// A text item's chunk begins with "\n" so it takes the full-render path
// (see refreshMessages); the incremental append branch is covered by
// TestRefreshMessages_ToolChunkAppends and MixedBatch below.
func TestRefreshMessages_AppendsNewTailOnly(t *testing.T) {
	ui := New("test")

	ui.appendMessage("hello there")
	ui.refreshMessages()
	if ui.renderedItems != 1 {
		t.Fatalf("expected renderedItems=1 after first render, got %d", ui.renderedItems)
	}
	before := ui.Messages.GetText(true)
	if !strings.Contains(before, "hello there") {
		t.Fatalf("first render missing user message; got %q", before)
	}

	ui.addAssistantResponse("streamed **response** tail")
	if ui.needsFullRender.Load() {
		t.Fatalf("appending a new item must not force a full rebuild")
	}

	ui.refreshMessages()
	after := ui.Messages.GetText(true)
	if !strings.HasPrefix(after, before) {
		t.Fatalf("append path rewrote existing content:\nbefore=%q\nafter=%q", before, after)
	}
	if !strings.Contains(after, "streamed") || !strings.Contains(after, "response") {
		t.Fatalf("append path dropped new content; got %q", after)
	}
	if ui.renderedItems != 2 {
		t.Fatalf("expected renderedItems=2, got %d", ui.renderedItems)
	}
}

// msgColumn trims a painted row to the messages column, cutting at the
// right pane's border glyph when present.
func msgColumn(row string) string {
	if i := strings.Index(row, "│"); i >= 0 {
		row = row[:i]
	}
	return row
}

// TestRefreshMessages_ResponseChunkHasSeparator verifies the painted output
// of a flushed assistant response: a blank line must separate it from the
// preceding tool line, and its first row must carry no stray leading space.
// This pins the tview incremental-reindex workaround in refreshMessages:
// writing a chunk that begins with "\n" onto the TextView mis-renders the
// separator as a one-space indent until the next full render fixes it.
func TestRefreshMessages_ResponseChunkHasSeparator(t *testing.T) {
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatalf("init simulation screen: %v", err)
	}
	defer sim.Fini()
	sim.SetSize(120, 40)

	ui := New("test")
	ui.Root.SetRect(0, 0, 120, 40)
	ui.Root.Draw(sim)

	paint := func() []string {
		ui.Root.Draw(sim)
		sim.Show()
		cells, w, _ := sim.GetContents()
		rows := make([]string, 0, 40)
		for y := 0; y < 40; y++ {
			var b strings.Builder
			for x := 0; x < w; x++ {
				i := y*w + x
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

	ui.appendMessage("comment on the bead please")
	ui.AddToolStart("1", "bash", "bd comment yaah-95ri.7")
	ui.AddToolEnd("1", "bash · bd comment", "ok")
	ui.refreshMessages()

	ui.tokenMu.Lock()
	ui.pendingTokens.WriteString("Posted to the bead. Here's the complete picture — what the fix looks like and your stopgap.")
	ui.isStreaming.Store(true)
	ui.tokenMu.Unlock()
	if !ui.flushPendingTokens() {
		t.Fatalf("flush did not render the streamed response")
	}
	ui.refreshMessages()
	rows := paint()

	posted := -1
	for i, r := range rows {
		if strings.Contains(r, "Posted to the bead") {
			posted = i
			break
		}
	}
	if posted < 2 {
		t.Fatalf("response row not found in painted output: %v", rows)
	}
	if strings.HasPrefix(msgColumn(rows[posted]), " ") {
		t.Errorf("response first row is visually indented by a stray space: %q", rows[posted])
	}
	if strings.TrimSpace(msgColumn(rows[posted-1])) != "" {
		t.Errorf("no blank line between response and preceding tool line: %q / %q", rows[posted-2], rows[posted-1])
	}
	if !strings.Contains(msgColumn(rows[posted-2]), "✓") {
		t.Errorf("expected tool line above the response separator: %q", rows[posted-2])
	}

	// The layout must be stable: a subsequent full render (forced by the
	// next tool completing) may not shift or re-indent the response.
	ui.AddToolStart("2", "bash", "git status")
	ui.AddToolEnd("2", "bash · git status", "clean")
	ui.refreshMessages()
	rows2 := paint()
	posted2 := -1
	for i, r := range rows2 {
		if strings.Contains(r, "Posted to the bead") {
			posted2 = i
			break
		}
	}
	if posted2 != posted {
		t.Errorf("response shifted between renders: %d -> %d", posted, posted2)
	}
	if posted2 >= 0 && strings.HasPrefix(msgColumn(rows2[posted2]), " ") {
		t.Errorf("full render indented the response: %q", rows2[posted2])
	}
}

// TestRefreshMessages_ToolChunkAppends verifies the tool-line fast path:
// a batch whose first new item is a tool block is appended onto the
// existing buffer — the append branch is proven by fullRenderCount not
// moving (a SetText rebuild would satisfy a prefix assertion anyway,
// since Format reproduces items 0..renderedItems verbatim).
func TestRefreshMessages_ToolChunkAppends(t *testing.T) {
	ui := New("test")

	ui.appendMessage("run something")
	ui.AddToolStart("1", "bash", "ls")
	ui.refreshMessages()
	before := ui.Messages.GetText(true)
	fullRenders := ui.fullRenderCount

	ui.AddToolStart("2", "grep", "pattern")
	if ui.needsFullRender.Load() {
		t.Fatalf("adding a new tool block must not force a full rebuild")
	}
	ui.refreshMessages()

	if ui.fullRenderCount != fullRenders {
		t.Fatalf("tool chunk took the full-render path (%d rebuilds, want %d)", ui.fullRenderCount, fullRenders)
	}
	after := ui.Messages.GetText(true)
	if !strings.HasPrefix(after, before) {
		t.Fatalf("append path rewrote existing content:\nbefore=%q\nafter=%q", before, after)
	}
	if !strings.Contains(after, "grep") {
		t.Fatalf("append path dropped new tool chunk; got %q", after)
	}
	if ui.renderedItems != len(ui.conversationLog) {
		t.Fatalf("expected renderedItems to cover the whole log, got %d of %d",
			ui.renderedItems, len(ui.conversationLog))
	}
}

// TestRefreshMessages_MixedBatchAppendsToolChunk verifies a batch that
// starts with a tool block but contains a later text item (e.g. a tool
// start coalesced with a flush). The painted-rows assertions are the
// real check: the raw buffer cannot see paint-time reindexing, so only
// a simulated-screen draw proves the text item's separator survived the
// tview resumed parse.
func TestRefreshMessages_MixedBatchAppendsToolChunk(t *testing.T) {
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatalf("init simulation screen: %v", err)
	}
	defer sim.Fini()
	sim.SetSize(120, 40)

	ui := New("test")
	ui.Root.SetRect(0, 0, 120, 40)
	// Warm the layout before any render, so the first full render runs at
	// the pane's real width and the drawing below does not trigger tview's
	// width-change reindex (which would fresh-parse instead of continuing).
	ui.Root.Draw(sim)

	ui.appendMessage("run something")
	ui.refreshMessages()
	ui.Root.Draw(sim)
	fullRenders := ui.fullRenderCount

	// Tool added first, then a streamed response flushed, with no
	// refresh in between — one mixed tail batch.
	ui.AddToolStart("1", "bash", "ls")
	ui.tokenMu.Lock()
	ui.pendingTokens.WriteString("the streamed answer")
	ui.isStreaming.Store(true)
	ui.tokenMu.Unlock()
	if !ui.flushPendingTokens() {
		t.Fatalf("flush did not consume pending tokens")
	}
	if ui.needsFullRender.Load() {
		t.Fatalf("mixed batch must not force a full rebuild")
	}
	ui.refreshMessages()
	if ui.fullRenderCount != fullRenders {
		t.Fatalf("mixed batch took the full-render path (%d rebuilds, want %d)", ui.fullRenderCount, fullRenders)
	}

	// Paint through the continuation parse and assert the text item's
	// blank-line separator survived: no leading space on its first row,
	// blank row between it and the tool line above.
	ui.Root.Draw(sim)
	sim.Show()
	cells, w, _ := sim.GetContents()
	rows := make([]string, 0, 40)
	for y := 0; y < 40; y++ {
		var b strings.Builder
		for x := 0; x < w; x++ {
			i := y*w + x
			if i >= len(cells) || len(cells[i].Runes) == 0 {
				b.WriteRune(' ')
				continue
			}
			b.WriteRune(cells[i].Runes[0])
		}
		rows = append(rows, strings.TrimRight(b.String(), " "))
	}

	answer := -1
	for i, r := range rows {
		if strings.Contains(r, "the streamed answer") {
			answer = i
			break
		}
	}
	if answer < 2 {
		t.Fatalf("streamed answer row not painted: %v", rows)
	}
	if strings.HasPrefix(msgColumn(rows[answer]), " ") {
		t.Errorf("mixed batch re-collapsed the separator: %q", rows[answer])
	}
	if strings.TrimSpace(msgColumn(rows[answer-1])) != "" {
		t.Errorf("no blank row between tool line and appended text: %q / %q", rows[answer-2], rows[answer-1])
	}
	if !strings.Contains(msgColumn(rows[answer-2]), "💻") {
		t.Errorf("expected tool line above the separator: %q", rows[answer-2])
	}

	// Buffer-level: append preserved prior content.
	after := ui.Messages.GetText(true)
	if !strings.Contains(after, "run something") {
		t.Fatalf("append path dropped existing content: %q", after)
	}
}

// TestAddToolEnd_ForcesFullRender verifies that mutating an existing
// block in place invalidates the append fast path.
func TestAddToolEnd_ForcesFullRender(t *testing.T) {
	ui := New("test")

	ui.appendMessage("run something")
	ui.refreshMessages()

	ui.AddToolStart("42", "bash", "ls")
	ui.refreshMessages()
	if ui.needsFullRender.Load() {
		t.Fatalf("adding a NEW tool block must not force a full rebuild")
	}

	ui.AddToolEnd("42", "bash", "file output")
	if !ui.needsFullRender.Load() {
		t.Fatalf("completing an EXISTING tool block must force a full rebuild")
	}
}

// TestEnqueueUIEventDirect_SaturatesAtCap verifies that direct fallbacks
// beyond uiMaxDirectFallbacks are dropped and counted instead of piling up
// unbounded blocked goroutines.
func TestEnqueueUIEventDirect_SaturatesAtCap(t *testing.T) {
	ui := New("test")
	ui.bgDone = make(chan struct{})
	for i := 0; i < uiMaxDirectFallbacks; i++ {
		ui.fallbackSem <- struct{}{}
	}

	start := time.Now()
	ui.enqueueUIEventDirect(false, func() {})

	if ui.uiEventFallbackSat.Load() != 1 {
		t.Fatalf("expected fallback saturation counter = 1, got %d", ui.uiEventFallbackSat.Load())
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("saturation path should bail after the critical wait limit, took %v", elapsed)
	}
}

// TestEnqueueUIEventDirect_AbandonsAtShutdown verifies that in-flight
// fallback work is abandoned once the background loops are stopped, so no
// goroutine blocks forever on QueueUpdate of a stopped application.
func TestEnqueueUIEventDirect_AbandonsAtShutdown(t *testing.T) {
	ui := New("test")
	done := make(chan struct{})
	ui.bgDone = done
	close(done)

	ran := false
	ui.enqueueUIEventDirect(false, func() { ran = true })

	time.Sleep(50 * time.Millisecond)
	if ran {
		t.Fatalf("fallback work executed after shutdown")
	}
}

// TestSetCurrentPrompt_EchoWrapsAndTruncates pins the prompt echo
// behavior: short prompts fit on one row, long prompts truncate with
// an ellipsis instead of growing past promptEchoMaxLines.
func TestSetCurrentPrompt_EchoWrapsAndTruncates(t *testing.T) {
	ui := New("test")

	ui.SetCurrentPrompt("short prompt")
	short := ui.promptEcho.GetText(true)
	if !strings.Contains(short, "short prompt") {
		t.Fatalf("echo missing text: %q", short)
	}
	if strings.Contains(short, "…") {
		t.Errorf("short prompt should not truncate: %q", short)
	}

	ui.SetCurrentPrompt(strings.Repeat("x", 500))
	long := ui.promptEcho.GetText(true)
	if !strings.HasSuffix(long, "…") {
		t.Errorf("long prompt should end with ellipsis: %q", long)
	}
	if got := len([]rune(strings.TrimSuffix(long, "…"))); got > 500 {
		t.Errorf("truncated echo unexpectedly long: %d runes", got)
	}
}
