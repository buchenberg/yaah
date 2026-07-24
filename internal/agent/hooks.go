package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// HookEmitter writes structured JSONL events to a hook directory.
// Emission is best-effort: errors are silently ignored so a failed
// write never breaks the agent loop. Safe for concurrent use.
type HookEmitter struct {
	dir       string
	sessionID string

	mu   sync.Mutex
	file *os.File
	once sync.Once
	ok   bool
}

// NewHookEmitter creates a HookEmitter. If dir is empty, all Emit
// calls are no-ops.
func NewHookEmitter(dir, sessionID string) *HookEmitter {
	return &HookEmitter{dir: dir, sessionID: sessionID}
}

// Emit writes a single JSONL event line. No-op if dir is empty or
// the file could not be opened.
func (h *HookEmitter) Emit(event HookEvent) {
	if h.dir == "" {
		return
	}
	h.once.Do(func() {
		if err := os.MkdirAll(h.dir, 0o755); err != nil {
			return
		}
		path := filepath.Join(h.dir, h.sessionID+".jsonl")
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		h.file = f
		h.ok = true
	})
	if !h.ok {
		return
	}
	event.SessionID = h.sessionID
	if event.Timestamp == 0 {
		event.Timestamp = time.Now().UnixMilli()
	}
	line, err := json.Marshal(event)
	if err != nil {
		return
	}
	h.mu.Lock()
	h.file.Write(append(line, '\n'))
	h.mu.Unlock()
}

// Close flushes and releases the file descriptor.
func (h *HookEmitter) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.file != nil {
		h.file.Close()
		h.file = nil
	}
}
