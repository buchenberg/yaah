package events

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
//
// The emitter is reusable across Loop Runs: Close releases the file
// descriptor and the next Emit lazily re-opens it. Callers must emit
// their final event (session.end) BEFORE Close.
type HookEmitter struct {
	dir       string
	sessionID string

	mu         sync.Mutex
	file       *os.File
	openFailed bool // sticky after the first failed open; no retry storms
}

// NewHookEmitter creates a HookEmitter. If dir is empty, all Emit
// calls are no-ops.
func NewHookEmitter(dir, sessionID string) *HookEmitter {
	return &HookEmitter{dir: dir, sessionID: sessionID}
}

// Emit writes a single JSONL event line. No-op if dir is empty or
// the file could not be opened. The file is opened lazily and
// re-opened after Close, so a reused Loop keeps emitting.
func (h *HookEmitter) Emit(event HookEvent) {
	if h.dir == "" {
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
	defer h.mu.Unlock()
	if h.file == nil && !h.openFailed {
		if err := os.MkdirAll(h.dir, 0o755); err != nil {
			h.openFailed = true
			return
		}
		path := filepath.Join(h.dir, h.sessionID+".jsonl")
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			h.openFailed = true
			return
		}
		h.file = f
	}
	if h.file == nil {
		return
	}
	h.file.Write(append(line, '\n'))
}

// Close flushes and releases the file descriptor. A subsequent Emit
// re-opens the file (the emitter is reusable across Runs).
func (h *HookEmitter) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.file != nil {
		h.file.Close()
		h.file = nil
	}
}
