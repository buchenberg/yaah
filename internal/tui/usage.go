package tui2

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/types"
)

// modelPrices maps model names to pricing per 1M tokens (input, output).
// Prices are in USD and sourced from provider documentation.
// These are estimates only - actual pricing may vary by region, tier, or contract.
var modelPrices = map[string]struct {
	input  float64
	output float64
}{
	// Claude models (Anthropic)
	"claude-sonnet-4-20250514":   {3.00, 15.00},
	"claude-opus-4-20250514":     {15.00, 75.00},
	"claude-3-5-sonnet-20250620": {3.00, 15.00},
	"claude-3-5-haiku-20250620":  {0.80, 3.00},
	"claude-3-opus-20240229":     {15.00, 75.00},
	"claude-3-sonnet-20240229":   {3.00, 15.00},
	"claude-3-haiku-20240307":    {0.25, 1.00},

	// GPT models (OpenAI)
	"gpt-4o":              {2.50, 10.00},
	"gpt-4o-mini":         {0.15, 0.60},
	"gpt-4-turbo":         {1.00, 3.00},
	"gpt-4-turbo-preview": {1.00, 3.00},
	"gpt-4":               {5.00, 15.00},
	"gpt-4-32k":           {6.00, 18.00},
	"gpt-3.5-turbo":       {0.50, 1.50},
	"gpt-3.5-turbo-16k":   {0.75, 2.25},

	// Llama models (Meta)
	"llama-3.1-70b":  {0.59, 2.79},
	"llama-3.1-405b": {5.89, 26.99},
	"llama-3-70b":    {0.59, 2.79},
	"llama-3-8b":     {0.08, 0.16},

	// Mistral models
	"mistral-large": {2.00, 6.00},
	"mistral-small": {0.25, 0.75},

	// Gemini models (Google)
	"gemini-1.5-pro":   {1.25, 5.00},
	"gemini-1.5-flash": {0.35, 1.05},
	"gemini-pro":       {1.25, 5.00},
	"gemini-flash":     {0.35, 1.05},
}

// cumulativeUsage tracks token usage across the entire conversation.
type cumulativeUsage struct {
	promptTokens     int
	completionTokens int
}

func (t *TUI2) accumulateUsage(usage types.Usage) {
	t.cumulativeUsage.promptTokens += usage.PromptTokens
	t.cumulativeUsage.completionTokens += usage.CompletionTokens
}

func (t *TUI2) resetUsage() {
	t.cumulativeUsage = cumulativeUsage{}
}

// calculateCost estimates the cost in USD for the accumulated usage.
// Returns the total cost as a formatted string.
func (t *TUI2) calculateCost(modelName string) string {
	prices, ok := modelPrices[modelName]
	if !ok {
		// Try to find the longest matching prefix.
		// This ensures "gpt-4-turbo" is preferred over "gpt-4" for "gpt-4-turbo-20250125".
		var bestMatch string
		for name := range modelPrices {
			if len(name) > len(modelName) {
				continue
			}
			if modelName[:len(name)] == name && len(name) > len(bestMatch) {
				bestMatch = name
			}
		}
		if bestMatch != "" {
			prices = modelPrices[bestMatch]
			ok = true
		} else {
			return "$0.00 (unknown model)"
		}
	}

	inputCost := float64(t.cumulativeUsage.promptTokens) / 1_000_000.0 * prices.input
	outputCost := float64(t.cumulativeUsage.completionTokens) / 1_000_000.0 * prices.output
	totalCost := inputCost + outputCost

	return fmt.Sprintf("$%.4f", totalCost)
}

// GetCumulativeUsage returns the accumulated usage statistics.
func (t *TUI2) GetCumulativeUsage() (promptTokens, completionTokens int) {
	return t.cumulativeUsage.promptTokens, t.cumulativeUsage.completionTokens
}
