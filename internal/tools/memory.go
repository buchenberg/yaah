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
func (t *MemorySearchTool) Description() string {
	return "Searches stored memory notes (user facts, preferences, project details)."
}

func (t *MemorySearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "The search query (empty to list all)"},
			"tag": {"type": "string", "description": "Optional tag to filter by (e.g. 'user_info', 'preferences')"},
			"top_k": {"type": "integer", "description": "Maximum number of results (default 10)"}
		},
		"required": []
	}`)
}

func (t *MemorySearchTool) Execute(ctx context.Context, args string) (string, error) {
	if t.DB == nil {
		return "", fmt.Errorf("memory database not available")
	}

	var params struct {
		Query string `json:"query"`
		Tag   string `json:"tag"`
		TopK  int    `json:"top_k"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("memory_search: invalid arguments: %w", err)
	}
	if params.TopK <= 0 {
		params.TopK = 10
	}

	results, err := t.DB.SearchMemory(params.Query, params.TopK, params.Tag)
	if err != nil {
		return "", fmt.Errorf("memory_search: %w", err)
	}

	if len(results) == 0 {
		return "No matching memories found.", nil
	}

	var output string
	for _, r := range results {
		id := r.ID
		if len(id) > 12 {
			id = id[:12]
		}
		tag := ""
		if r.Tags != "" && r.Tags != "null" {
			tag = " " + r.Tags
		}
		output += fmt.Sprintf("- [%s]%s %s\n", id, tag, r.Text)
	}
	return output, nil
}

// MemoryAddTool adds a new memory entry.
type MemoryAddTool struct {
	DB *memory.DB
}

func (t *MemoryAddTool) Name() string { return "memory_add" }
func (t *MemoryAddTool) Description() string {
	return "Saves a fact, preference, or decision to persistent memory for future recall."
}

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

	dupID, err := t.DB.AddMemoryDedup(entry)
	if err != nil {
		return "", fmt.Errorf("memory_add: %w", err)
	}
	if dupID != "" {
		return fmt.Sprintf("Memory already exists (id: %s): %s\nUse memory_search to retrieve this fact when relevant.", dupID[:12], params.Text), nil
	}
	return fmt.Sprintf("Memory saved (id: %s): %s. This fact is now in persistent memory — use memory_search to recall it when relevant.", entry.ID[:12], params.Text), nil
}

// MemoryDeleteTool removes a memory entry by ID.
type MemoryDeleteTool struct {
	DB *memory.DB
}

func (t *MemoryDeleteTool) Name() string        { return "memory_delete" }
func (t *MemoryDeleteTool) Description() string { return "Deletes a stored memory entry by its ID." }

func (t *MemoryDeleteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "The ID of the memory entry to delete"}
		},
		"required": ["id"]
	}`)
}

func (t *MemoryDeleteTool) Execute(ctx context.Context, args string) (string, error) {
	if t.DB == nil {
		return "", fmt.Errorf("memory database not available")
	}
	var params struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("memory_delete: invalid arguments: %w", err)
	}
	if err := t.DB.DeleteMemory(params.ID); err != nil {
		return "", fmt.Errorf("memory_delete: %w", err)
	}
	return fmt.Sprintf("Memory deleted: %s", params.ID[:min(len(params.ID), 12)]), nil
}

// MemoryUpdateTool updates the text of an existing memory entry.
type MemoryUpdateTool struct {
	DB *memory.DB
}

func (t *MemoryUpdateTool) Name() string { return "memory_update" }
func (t *MemoryUpdateTool) Description() string {
	return "Updates the text of an existing stored memory entry."
}

func (t *MemoryUpdateTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "The ID of the memory entry to update"},
			"text": {"type": "string", "description": "The new text for the memory entry"}
		},
		"required": ["id", "text"]
	}`)
}

func (t *MemoryUpdateTool) Execute(ctx context.Context, args string) (string, error) {
	if t.DB == nil {
		return "", fmt.Errorf("memory database not available")
	}
	var params struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("memory_update: invalid arguments: %w", err)
	}
	if err := t.DB.UpdateMemory(params.ID, params.Text); err != nil {
		return "", fmt.Errorf("memory_update: %w", err)
	}
	return fmt.Sprintf("Memory updated: %s", params.Text), nil
}

// MemorySessionSearchTool searches past session messages using FTS5.
type MemorySessionSearchTool struct {
	DB *memory.DB
}

func (t *MemorySessionSearchTool) Name() string { return "memory_search_sessions" }
func (t *MemorySessionSearchTool) Description() string {
	return "Searches past conversation sessions. With no query, lists recent sessions with their first prompt as a topic summary."
}

func (t *MemorySessionSearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "The search query (empty to list recent messages)"},
			"top_k": {"type": "integer", "description": "Maximum number of results (default 10)"}
		},
		"required": []
	}`)
}

func (t *MemorySessionSearchTool) Execute(ctx context.Context, args string) (string, error) {
	if t.DB == nil {
		return "", fmt.Errorf("session search database not available")
	}

	var params struct {
		Query string `json:"query"`
		TopK  int    `json:"top_k"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("memory_search_sessions: invalid arguments: %w", err)
	}
	if params.TopK <= 0 {
		params.TopK = 10
	}

	if params.Query == "" {
		return t.listRecentMessages(params.TopK)
	}

	results, err := t.DB.SearchMessages(params.Query, params.TopK)
	if err != nil {
		return "", fmt.Errorf("memory_search_sessions: %w", err)
	}

	if len(results) == 0 {
		return "No matching session messages found.", nil
	}

	var output string
	for _, m := range results {
		output += fmt.Sprintf("[%s] %s: %s\n", m.SessionID[:12], m.Role, m.Content)
	}
	return output, nil
}

func (t *MemorySessionSearchTool) listRecentMessages(limit int) (string, error) {
	sessions, err := t.DB.ListSessions(10)
	if err != nil {
		return "", fmt.Errorf("memory_search_sessions: %w", err)
	}
	if len(sessions) == 0 {
		return "No past sessions found.", nil
	}

	var output string
	count := 0
	for _, s := range sessions {
		msgs, err := t.DB.GetMessages(s.ID)
		if err != nil || len(msgs) == 0 {
			continue
		}
		// Find the first user message as a topic indicator.
		topic := ""
		for _, m := range msgs {
			if m.Role == "user" {
				topic = m.Content
				if len(topic) > 120 {
					topic = topic[:117] + "..."
				}
				break
			}
		}
		status := "active"
		if s.EndedAt > 0 {
			status = time.Unix(s.EndedAt, 0).Format("Jan 2 15:04")
		}
		tokenInfo := ""
		if s.TokensIn > 0 || s.TokensOut > 0 {
			tokenInfo = fmt.Sprintf(" | %d in / %d out tokens", s.TokensIn, s.TokensOut)
		}
		output += fmt.Sprintf("[%s] %s | %s | %d msgs | model: %s%s\n  %s\n",
			s.ID[:12], time.Unix(s.StartedAt, 0).Format("Jan 2 15:04"), status,
			len(msgs), s.Model, tokenInfo, topic)
		count++
		if count >= limit {
			return output, nil
		}
	}
	if output == "" {
		return "No messages found in recent sessions.", nil
	}
	return output, nil
}
