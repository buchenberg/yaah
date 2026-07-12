package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/buchenberg/yaah/internal/memory"
)

// MemorySearchTool searches persistent memory using FTS5.
type MemorySearchTool struct {
	DB *memory.DB
}

func (t *MemorySearchTool) Name() string { return "memory_search" }

func (t *MemorySearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "The search query"},
			"top_k": {"type": "integer", "description": "Maximum number of results (default 10)"}
		},
		"required": ["query"]
	}`)
}

func (t *MemorySearchTool) Execute(ctx context.Context, args string) (string, error) {
	if t.DB == nil {
		return "", fmt.Errorf("memory database not available")
	}

	var params struct {
		Query string `json:"query"`
		TopK  int    `json:"top_k"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("memory_search: invalid arguments: %w", err)
	}
	if params.TopK <= 0 {
		params.TopK = 10
	}

	results, err := t.DB.SearchMemory(params.Query, params.TopK)
	if err != nil {
		return "", fmt.Errorf("memory_search: %w", err)
	}

	if len(results) == 0 {
		return "No matching memories found.", nil
	}

	var output string
	for _, r := range results {
		output += fmt.Sprintf("- %s\n", r.Text)
	}
	return output, nil
}

// MemoryAddTool adds a new memory entry.
type MemoryAddTool struct {
	DB *memory.DB
}

func (t *MemoryAddTool) Name() string { return "memory_add" }

func (t *MemoryAddTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"text": {"type": "string", "description": "The memory note text"},
			"tags": {"type": "string", "description": "Optional JSON array of tags"}
		},
		"required": ["text"]
	}`)
}

func (t *MemoryAddTool) Execute(ctx context.Context, args string) (string, error) {
	if t.DB == nil {
		return "", fmt.Errorf("memory database not available")
	}

	var params struct {
		Text string `json:"text"`
		Tags string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("memory_add: invalid arguments: %w", err)
	}

	entry := memory.Entry{
		ID:        fmt.Sprintf("mem-%d", time.Now().UnixNano()),
		Text:      params.Text,
		Tags:      params.Tags,
		Source:    "agent",
		CreatedAt: time.Now().Unix(),
	}

	if err := t.DB.AddMemory(entry); err != nil {
		return "", fmt.Errorf("memory_add: %w", err)
	}

	return fmt.Sprintf("Memory added: %s", params.Text), nil
}
