package yaah

import (
	"reflect"
	"testing"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/config"
)

// TestConfigAgentConfigParity verifies that every field in agent.AgentConfig
// has a corresponding field in config.Defaults with the same name and type.
// This catches drift when a tuning knob is added to one struct but not the
// other — a silent maintenance risk since the conversion in loopBuilder is
// manual.
//
// Exceptions (fields that exist in AgentConfig but not Defaults, or vice
// versa) are listed in the allowedExtra map.
func TestConfigAgentConfigParity(t *testing.T) {
	defaultsType := reflect.TypeOf(config.Defaults{})
	agentCfgType := reflect.TypeOf(agent.AgentConfig{})

	defaultsFields := structFields(defaultsType)
	agentFields := structFields(agentCfgType)

	// Fields in config.Defaults that are handled outside AgentConfig
	// (provider resolution, approval resolution, prompt assembly, etc.).
	allowedMissingInAgent := map[string]bool{
		"SmallModel":           true, // → compact provider resolution
		"Approval":             true, // → WithApprovalMode
		"Provider":             true, // → provider resolution
		"WorkspaceAsk":         true, // → path validator
		"Model":                true, // → WithModel
		"Directives":           true, // → prompt assembly
		"ReasoningProtect":     true, // → AgentConfig.ReasoningProtectTurns (renamed)
		"ShepherdTraceDir":     true, // → session-level shepherd init in wiring
		"SupervisedMaxRetries": true, // → supervised_task tool registration in wiring
		"SupervisedRepoPath":   true, // → supervised_task tool registration in wiring
		"TurnCheckpointMax":    true, // → sub-agent runner turn checkpointer
		"MaxTurnRestores":      true, // → sub-agent runner turn checkpointer
	}

	// Fields in agent.AgentConfig that come from other config sections
	// (not config.Defaults) or have a renamed counterpart in Defaults.
	allowedExtra := map[string]bool{
		"ReasoningProtectTurns": true, // ← Defaults.ReasoningProtect (renamed)
		"JSONMode":              true, // ← AgentConfig.JSONMode (separate config section)
		"QualityGates":          true, // ← AgentConfig.QualityGates (separate config section)
	}

	// Check every Defaults field has a matching AgentConfig field.
	for name, dt := range defaultsFields {
		if allowedMissingInAgent[name] {
			continue
		}
		at, ok := agentFields[name]
		if !ok {
			t.Errorf("config.Defaults.%s (%s) has no matching field in agent.AgentConfig",
				name, dt)
			continue
		}
		if at != dt {
			t.Errorf("type mismatch for %s: config.Defaults=%s, agent.AgentConfig=%s",
				name, dt, at)
		}
	}

	// Check AgentConfig fields that don't exist in Defaults — flag
	// unexpected ones (not in the allowed list) as potential drift.
	for name := range agentFields {
		if _, ok := defaultsFields[name]; ok {
			continue
		}
		if !allowedExtra[name] {
			t.Errorf("agent.AgentConfig.%s has no matching field in config.Defaults (if intentional, add to allowedExtra)",
				name)
		}
	}
}

// structFields returns a map of field name → type string for all exported
// fields of a struct type, including promoted fields from exported embedded
// structs.
func structFields(t reflect.Type) map[string]string {
	m := make(map[string]string)
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			for name, typ := range structFields(f.Type) {
				m[name] = typ
			}
			continue
		}
		m[f.Name] = f.Type.String()
	}
	return m
}

// TestLoopBuilderAgentConfigRoundtrip verifies that setting all fields in
// config.Defaults produces a Loop with matching LoopConfig values via the
// loopBuilder → Build path. This catches silent zero-value drops where a
// field exists in both structs but the conversion code forgets to map it.
func TestLoopBuilderAgentConfigRoundtrip(t *testing.T) {
	cfg := &config.Config{
		Agent: config.AgentConfig{
			Default: config.Defaults{
				MaxLoopCycles:          42,
				MaxToolTurns:           7,
				MaxRetries:             3,
				RetryBackoffSecs:       5,
				ContextWindow:          131072,
				CompactionThreshold:    0.6,
				RawCompactionThreshold: 0.3,
				CompactMaxMessages:     100,
				EstimateFactor:         1.5,
				LoopDetectCount:        7,
				LoopDetectWindow:       14,
				MaxToolConcurrency:     4,
				WrapUpThreshold:        3,
				MaxInlineToolsPerTurn:  8,
				PromptCaching:          true,
				ReasoningProtect:       4,
				ToolResultMaxLines:     300,
				ToolResultMaxBytes:     10240,
				PruneProtectTokens:     3000,
				PruneMinReclaim:        500,
				PruneMinTurns:          2,
			},
		},
	}

	s := &agentSession{cfg: cfg}
	b := s.loopBuilder(nil, "test-model", nil, "", nil, "")
	loop := b.Build(agent.LoopBuildOptions{})

	// Verify every mapped field made it through.
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"MaxLoopCycles", loop.Config.MaxLoopCycles, 42},
		{"MaxToolTurns", loop.Config.MaxToolTurns, 7},
		{"MaxRetries", loop.Config.MaxRetries, 3},
		{"ContextWindow", loop.Config.ContextWindow, 131072},
		{"CompactionThreshold", loop.Config.CompactionThreshold, 0.6},
		{"RawCompactionThreshold", loop.Config.RawCompactionThreshold, 0.3},
		{"CompactMaxMessages", loop.Config.CompactMaxMessages, 100},
		{"EstimateFactor", loop.Config.EstimateFactor, 1.5},
		{"LoopDetectCount", loop.Config.LoopDetectCount, 7},
		{"LoopDetectWindow", loop.Config.LoopDetectWindow, 14},
		{"MaxToolConcurrency", loop.Config.MaxToolConcurrency, 4},
		{"WrapUpThreshold", loop.Config.WrapUpThreshold, 3},
		{"MaxInlineToolsPerTurn", loop.Config.MaxInlineToolsPerTurn, 8},
		{"PromptCaching", loop.Config.PromptCaching, true},
		{"ReasoningProtectTurns", loop.CtxMgr.ReasoningProtectTurns, 4},
		{"ToolResultMaxLines", loop.CtxMgr.ToolResultMaxLines, 300},
		{"ToolResultMaxBytes", loop.CtxMgr.ToolResultMaxBytes, 10240},
		{"PruneProtectTokens", loop.CtxMgr.PruneProtectTokens, 3000},
		{"PruneMinReclaim", loop.CtxMgr.PruneMinReclaim, 500},
		{"PruneMinTurns", loop.CtxMgr.PruneMinTurns, 2},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}
