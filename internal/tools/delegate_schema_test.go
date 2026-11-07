package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/subagent"
)

// delegationModeFixtures builds role bytes covering the mode vocabulary
// for the delegate-surface filter tests: "primary" (an orchestrator
// persona), "subagent", "all", and a role with no mode key (the
// historical default). The ids are all the same length so the map
// literal needs no gofmt alignment padding.
func delegationModeFixtures() map[string][]byte {
	return map[string][]byte{
		"boss.md": []byte("---\nname: Boss\nmode: primary\ntools: [read]\ndescription: runs the session\n---\nbody"),
		"work.md": []byte("---\nname: Worker\nmode: subagent\ntools: [read]\ndescription: does work\n---\nbody"),
		"flex.md": []byte("---\nname: Flex\nmode: all\ntools: [read]\n---\nbody"),
		"base.md": []byte("---\nname: Legacy\ntools: [read]\n---\nbody"),
	}
}

// schemaRoleEnum extracts the delegate schema's role enum.
func schemaRoleEnum(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var schema struct {
		Properties struct {
			Role struct {
				Enum []string `json:"enum"`
			} `json:"role"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	return schema.Properties.Role.Enum
}

// schemaRoleDescription extracts the delegate schema's role parameter
// description (the rendered role list).
func schemaRoleDescription(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var schema struct {
		Properties struct {
			Role struct {
				Description string `json:"description"`
			} `json:"role"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	return schema.Properties.Role.Description
}

// TestDelegateSchemaExcludesPrimaryOnlyRoles pins yaah-21uk.4: a role
// with mode "primary" is an orchestrator persona and must vanish from
// the delegate tool's dispatch surface — the schema enum, the rendered
// role descriptions, and execution-time validation. "subagent" (explicit
// or the missing-key default) and "all" stay.
func TestDelegateSchemaExcludesPrimaryOnlyRoles(t *testing.T) {
	reg := subagent.NewRoleRegistry()
	if err := reg.LoadBytes(delegationModeFixtures()); err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	subagent.SetDefaultRoleRegistry(reg)
	defer subagent.SetDefaultRoleRegistry(nil)

	names := []string{"boss", "work", "flex", "base"}
	tool := &DelegateTool{
		RoleNames:        names,
		RoleResolver:     func() []string { return names },
		RoleDescriptions: map[string]string{"boss": "runs the session", "work": "does work"},
	}

	t.Run("enum drops primary keeps subagent all and default", func(t *testing.T) {
		enum := schemaRoleEnum(t, tool.Schema())
		if slices.Contains(enum, "boss") {
			t.Errorf("primary-only role leaked into the schema enum: %v", enum)
		}
		for _, want := range []string{"work", "flex", "base"} {
			if !slices.Contains(enum, want) {
				t.Errorf("enum missing delegatable role %q: %v", want, enum)
			}
		}
	})

	t.Run("rendered role descriptions drop primary", func(t *testing.T) {
		desc := schemaRoleDescription(t, tool.Schema())
		if strings.Contains(desc, "boss") {
			t.Errorf("primary-only role leaked into the role description: %q", desc)
		}
		if !strings.Contains(desc, "work: does work") {
			t.Errorf("delegatable role description missing: %q", desc)
		}
	})

	t.Run("execution validation rejects primary", func(t *testing.T) {
		_, err := tool.Execute(context.Background(), `{"description":"d","prompt":"p","role":"boss"}`)
		if err == nil || !IsRoleNotFound(err) {
			t.Fatalf("expected primary-only role to be rejected at dispatch, got %v", err)
		}
	})
}
