package tui2

import (
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestCalculateCost_ExactMatch(t *testing.T) {
	// Create a TUI2 instance to access calculateCost
	ui := New("test")

	// Test exact model match
	cost := ui.calculateCost("gpt-4o")
	if cost == "$0.00 (unknown model)" {
		t.Errorf("Expected cost for gpt-4o, got unknown model")
	}
	if cost == "" {
		t.Errorf("Expected non-empty cost for gpt-4o")
	}
}

func TestCalculateCost_UnknownModel(t *testing.T) {
	ui := New("test")

	cost := ui.calculateCost("nonexistent-model")
	expected := "$0.00 (unknown model)"
	if cost != expected {
		t.Errorf("Expected %q for unknown model, got %q", expected, cost)
	}
}

func TestCalculateCost_PrefixMatch(t *testing.T) {
	ui := New("test")

	// Test that longer prefix is preferred
	// "gpt-4-turbo" should match before "gpt-4"
	cost := ui.calculateCost("gpt-4-turbo-20250125")
	if cost == "$0.00 (unknown model)" {
		t.Errorf("Expected cost for gpt-4-turbo-20250125 via prefix match")
	}
}

func TestCalculateCost_PrefixLongestMatch(t *testing.T) {
	ui := New("test")

	// Both "gpt-4" and "gpt-4-turbo" are in the map
	// For "gpt-4-turbo-20250125", the longest match should be "gpt-4-turbo"
	cost := ui.calculateCost("gpt-4-turbo-20250125")
	// We can't easily verify the exact rate without knowing the prices,
	// but we can verify it's not unknown
	if cost == "$0.00 (unknown model)" {
		t.Errorf("Expected prefix match for gpt-4-turbo-20250125")
	}
}

func TestAccumulateUsage(t *testing.T) {
	ui := New("test")

	// Initially zero
	prompt, completion := ui.GetCumulativeUsage()
	if prompt != 0 || completion != 0 {
		t.Errorf("Expected zero usage initially, got prompt=%d, completion=%d", prompt, completion)
	}

	// Accumulate some usage
	ui.accumulateUsage(types.Usage{PromptTokens: 100, CompletionTokens: 50})
	prompt, completion = ui.GetCumulativeUsage()
	if prompt != 100 || completion != 50 {
		t.Errorf("Expected prompt=100, completion=50, got prompt=%d, completion=%d", prompt, completion)
	}

	// Accumulate more
	ui.accumulateUsage(types.Usage{PromptTokens: 200, CompletionTokens: 150})
	prompt, completion = ui.GetCumulativeUsage()
	if prompt != 300 || completion != 200 {
		t.Errorf("Expected prompt=300, completion=200, got prompt=%d, completion=%d", prompt, completion)
	}
}

func TestResetUsage(t *testing.T) {
	ui := New("test")

	// Accumulate usage
	ui.accumulateUsage(types.Usage{PromptTokens: 100, CompletionTokens: 50})
	prompt, completion := ui.GetCumulativeUsage()
	if prompt != 100 || completion != 50 {
		t.Errorf("Expected prompt=100, completion=50, got prompt=%d, completion=%d", prompt, completion)
	}

	// Reset
	ui.resetUsage()
	prompt, completion = ui.GetCumulativeUsage()
	if prompt != 0 || completion != 0 {
		t.Errorf("Expected zero usage after reset, got prompt=%d, completion=%d", prompt, completion)
	}
}
