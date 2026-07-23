package agent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/types"
)

// emitHook writes a JSON event line to the hook file if HookDir is set.
// Errors are silently ignored — hook emission is best-effort and a failed
// emission must never break the agent loop.
func (l *Loop) emitHook(event HookEvent) {
	if l.HookDir == "" {
		return
	}
	l.hookOnce.Do(func() {
		if err := os.MkdirAll(l.HookDir, 0o755); err != nil {
			return
		}
		path := filepath.Join(l.HookDir, l.SessionID+".jsonl")
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		l.hookFile = f
		l.hookOK = true
	})
	if !l.hookOK {
		return
	}
	event.SessionID = l.SessionID
	if event.Timestamp == 0 {
		event.Timestamp = time.Now().UnixMilli()
	}
	line, err := json.Marshal(event)
	if err != nil {
		return
	}
	l.hookMu.Lock()
	l.hookFile.Write(append(line, '\n'))
	l.hookMu.Unlock()
}

// closeHook closes the hook file if it was opened. Must be called after Run()
// completes to flush and release the file descriptor.
func (l *Loop) closeHook() {
	if l.hookFile != nil {
		l.hookFile.Close()
		l.hookFile = nil
	}
}

// persistMessage writes a single message to the database.
// No-op if DB is nil. Errors are logged to stderr but never returned,
// so the agent loop can continue even if the database is unavailable.
func (l *Loop) persistMessage(msg types.Message) {
	if l.DB == nil {
		return
	}
	content := msg.Content
	if content == "" {
		var parts []string
		for _, tc := range msg.ToolCalls {
			parts = append(parts, fmt.Sprintf("[tool:%s] %s", tc.Function.Name, tc.Function.Arguments))
		}
		content = strings.Join(parts, "\n")
	}
	toolCallsJSON := ""
	if len(msg.ToolCalls) > 0 {
		data, _ := json.Marshal(msg.ToolCalls)
		toolCallsJSON = string(data)
	}
	toolName := ""
	if msg.Role == "tool" {
		toolName = msg.Name
	}
	m := memory.Message{
		ID:         newMessageID(),
		SessionID:  l.SessionID,
		Idx:        l.MsgIdx,
		Role:       msg.Role,
		Content:    content,
		ToolName:   toolName,
		ToolCallID: msg.ToolCallID,
		ToolCalls:  toolCallsJSON,
		Timestamp:  time.Now().Unix(),
	}
	if err := l.writeMsg(m); err != nil {
		fmt.Fprintf(os.Stderr, "warning: db persist: %v\n", err)
		return
	}
	l.MsgIdx++
}

func newMessageID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func (l *Loop) writeMsg(m memory.Message) error {
	if l.WriteDebouncer != nil {
		return l.WriteDebouncer.Update(context.Background(), m)
	}
	if l.DB != nil {
		return l.DB.AddMessage(m)
	}
	return nil
}
