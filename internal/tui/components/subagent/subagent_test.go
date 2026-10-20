package subagent

import (
	"strings"
	"testing"
	"time"
)

func TestNew_ActiveState(t *testing.T) {
	b := New("sa-1", "developer", "fix the bug")
	if b.ID() != "sa-1" {
		t.Errorf("ID = %q, want sa-1", b.ID())
	}
	if b.Role() != "developer" {
		t.Errorf("Role = %q, want developer", b.Role())
	}
	if b.S() != Active {
		t.Errorf("new block should be Active, got %v", b.S())
	}
	if b.Task() != "fix the bug" {
		t.Errorf("Task = %q, want 'fix the bug'", b.Task())
	}
	if b.DisplayName() == "" {
		t.Error("DisplayName should not be empty")
	}
}

func TestNew_DisplayName(t *testing.T) {
	b := New("sa-1", "developer", "task")
	name := b.DisplayName()
	if !strings.Contains(name, "developer") && !strings.Contains(name, "Developer") {
		t.Errorf("DisplayName should contain role, got %q", name)
	}
}

func TestComplete(t *testing.T) {
	b := New("sa-1", "developer", "task")
	b.Complete()
	if b.S() != Done {
		t.Errorf("should be Done after Complete, got %v", b.S())
	}
}

func TestFail(t *testing.T) {
	b := New("sa-1", "developer", "task")
	b.Fail()
	if b.S() != Error {
		t.Errorf("should be Error after Fail, got %v", b.S())
	}
}

func TestAdvanceSpinner_Active(t *testing.T) {
	b := New("sa-1", "developer", "task")
	initial := b.spinnerFrame
	b.AdvanceSpinner()
	if b.spinnerFrame == initial {
		t.Error("spinner frame should change on active block")
	}
}

func TestAdvanceSpinner_Done(t *testing.T) {
	b := New("sa-1", "developer", "task")
	b.Complete()
	initial := b.spinnerFrame
	b.AdvanceSpinner()
	if b.spinnerFrame != initial {
		t.Error("spinner frame should not change on done block")
	}
}

func TestSpinnerFrame(t *testing.T) {
	b := New("sa-1", "developer", "task")
	if got := b.SpinnerFrame(); got != spinnerFrames[0] {
		t.Errorf("SpinnerFrame = %q, want %q", got, spinnerFrames[0])
	}
	b.AdvanceSpinner()
	if got := b.SpinnerFrame(); got != spinnerFrames[1] {
		t.Errorf("SpinnerFrame after advance = %q, want %q", got, spinnerFrames[1])
	}
}

func TestElapsed(t *testing.T) {
	b := New("sa-1", "developer", "task")
	b.startTime = time.Now().Add(-1 * time.Second)
	elapsed := b.Elapsed()
	if elapsed < 900*time.Millisecond || elapsed > 1100*time.Millisecond {
		t.Errorf("Elapsed ~1s, got %v", elapsed)
	}
}
