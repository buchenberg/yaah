package tui

import "testing"

func TestEstimateCost_ExactMatch(t *testing.T) {
	cost := estimateCost("gpt-4o", 1_000_000, 1_000_000)
	if cost == "$0.00 (unknown model)" {
		t.Errorf("Expected cost for gpt-4o, got unknown model")
	}
	if cost == "" {
		t.Errorf("Expected non-empty cost for gpt-4o")
	}
}

func TestEstimateCost_UnknownModel(t *testing.T) {
	cost := estimateCost("nonexistent-model", 100, 50)
	expected := "$0.00 (unknown model)"
	if cost != expected {
		t.Errorf("Expected %q for unknown model, got %q", expected, cost)
	}
}

func TestEstimateCost_PrefixMatch(t *testing.T) {
	// "gpt-4-turbo" should be preferred over "gpt-4" via longest-prefix match.
	cost := estimateCost("gpt-4-turbo-20250125", 100, 50)
	if cost == "$0.00 (unknown model)" {
		t.Errorf("Expected cost for gpt-4-turbo-20250125 via prefix match")
	}
}

func TestEstimateCost_ZeroTokens(t *testing.T) {
	cost := estimateCost("gpt-4o", 0, 0)
	if cost != "$0.0000" {
		t.Errorf("Expected $0.0000 for zero tokens, got %q", cost)
	}
}
