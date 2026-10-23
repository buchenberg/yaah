package yaah

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/todo"
)

// syncBuffer is a mutex-guarded ResponseWriter so a test can read the SSE
// frames that forwardCtrl writes from its own goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	hdr http.Header
	buf bytes.Buffer
}

func newSyncBuffer() *syncBuffer { return &syncBuffer{hdr: http.Header{}} }

func (b *syncBuffer) Header() http.Header { return b.hdr }

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) WriteHeader(int) {}

// Flush satisfies http.Flusher, which sseView.writeLocked checks for.
func (b *syncBuffer) Flush() {}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitForFrame blocks until substr appears in the written frames.
func waitForFrame(t *testing.T, b *syncBuffer, substr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(b.String(), substr) {
		if time.Now().After(deadline) {
			t.Fatalf("frame %q never appeared; frames = %s", substr, b.String())
		}
		time.Sleep(time.Millisecond)
	}
}

// waitAnswerMapEmpty blocks until every registered answer id has been released.
func waitAnswerMapEmpty(t *testing.T, am *answerMap) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		am.mu.Lock()
		n := len(am.pending)
		am.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("answer map still holds %d entries; every id must be released", n)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestWireTodoOf pins the todo wire projection: the item ID must reach the
// browser (the client keys row state off it and only falls back to the array
// index when it is empty) and an empty priority must still be serialized
// (yaah-ljk).
func TestWireTodoOf(t *testing.T) {
	t.Run("carries the item ID", func(t *testing.T) {
		got := wireTodoOf(todo.Item{
			ID: "td-2", Content: "write tests", Status: "in_progress", Priority: "high",
		})
		want := wireTodo{ID: "td-2", Content: "write tests", Status: "in_progress", Priority: "high"}
		if got != want {
			t.Errorf("wireTodoOf = %+v; want %+v", got, want)
		}
	})

	t.Run("serializes the id and an empty priority", func(t *testing.T) {
		frame := string(marshalWire(sseWireEvent{
			Type:  "ctrl.todos",
			Items: []wireTodo{wireTodoOf(todo.Item{ID: "td-1", Content: "c", Status: "pending"})},
		}))
		if !strings.Contains(frame, `"id":"td-1"`) {
			t.Errorf("frame = %s; want the todo id on the wire", frame)
		}
		if !strings.Contains(frame, `"priority":""`) {
			t.Errorf("frame = %s; want an empty priority to survive serialization", frame)
		}
	})
}

// TestForwardCtrlAnswerLifecycle pins the fix for the unbounded answer map
// (yaah-fcg) and for modal overwriting: forwardCtrl registered one id per
// question/approval/continue and only released it when the client answered, so
// a long-running server accumulated an entry per prompt, and a second control
// item emitted while a dialog was open replaced it on screen so the first
// answer was never delivered.
func TestForwardCtrlAnswerLifecycle(t *testing.T) {
	t.Run("answered question releases its id", func(t *testing.T) {
		buf := newSyncBuffer()
		am := newAnswerMap()
		ch := make(chan control.Msg, 4)
		var idGen atomic.Int64
		answers := make(chan string, 1)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go forwardCtrl(ctx, ch, &sseView{w: buf}, am, &idGen)

		ch <- &control.Question{Header: "h", Question: "q", AnswerCh: answers}
		waitForFrame(t, buf, `"id":"q1"`)

		// The id is registered before the frame is written, so reaching the
		// frame guarantees deliver can find it.
		if !am.deliver("q1", "yes") {
			t.Fatal("question id was not registered before the frame was written")
		}
		select {
		case ans := <-answers:
			if ans != "yes" {
				t.Errorf("tool received %q; want %q", ans, "yes")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("answer was never delivered to the question tool")
		}
		waitAnswerMapEmpty(t, am)
	})

	t.Run("torn-down stream releases its id", func(t *testing.T) {
		buf := newSyncBuffer()
		am := newAnswerMap()
		ch := make(chan control.Msg, 4)
		var idGen atomic.Int64
		answers := make(chan string, 1)
		ctx, cancel := context.WithCancel(context.Background())

		go forwardCtrl(ctx, ch, &sseView{w: buf}, am, &idGen)

		ch <- &control.Question{Header: "h", Question: "q", AnswerCh: answers}
		waitForFrame(t, buf, `"id":"q1"`)
		cancel()

		waitAnswerMapEmpty(t, am)
		select {
		case ans := <-answers:
			t.Fatalf("unexpected answer %q: an abandoned prompt must not fabricate one", ans)
		default:
		}
	})

	t.Run("a second question waits for the open modal", func(t *testing.T) {
		buf := newSyncBuffer()
		am := newAnswerMap()
		ch := make(chan control.Msg, 4)
		var idGen atomic.Int64
		first := make(chan string, 1)
		second := make(chan string, 1)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go forwardCtrl(ctx, ch, &sseView{w: buf}, am, &idGen)

		ch <- &control.Question{Header: "one", Question: "first", AnswerCh: first}
		waitForFrame(t, buf, `"first"`)
		ch <- &control.Question{Header: "two", Question: "second", AnswerCh: second}

		// The second modal must wait: emitting it now would overwrite the
		// dialog the user is looking at and lose the first answer.
		deadline := time.Now().Add(100 * time.Millisecond)
		for time.Now().Before(deadline) {
			if strings.Contains(buf.String(), `"second"`) {
				t.Fatal("second modal was emitted while the first was still open")
			}
			time.Sleep(time.Millisecond)
		}

		if !am.deliver("q1", "go") {
			t.Fatal("first question id is not pending")
		}
		waitForFrame(t, buf, `"second"`)
		if !am.deliver("q2", "go") {
			t.Fatal("second question id is not pending")
		}
		waitAnswerMapEmpty(t, am)
	})
}
