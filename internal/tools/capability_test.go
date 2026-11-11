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
		if p.Effects&(EffectMutate|EffectExec|EffectNetwork) != 0 && p.Danger == DangerPolicyNone {
			t.Errorf("%s mutates, executes or reaches the network but declares no danger policy", name)
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
		{"role edit", &RoleTool{}, `{"action":"edit"}`, true},
		{"role delete", &RoleTool{}, `{"action":"delete"}`, true},
		{"role list", &RoleTool{}, `{"action":"list"}`, false},
		{"role show", &RoleTool{}, `{"action":"show"}`, false},
		{"role empty defaults to list", &RoleTool{}, `{}`, false},
		{"skill create", &SkillTool{}, `{"action":"create"}`, true},
		{"skill edit", &SkillTool{}, `{"action":"edit"}`, true},
		{"skill delete", &SkillTool{}, `{"action":"delete"}`, true},
		{"skill load", &SkillTool{}, `{"action":"load"}`, false},
		{"skill search", &SkillTool{}, `{"action":"search"}`, false},
		{"skill empty defaults to load", &SkillTool{}, `{}`, false},
		{"go_mod tidy", NewGoModTool(), `{"action":"tidy"}`, true},
		{"go_mod add", NewGoModTool(), `{"action":"add"}`, true},
		{"go_mod remove", NewGoModTool(), `{"action":"remove"}`, true},
		{"go_mod upgrade", NewGoModTool(), `{"action":"upgrade"}`, true},
		{"go_mod tidy dry run", NewGoModTool(), `{"action":"tidy","dry_run":true}`, false},
		{"go_mod add dry run", NewGoModTool(), `{"action":"add","dry_run":true}`, false},
		{"go_mod verify", NewGoModTool(), `{"action":"verify"}`, false},
		{"go_mod graph", NewGoModTool(), `{"action":"graph"}`, false},
		{"go_mod why", NewGoModTool(), `{"action":"why"}`, false},
		{"go_mod list", NewGoModTool(), `{"action":"list"}`, false},
		{"bisect start", NewBisectTool(), `{"action":"start"}`, true},
		{"bisect good", NewBisectTool(), `{"action":"good"}`, true},
		{"bisect bad", NewBisectTool(), `{"action":"bad"}`, true},
		{"bisect skip", NewBisectTool(), `{"action":"skip"}`, true},
		{"bisect reset", NewBisectTool(), `{"action":"reset"}`, true},
		{"bisect log", NewBisectTool(), `{"action":"log"}`, false},
		{"proposal apply", &ProposalTool{}, `{"action":"apply"}`, true},
		{"proposal select", &ProposalTool{}, `{"action":"select"}`, true},
		{"proposal release", &ProposalTool{}, `{"action":"release"}`, true},
		{"proposal discard", &ProposalTool{}, `{"action":"discard"}`, true},
		{"proposal continue", &ProposalTool{}, `{"action":"continue"}`, true},
		{"proposal fork", &ProposalTool{}, `{"action":"fork"}`, true},
		{"proposal show", &ProposalTool{}, `{"action":"show"}`, false},
		{"proposal list", &ProposalTool{}, `{"action":"list"}`, false},
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
