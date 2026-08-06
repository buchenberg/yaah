package banner

import (
	"strings"
	"testing"
)

// ---------- LolcatRGB ----------

func TestLolcatRGB(t *testing.T) {
	t.Run("produces_valid_rgb", func(t *testing.T) {
		for i := 0; i < 100; i++ {
			r, g, b := LolcatRGB(i)
			if r < 0 || r > 255 || g < 0 || g > 255 || b < 0 || b > 255 {
				t.Errorf("LolcatRGB(%d) = (%d, %d, %d) — values out of [0,255] range", i, r, g, b)
			}
		}
	})

	t.Run("different_indices_different_colors", func(t *testing.T) {
		r0, g0, b0 := LolcatRGB(0)
		r1, g1, b1 := LolcatRGB(1)
		if r0 == r1 && g0 == g1 && b0 == b1 {
			t.Errorf("LolcatRGB(0) and LolcatRGB(1) produced identical colors — should differ")
		}
	})

	t.Run("deterministic", func(t *testing.T) {
		r1, g1, b1 := LolcatRGB(42)
		r2, g2, b2 := LolcatRGB(42)
		if r1 != r2 || g1 != g2 || b1 != b2 {
			t.Errorf("LolcatRGB is not deterministic")
		}
	})
}

// ---------- Lolcat ----------

func TestLolcatWithNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	result := Lolcat("hello\nworld")
	if strings.Contains(result, "\033[") {
		t.Errorf("Lolcat with NO_COLOR should not contain ANSI escapes; got %q", result)
	}
	if !strings.Contains(result, "hello") || !strings.Contains(result, "world") {
		t.Errorf("Lolcat should contain original text; got %q", result)
	}
}

func TestLolcatWithColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")

	result := Lolcat("ab")
	if !strings.Contains(result, "\033[") {
		t.Errorf("Lolcat without NO_COLOR should contain ANSI escapes; got %q", result)
	}
}

func TestLolcatEmpty(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	result := Lolcat("")
	if result != "\n" {
		t.Errorf("Lolcat empty = %q; want %q", result, "\n")
	}
}

// ---------- GeneratePlain ----------

func TestGeneratePlain(t *testing.T) {
	art, tagline, lineCount := GeneratePlain()
	if art == "" {
		t.Error("GeneratePlain art should not be empty")
	}
	if tagline == "" {
		t.Error("GeneratePlain tagline should not be empty")
	}
	if lineCount < 1 {
		t.Errorf("GeneratePlain lineCount = %d; want >= 1", lineCount)
	}
	// The art should have multiple lines (figlet output)
	if !strings.Contains(art, "\n") {
		t.Errorf("GeneratePlain art should contain multiple lines: %q", art)
	}
	// Should not contain ANSI escapes (plain)
	if strings.Contains(art, "\033[") {
		t.Errorf("GeneratePlain art should not contain ANSI escapes; got %q", art)
	}
}

// ---------- Generate ----------

func TestGenerate(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	result, lineCount := Generate()
	if result == "" {
		t.Error("Generate result should not be empty")
	}
	if lineCount < 1 {
		t.Errorf("Generate lineCount = %d; want >= 1", lineCount)
	}
	// With NO_COLOR, there should be no ANSI escapes
	if strings.Contains(result, "\033[") {
		t.Errorf("Generate with NO_COLOR should not contain ANSI escapes")
	}
	// The result should contain the tagline
	if !strings.Contains(result, "\n") {
		t.Errorf("Generate result should contain newlines from art: %q", result)
	}
}

func TestGenerateWithColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")

	result, lineCount := Generate()
	if result == "" {
		t.Error("Generate result should not be empty")
	}
	if lineCount < 1 {
		t.Errorf("Generate lineCount = %d; want >= 1", lineCount)
	}
	// Without NO_COLOR, the colored path produces ANSI escapes
	if !strings.Contains(result, "\033[") {
		t.Errorf("Generate without NO_COLOR should contain ANSI escapes: %q", result)
	}
}

// ---------- Render ----------

func TestRender(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	result := Render("v1.2.3-test")
	if result == "" {
		t.Error("Render result should not be empty")
	}
	if !strings.Contains(result, "v1.2.3-test") {
		t.Errorf("Render should contain version string; got %q", result)
	}
	// Should start with a newline
	if !strings.HasPrefix(result, "\n") {
		t.Errorf("Render should start with newline; got %q", result)
	}
	// With NO_COLOR, should not contain ANSI escapes
	if strings.Contains(result, "\033[") {
		t.Errorf("Render with NO_COLOR should not contain ANSI escapes: %q", result)
	}
	// Should end with newlines
	if !strings.HasSuffix(result, "\n") {
		t.Errorf("Render should end with newline; got %q", result)
	}
}

func TestRenderWithColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")

	result := Render("v1.2.3-test")
	if result == "" {
		t.Error("Render result should not be empty")
	}
	// Without NO_COLOR, should contain ANSI dim escapes for the version
	if !strings.Contains(result, "\033[2m") {
		t.Errorf("Render without NO_COLOR should contain dim escape for version: %q", result)
	}
	if !strings.Contains(result, "v1.2.3-test") {
		t.Errorf("Render should contain version string; got %q", result)
	}
}
