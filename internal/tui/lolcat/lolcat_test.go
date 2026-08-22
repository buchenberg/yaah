package lolcat

import (
	"strings"
	"testing"
)

func TestRGB_Consistency(t *testing.T) {
	r1, g1, b1 := RGB(0)
	r2, g2, b2 := RGB(0)
	if r1 != r2 || g1 != g2 || b1 != b2 {
		t.Errorf("RGB(0) not deterministic: (%d,%d,%d) vs (%d,%d,%d)", r1, g1, b1, r2, g2, b2)
	}
}

func TestRGB_Range(t *testing.T) {
	for i := 0; i < 1000; i++ {
		r, g, b := RGB(i)
		if r < 0 || r > 255 || g < 0 || g > 255 || b < 0 || b > 255 {
			t.Errorf("RGB(%d) out of range: (%d,%d,%d)", i, r, g, b)
		}
	}
}

func TestRainbow_ProducesTags(t *testing.T) {
	out := Rainbow("hi", 0)
	if !strings.Contains(out, "[#") || !strings.Contains(out, "]") {
		t.Errorf("expected tview color tags, got: %s", out)
	}
}

func TestRainbow_ShiftsWithSeed(t *testing.T) {
	a := Rainbow("abc", 0)
	b := Rainbow("abc", 1)
	if a == b {
		t.Errorf("expected different outputs for seeds 0 and 1")
	}
}

func TestStripTags(t *testing.T) {
	tagged := "[#ff0000]hello[-] [#00ff00]world[-]"
	got := StripTags(tagged)
	if got != "hello world" {
		t.Errorf("StripTags: got %q, want %q", got, "hello world")
	}
}

func TestStripTags_NoTags(t *testing.T) {
	got := StripTags("plain text")
	if got != "plain text" {
		t.Errorf("got %q", got)
	}
}
