package memory

import (
	"encoding/json"
	"fmt"

	"github.com/buchenberg/yaah/internal/todo"
)

// SaveTodos persists the todo list as a JSON blob.
func (d *DB) SaveTodos(items []todo.Item) error {
	data, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("marshal todos: %w", err)
	}
	_, err = d.sql.Exec(
		"INSERT OR REPLACE INTO todos (id, data, updated_at) VALUES (1, ?, unixepoch())",
		string(data),
	)
	return err
}

// LoadTodos retrieves the persisted todo list.
func (d *DB) LoadTodos() ([]todo.Item, error) {
	var data string
	err := d.sql.QueryRow("SELECT data FROM todos WHERE id = 1").Scan(&data)
	if err != nil {
		return nil, nil
	}
	var items []todo.Item
	if err := json.Unmarshal([]byte(data), &items); err != nil {
		return nil, fmt.Errorf("unmarshal todos: %w", err)
	}
	return items, nil
}
