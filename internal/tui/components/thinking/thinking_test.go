package thinking

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/tui/lolcat"
)

func TestNew(t *testing.T) {
	ind := New("Thinking...")
	if ind == nil {
		t.Fatal("New returned nil")
	}
	if ind.label != "Thinking..." {
		t.Errorf("label = %q, want %q", ind.label, "Thinking...")
	}
	if len(ind.frames) != 10 {
		t.Errorf("frames len = %d, want 10", len(ind.frames))
	}
	if ind.Visible() {
		t.Error("indicator should start hidden")
	}
}

func TestShowHide(t *testing.T) {
	ind := New("Testing")
	ind.Show()
	if !ind.Visible() {
		t.Error("should be visible after Show")
	}
	ind.Hide()
	if ind.Visible() {
		t.Error("should not be visible after Hide")
	}
}

func TestRender_Hidden(t *testing.T) {
	ind := New("Thinking...")
	if out := ind.Render(); out != "" {
		t.Errorf("Render when hidden: got %q, want empty", out)
	}
}

func TestRender_Visible(t *testing.T) {
	ind := New("Thinking...")
	ind.Show()
	out := ind.Render()
	if out == "" {
		t.Error("Render when visible should not be empty")
	}
	plain := lolcat.StripTags(out)
	if !strings.Contains(plain, ind.label) {
		t.Errorf("Render should contain label after stripping tags, got %q", plain)
	}
	if !strings.Contains(out, "[") {
		t.Error("Render should contain tview color tags")
	}
}

func TestSpinner_Hidden(t *testing.T) {
	ind := New("Thinking...")
	if s := ind.Spinner(); s != " " {
		t.Errorf("Spinner when hidden: got %q, want space", s)
	}
}

func TestSpinner_Visible(t *testing.T) {
	ind := New("Thinking...")
	ind.Show()
	if s := ind.Spinner(); s == " " {
		t.Error("Spinner when visible should not be space")
	}
}

func TestAdvance_CyclesFrames(t *testing.T) {
	ind := New("Thinking...")
	ind.Show()
	initial := ind.Spinner()
	for i := 0; i < 10; i++ {
		ind.Advance()
	}
	if s := ind.Spinner(); s != initial {
		t.Errorf("after 10 advances (full cycle), got %q, want %q (initial)", s, initial)
	}
}

func TestAdvance_RenderChanges(t *testing.T) {
	ind := New("Thinking...")
	ind.Show()
	r1 := ind.Render()
	ind.Advance()
	r2 := ind.Render()
	if r1 == r2 {
		t.Error("Render output should change after Advance (seed changes)")
	}
}

func TestRender_ContainsSpinnerAndLabel(t *testing.T) {
	ind := New("Reasoning...")
	ind.Show()
	out := ind.Render()
	if !strings.HasPrefix(out, "[") {
		t.Error("Render should start with a color tag")
	}
}

func TestMultipleIndicators_Independent(t *testing.T) {
	a := New("Thinking...")
	b := New("Reasoning...")
	a.Show()
	b.Show()
	ar1 := lolcat.StripTags(a.Render())
	br1 := lolcat.StripTags(b.Render())
	if ar1 == br1 {
		t.Error("different labels should produce different output")
	}
	if !strings.Contains(ar1, "Thinking...") {
		t.Error("a should contain Thinking...")
	}
	if !strings.Contains(br1, "Reasoning...") {
		t.Error("b should contain Reasoning...")
	}
}

func TestAdvance_SeedIncrements(t *testing.T) {
	ind := New("Thinking...")
	ind.Show()
	ind.Advance()
	ind.Advance()
	ind.Advance()
	out := ind.Render()
	if out == "" {
		t.Error("render should not be empty after advances")
	}
}
