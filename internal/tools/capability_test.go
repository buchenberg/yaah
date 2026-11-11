package tools

import "testing"

// capability_test.go is the enforcement for capability.go: a tool added
// without a policy, a mutating/executing tool without a danger declaration, or
// a policy claiming a classifier the tool does not implement fails here.

func TestToolPolicyCoverage(t *testing.T) {
	seen := make(map[string]bool)
	for _, tool := range allBuiltinTools() {
		name := tool.Name()
		if seen[name] {
			continue
		}
		seen[name] = true
		p, ok := PolicyFor(name)
		if !ok {
			t.Errorf("%s has no capability policy; add one to capability.go", name)
			continue
		}
		if p.Effects&(EffectMutate|EffectExec) != 0 && p.Danger == DangerPolicyNone {
			t.Errorf("%s mutates or executes but declares no danger policy", name)
		}
		switch p.Danger {
		case DangerPolicyClassifier:
			if _, ok := tool.(DangerClassifier); !ok {
				t.Errorf("%s declares DangerPolicyClassifier but does not implement DangerClassifier", name)
			}
		case DangerPolicySafe:
			if p.Reason == "" {
				t.Errorf("%s declares DangerPolicySafe without a reason", name)
			}
		case DangerPolicyAlways:
			if name != "bash" && name != "powershell" {
				t.Errorf("%s declares DangerPolicyAlways; only the shell tools are platform-independent gates", name)
			}
		}
	}
	for name := range Policies() {
		if !seen[name] {
			t.Errorf("policy for %q has no registered tool (stale entry)", name)
		}
	}
}

func TestDangerousActionsAreClassified(t *testing.T) {
	cases := []struct {
		name string
		tool Tool
		args string
		want bool
	}{
		{"role create", &RoleTool{}, `{"action":"create"}`, true},
		{"role list", &RoleTool{}, `{"action":"list"}`, false},
		{"skill create", &SkillTool{}, `{"action":"create"}`, true},
		{"skill load", &SkillTool{}, `{"action":"load"}`, false},
		{"go_mod tidy", NewGoModTool(), `{"action":"tidy"}`, true},
		{"go_mod list", NewGoModTool(), `{"action":"list"}`, false},
		{"bisect start", NewBisectTool(), `{"action":"start"}`, true},
		{"bisect log", NewBisectTool(), `{"action":"log"}`, false},
		{"proposal apply", &ProposalTool{}, `{"action":"apply"}`, true},
		{"proposal show", &ProposalTool{}, `{"action":"show"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			classifier, ok := tc.tool.(DangerClassifier)
			if !ok {
				t.Fatalf("%s does not implement DangerClassifier", tc.name)
			}
			if got := classifier.IsDangerous(tc.args); got != tc.want {
				t.Errorf("IsDangerous(%s) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}
