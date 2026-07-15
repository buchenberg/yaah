package yaah

import (
	"bytes"
	"strings"
	"testing"
)

func TestIsAllYaahs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"empty", nil, false},
		{"one yaah", []string{"yaah"}, true},
		{"three yaahs", []string{"yaah", "yaah", "yaah"}, true},
		{"mixed prompt", []string{"yaah", "explain", "this"}, false},
		{"uppercase", []string{"YAAH"}, false},
		{"other word", []string{"hello"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAllYaahs(tc.args); got != tc.want {
				t.Errorf("isAllYaahs(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestGoatCelebration_escalates(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	t.Run("level one is modest", func(t *testing.T) {
		out := goatCelebration(1)
		if !strings.Contains(out, "yaah!") {
			t.Errorf("level 1 missing chant\ngot:\n%s", out)
		}
		if strings.Contains(out, "YAAH!") {
			t.Errorf("level 1 should not shout\ngot:\n%s", out)
		}
	})

	t.Run("levels differ", func(t *testing.T) {
		if goatCelebration(1) == goatCelebration(2) {
			t.Error("levels 1 and 2 produced identical output")
		}
		if goatCelebration(2) == goatCelebration(3) {
			t.Error("levels 2 and 3 produced identical output")
		}
	})

	t.Run("caps at max level", func(t *testing.T) {
		max := goatCelebration(len(goatLevels))
		if got := goatCelebration(len(goatLevels) + 5); got != max {
			t.Errorf("output beyond max level should equal max level\ngot:\n%s\nwant:\n%s", got, max)
		}
		if !strings.Contains(max, "YAAH!") {
			t.Errorf("max level missing shouted chant\ngot:\n%s", max)
		}
	})
}

func TestGoat_rootCommand(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("YAAH_HOME", t.TempDir())

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	defer rootCmd.ResetFlags()

	rootCmd.SetArgs([]string{"yaah", "yaah"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "yaah! yaah!") {
		t.Errorf("root command missing level 2 chant\ngot:\n%s", output)
	}
	if !strings.Contains(output, "_))") {
		t.Errorf("root command missing goat art\ngot:\n%s", output)
	}
}
