package assert

import (
	"sort"
	"strings"
	"testing"
)

func TestRegistryAllowlistIsFixed(t *testing.T) {
	names := Names()
	if len(names) == 0 {
		t.Fatal("registry is empty")
	}
	for i := 1; i < len(names); i++ {
		if names[i] <= names[i-1] {
			t.Fatalf("Names not strictly sorted at %d: %q then %q", i, names[i-1], names[i])
		}
	}
	// Every registered name must resolve back to a spec with a runnable check.
	for _, name := range names {
		spec, ok := Lookup(name)
		if !ok {
			t.Fatalf("Names reports %q but Lookup misses it", name)
		}
		if spec.Name != name {
			t.Errorf("Lookup(%q).Name = %q", name, spec.Name)
		}
		if spec.Check == nil {
			t.Errorf("check %q has no function", name)
		}
	}
	// Names returns a copy: mutating the returned slice must not touch the
	// package invariant.
	got := Names()
	got[0] = "corrupted"
	if Names()[0] == "corrupted" {
		t.Fatal("Names returned the registry's backing slice")
	}
}

func TestProblemDeclarations(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		wantSub string
	}{
		{"", nil, "names no check"},
		{"nope", nil, "not one of the registered checks"},
		{"nonempty", map[string]any{"extra": 1}, `does not read "extra"`},
		{"count_ge", nil, `needs the argument "n"`},
		{"count_ge", map[string]any{"n": "three"}, `"n" must be a number`},
		{"count_ge", map[string]any{"n": 0.5}, "whole number"},
		{"nonempty", map[string]any{"from": ""}, `"from" as a step id, and it must be a non-empty string`},
	}
	for _, c := range cases {
		got := Problem(c.name, c.args)
		if !strings.Contains(got, c.wantSub) {
			t.Errorf("Problem(%q, %v) = %q, want substring %q", c.name, c.args, got, c.wantSub)
		}
	}
	if got := Problem("count_ge", map[string]any{"n": 3}); got != "" {
		t.Errorf("valid declaration refused: %q", got)
	}
	if got := Problem("nonempty", map[string]any{"from": "steps.fan"}); got != "" {
		t.Errorf("valid from reference refused: %q", got)
	}
}

func TestNonempty(t *testing.T) {
	cases := []struct {
		value any
		ok    bool
	}{
		{nil, false},
		{"", false},
		{"   ", false},
		{"text", true},
		{[]any{}, false},
		{[]any{1}, true},
		{[]string{}, false},
		{[]string{"a"}, true},
		{map[string]any{}, false},
		{map[string]any{"k": 1}, true},
		{0, true}, // a delivered zero is present
		{false, true},
	}
	for _, c := range cases {
		ok, reason := nonempty(Input{Value: c.value})
		if ok != c.ok {
			t.Errorf("nonempty(%v) = %v (%q), want %v", c.value, ok, reason, c.ok)
		}
		if ok && reason == "" {
			t.Errorf("nonempty(%v) passed with an empty reason", c.value)
		}
	}
}

func TestAllSucceeded(t *testing.T) {
	cases := []struct {
		value any
		ok    bool
	}{
		{nil, false},
		{[]any{}, false},
		{[]any{map[string]any{"status": "done"}}, true},
		{[]any{map[string]any{"failed": true}}, false},
		{[]any{map[string]any{"status": "error"}}, false},
		{[]any{map[string]any{"state": "failed"}}, false},
		{[]any{map[string]any{"state": "cancelled"}}, false},
		{
			[]any{map[string]any{"status": "done"}, map[string]any{"state": "failed"}},
			false,
		},
		{
			[]any{nil, map[string]any{"status": "done"}},
			false,
		},
	}
	for _, c := range cases {
		if ok, _ := allSucceeded(Input{Value: c.value}); ok != c.ok {
			t.Errorf("allSucceeded(%v) = %v, want %v", c.value, ok, c.ok)
		}
	}
}

func TestCounts(t *testing.T) {
	subject := []any{"a", "b", "c"}
	if ok, _ := countGe(Input{Value: subject, Args: map[string]any{"n": 3}}); !ok {
		t.Error("count_ge 3 over 3 entries should pass")
	}
	if ok, _ := countGe(Input{Value: subject, Args: map[string]any{"n": 4}}); ok {
		t.Error("count_ge 4 over 3 entries should fail")
	}
	if ok, _ := countEq(Input{Value: subject, Args: map[string]any{"n": 3}}); !ok {
		t.Error("count_eq 3 over 3 entries should pass")
	}
	if ok, _ := countEq(Input{Value: subject, Args: map[string]any{"n": 2}}); ok {
		t.Error("count_eq 2 over 3 entries should fail")
	}
}

func TestDiffWithinLimit(t *testing.T) {
	cases := []struct {
		value any
		max   int
		ok    bool
	}{
		{nil, 5, false}, // nothing to inspect
		{[]any{map[string]any{"n": 1}}, 5, false}, // no entry carries a file count
		{[]any{map[string]any{"files": 3}}, 5, true},
		{[]any{map[string]any{"files": 6}}, 5, false},
		{
			[]any{map[string]any{"changed_files": 2}, map[string]any{"diff": map[string]any{"file_count": 9}}},
			5, false,
		},
	}
	for _, c := range cases {
		ok, reason := diffWithinLimit(Input{Value: c.value, Args: map[string]any{"max_files": c.max}})
		if ok != c.ok {
			t.Errorf("diffWithinLimit(%v, max=%d) = %v (%q), want %v", c.value, c.max, ok, reason, c.ok)
		}
	}
}

func TestNamesSortedCopyMatchesRegistry(t *testing.T) {
	var want []string
	for name := range registry {
		want = append(want, name)
	}
	sort.Strings(want)
	if got := Names(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Names() = %v, want %v", got, want)
	}
}
