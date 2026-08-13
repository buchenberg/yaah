package yaah

import (
	"slices"
	"testing"
)

func TestSplitRolesBySupervised(t *testing.T) {
	names := []string{"analyst", "developer", "counter", "reviewer", "tester"}
	supervisedSet := map[string]bool{"developer": true, "tester": true}
	isSupervised := func(name string) bool { return supervisedSet[name] }

	plain, supervised := splitRolesBySupervised(names, isSupervised)

	wantPlain := []string{"analyst", "counter", "reviewer"}
	wantSupervised := []string{"developer", "tester"}

	if len(plain) != len(wantPlain) {
		t.Fatalf("plain = %v, want %v", plain, wantPlain)
	}
	for i, n := range wantPlain {
		if plain[i] != n {
			t.Errorf("plain[%d] = %q, want %q", i, plain[i], n)
		}
	}
	if len(supervised) != len(wantSupervised) {
		t.Fatalf("supervised = %v, want %v", supervised, wantSupervised)
	}
	for i, n := range wantSupervised {
		if supervised[i] != n {
			t.Errorf("supervised[%d] = %q, want %q", i, supervised[i], n)
		}
	}

	// A role must land in exactly one bucket.
	for _, n := range names {
		inPlain := slices.Contains(plain, n)
		inSupervised := slices.Contains(supervised, n)
		if inPlain == inSupervised {
			t.Errorf("role %q in exactly-one-bucket violated: plain=%v supervised=%v", n, inPlain, inSupervised)
		}
	}
}

func TestSplitRolesBySupervised_AllPlainDefault(t *testing.T) {
	// No supervised flags → every role is plain (the default-off model).
	names := []string{"worker", "reviewer"}
	plain, supervised := splitRolesBySupervised(names, func(string) bool { return false })

	if len(supervised) != 0 {
		t.Errorf("supervised = %v, want empty when no role is supervised", supervised)
	}
	if len(plain) != 2 {
		t.Errorf("plain = %v, want both roles", plain)
	}
}

func TestSplitRolesBySupervised_Empty(t *testing.T) {
	plain, supervised := splitRolesBySupervised(nil, func(string) bool { return true })
	if plain != nil || supervised != nil {
		t.Errorf("empty input should yield nil slices, got plain=%v supervised=%v", plain, supervised)
	}
}
