package subagent

import (
	"strings"
	"testing"
	"time"
)

func TestNewBlock(t *testing.T) {
	b := New("id1", "analyst", "Research", "Find info", "gpt-4")
	if b.ID() != "id1" {
		t.Errorf("ID() = %q, want %q", b.ID(), "id1")
	}
	if b.Role() != "analyst" {
		t.Errorf("Role() = %q, want %q", b.Role(), "analyst")
	}
	if b.S() != Active {
		t.Errorf("S() = %v, want %v", b.S(), Active)
	}
	if b.IsExpanded() {
		t.Errorf("IsExpanded() = true, want false for new block")
	}
}

func TestBlockComplete(t *testing.T) {
	b := New("id1", "analyst", "Research", "Find info", "gpt-4")
	b.Complete()
	if b.S() != Done {
		t.Errorf("S() after Complete() = %v, want %v", b.S(), Done)
	}
	if b.endTime.IsZero() {
		t.Errorf("endTime should be set after Complete()")
	}
}

func TestBlockFail(t *testing.T) {
	b := New("id1", "analyst", "Research", "Find info", "gpt-4")
	b.Fail("connection error")
	if b.S() != Error {
		t.Errorf("S() after Fail() = %v, want %v", b.S(), Error)
	}
	if b.err != "connection error" {
		t.Errorf("err = %q, want %q", b.err, "connection error")
	}
}

func TestBlockToggle(t *testing.T) {
	b := New("id1", "analyst", "Research", "Find info", "gpt-4")
	if b.IsExpanded() {
		t.Errorf("IsExpanded() = true, want false for new block")
	}
	b.Toggle()
	if !b.IsExpanded() {
		t.Errorf("IsExpanded() = false after Toggle(), want true")
	}
	b.Toggle()
	if b.IsExpanded() {
		t.Errorf("IsExpanded() = true after second Toggle(), want false")
	}
}

func TestBlockToggleBlink(t *testing.T) {
	b := New("id1", "analyst", "Research", "Find info", "gpt-4")
	if !b.blinkVisible {
		t.Errorf("blinkVisible = false, want true for new Active block")
	}
	b.ToggleBlink()
	if b.blinkVisible {
		t.Errorf("blinkVisible = true after ToggleBlink(), want false")
	}
}

func TestBlockRenderActive(t *testing.T) {
	b := New("id1", "analyst", "Research", "Find info", "gpt-4")
	got := b.Render()
	if !strings.Contains(got, "▶") {
		t.Errorf("Active block render should contain ▶, got %q", got)
	}
	if !strings.Contains(got, "analyst") {
		t.Errorf("Active block render should contain role, got %q", got)
	}
	if !strings.Contains(got, "🤖") {
		t.Errorf("Active block render should contain robot emoji, got %q", got)
	}
}

func TestBlockRenderDone(t *testing.T) {
	b := New("id1", "analyst", "Research", "Find info", "gpt-4")
	b.Complete()
	got := b.Render()
	if !strings.Contains(got, "✓") {
		t.Errorf("Done block render should contain ✓, got %q", got)
	}
	if !strings.Contains(got, "analyst") {
		t.Errorf("Done block render should contain role, got %q", got)
	}
}

func TestBlockRenderError(t *testing.T) {
	b := New("id1", "analyst", "Research", "Find info", "gpt-4")
	b.Fail("something went wrong")
	got := b.Render()
	if !strings.Contains(got, "✗") {
		t.Errorf("Error block render should contain ✗, got %q", got)
	}
	if !strings.Contains(got, "analyst") {
		t.Errorf("Error block render should contain role, got %q", got)
	}
	b.Toggle()
	gotExpanded := b.Render()
	if !strings.Contains(gotExpanded, "something went wrong") {
		t.Errorf("Expanded error block render should contain error message, got %q", gotExpanded)
	}
}

func TestBlockRenderExpanded(t *testing.T) {
	b := New("id1", "analyst", "Research", "Find info", "gpt-4")
	b.Toggle()
	got := b.Render()
	if !strings.Contains(got, "Specialty:") {
		t.Errorf("Expanded block render should contain Specialty, got %q", got)
	}
	if !strings.Contains(got, "Task:") {
		t.Errorf("Expanded block render should contain Task, got %q", got)
	}
	if !strings.Contains(got, "Model:") {
		t.Errorf("Expanded block render should contain Model, got %q", got)
	}
}

func TestBlockDurationStr(t *testing.T) {
	b := New("id1", "analyst", "Research", "Find info", "gpt-4")
	time.Sleep(10 * time.Millisecond)
	b.Complete()
	dur := b.durationStr()
	if !strings.HasSuffix(dur, "s") && !strings.HasSuffix(dur, "ms") {
		t.Errorf("durationStr() should end with s or ms, got %q", dur)
	}
}
