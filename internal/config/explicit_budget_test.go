package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureExplicitDefaultBudget(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want ExplicitDefaultBudget
	}{
		{"empty document", "", ExplicitDefaultBudget{}},
		{"agents stanza absent", "editor: vi\n", ExplicitDefaultBudget{}},
		{"default stanza absent", "agents:\n  subagent:\n    max_concurrency: 2\n", ExplicitDefaultBudget{}},
		{"neither budget key", "agents:\n  default:\n    model: m\n", ExplicitDefaultBudget{}},
		{"iterations only", "agents:\n  default:\n    max_iterations: 42\n", ExplicitDefaultBudget{MaxIterations: 42}},
		{"turns only", "agents:\n  default:\n    max_turns: 7\n", ExplicitDefaultBudget{MaxTurns: 7}},
		{"both", "agents:\n  default:\n    max_iterations: 42\n    max_turns: 7\n", ExplicitDefaultBudget{MaxIterations: 42, MaxTurns: 7}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := captureExplicitDefaultBudget([]byte(tt.yaml)); got != tt.want {
				t.Errorf("captureExplicitDefaultBudget = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestLoadCapturesExplicitDefaultBudget pins the end-to-end seam: Load
// merges the file into the shipped defaults AND records which budget
// keys were explicit, so persona resolution can tell an explicit
// max_iterations: 50 from the built-in default 50.
func TestLoadCapturesExplicitDefaultBudget(t *testing.T) {
	tests := []struct {
		name         string
		yaml         string
		wantIters    int
		wantTurns    int
		wantExplicit ExplicitDefaultBudget
	}{
		{
			"explicit values survive the merge",
			"agents:\n  default:\n    max_iterations: 42\n    max_turns: 7\n",
			42, 7, ExplicitDefaultBudget{MaxIterations: 42, MaxTurns: 7},
		},
		{
			"unset keys keep the defaults and no explicit record",
			"agents:\n  default:\n    model: m\n",
			50, 0, ExplicitDefaultBudget{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("YAAH_HOME", home)
			if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(tt.yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Agent.Default.MaxLoopCycles != tt.wantIters || cfg.Agent.Default.MaxToolTurns != tt.wantTurns {
				t.Errorf("merged defaults = %d/%d, want %d/%d",
					cfg.Agent.Default.MaxLoopCycles, cfg.Agent.Default.MaxToolTurns, tt.wantIters, tt.wantTurns)
			}
			if cfg.ExplicitDefaultBudget != tt.wantExplicit {
				t.Errorf("ExplicitDefaultBudget = %+v, want %+v", cfg.ExplicitDefaultBudget, tt.wantExplicit)
			}
		})
	}

	t.Run("missing file records nothing explicit", func(t *testing.T) {
		t.Setenv("YAAH_HOME", t.TempDir())
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.ExplicitDefaultBudget != (ExplicitDefaultBudget{}) {
			t.Errorf("ExplicitDefaultBudget = %+v, want zero", cfg.ExplicitDefaultBudget)
		}
	})
}
