package doctor

import (
	"errors"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/config"
)

// TestCheckPipeline_MatchesSourceOfTruth pins the doctor's pipeline
// report to the pipeline package's defaults — the report used to carry
// its own copy of the middleware list that drifted (review B10e).
func TestCheckPipeline_MatchesSourceOfTruth(t *testing.T) {
	check := CheckPipeline(&config.Config{}, nil)
	if check.Status != "OK" {
		t.Fatalf("status = %q, want OK", check.Status)
	}
	for _, name := range pipeline.DefaultPipelineNames() {
		if !strings.Contains(check.Detail, name) {
			t.Errorf("pipeline report %q missing default middleware %q", check.Detail, name)
		}
	}
	if strings.Contains(check.Detail, "staleness") {
		t.Errorf("pipeline report still lists deleted middleware staleness: %q", check.Detail)
	}
}

func TestCheckPipeline_AdditiveEnabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.Middleware.Enabled = []string{"shepherd_trace"}
	check := CheckPipeline(cfg, nil)
	if !strings.Contains(check.Detail, "shepherd_trace") {
		t.Errorf("enabled entry missing from report: %q", check.Detail)
	}
	for _, name := range pipeline.DefaultPipelineNames() {
		if !strings.Contains(check.Detail, name) {
			t.Errorf("default middleware %q dropped when enabled list is set: %q", name, check.Detail)
		}
	}
}

func TestCheckPipeline_Disabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.Middleware.Disabled = []string{"approval"}
	check := CheckPipeline(cfg, nil)
	active, _, _ := strings.Cut(check.Detail, " (disabled")
	if strings.Contains(active, "approval") {
		t.Errorf("disabled middleware still reported active: %q", check.Detail)
	}
	if !strings.Contains(check.Detail, "disabled: approval") {
		t.Errorf("report should mention disabled middleware: %q", check.Detail)
	}
}

func TestCheckPipeline_ConfigError(t *testing.T) {
	check := CheckPipeline(nil, errors.New("boom"))
	if check.Status != "WARN" {
		t.Errorf("status = %q, want WARN", check.Status)
	}
}
