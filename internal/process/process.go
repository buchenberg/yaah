package process

import (
	"bufio"
	"fmt"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// logBufSize caps the retained log output per process.
const logBufSize = 200 * 1024 // 200 KB

// logTruncatedMarker prefixes a log whose oldest output was dropped. The
// retained tail is the diagnostically useful part (a server/build's most
// recent output), so overflow drops the head, not the tail.
const logTruncatedMarker = "...[earlier output dropped]...\n"

// Info holds state for a single tracked background process.
type Info struct {
	ID          string
	Command     string
	Description string
	StartedAt   time.Time
	status      string // "running", "finished", "stopped", "error"
	logBuf      []byte // ring buffer of logBufSize bytes, allocated on first output
	logPos      int    // next write position in logBuf
	logLen      int    // valid bytes in logBuf (<= logBufSize)
	logDropped  bool   // at least one old byte was overwritten
	cmd         *exec.Cmd
	mu          sync.Mutex
}

// Status returns the current lifecycle state under the same mutex used by
// the exit and stop paths. Callers outside this package must use this
// accessor; the field is unexported so an unsynchronized read cannot compile.
func (p *Info) Status() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

// Logs returns the retained process output. When the buffer overflowed, the
// oldest output is gone and the result starts with logTruncatedMarker; the
// newest output is always retained.
func (p *Info) Logs() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.logLen == 0 {
		return ""
	}
	if !p.logDropped {
		// Without an overwrite the buffer is linear, even if a write ended
		// exactly at the cap (logPos == 0).
		return string(p.logBuf[:p.logLen])
	}
	return logTruncatedMarker + string(p.logBuf[p.logPos:]) + string(p.logBuf[:p.logPos])
}

// logsTruncated reports whether earlier output was dropped from the buffer.
// It distinguishes a process that produced nothing (false, empty Logs) from
// one whose head overflowed (true).
func (p *Info) logsTruncated() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.logDropped
}

// appendLog appends text to the ring buffer, retaining the newest logBufSize
// bytes. Overflow drops the oldest output rather than the tail.
func (p *Info) appendLog(s string) {
	if s == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.appendLogLocked(s)
}

// appendLogLocked is appendLog for callers that already hold p.mu.
func (p *Info) appendLogLocked(s string) {
	if s == "" {
		return
	}
	if p.logBuf == nil {
		p.logBuf = make([]byte, logBufSize)
	}
	if len(s) >= logBufSize {
		dropped := p.logLen > 0 || len(s) > logBufSize
		copy(p.logBuf, s[len(s)-logBufSize:])
		p.logPos = 0
		p.logLen = logBufSize
		p.logDropped = p.logDropped || dropped
		return
	}
	for len(s) > 0 {
		n := copy(p.logBuf[p.logPos:], s)
		if p.logLen == logBufSize {
			p.logDropped = true
		}
		p.logPos = (p.logPos + n) % logBufSize
		p.logLen += n
		if p.logLen > logBufSize {
			p.logLen = logBufSize
		}
		s = s[n:]
	}
}

// Manager tracks all background processes.
type Manager struct {
	mu     sync.Mutex
	procs  map[string]*Info
	nextID atomic.Int64
}

// NewManager creates a new process manager.
func NewManager() *Manager {
	return &Manager{
		procs: make(map[string]*Info),
	}
}

// nextProcessID mints a unique process id. Add returns the new value, so
// concurrent callers cannot observe the same counter value.
func (m *Manager) nextProcessID() string {
	return fmt.Sprintf("proc-%d", m.nextID.Add(1))
}

// Start launches a command as a tracked background process.
func (m *Manager) Start(command, description string) (*Info, error) {
	// Use powershell on Windows, sh on Unix for the shell wrapper.
	// On Unix we must NOT prefer pwsh even if it is on PATH (GitHub Actions
	// ubuntu runners include pwsh, whose startup is ~1-2s and causes
	// trivial commands like `echo` to blow past test timeouts).
	// On Windows -NoProfile skips profile scripts: startup is faster and
	// deterministic regardless of the user's profile contents.
	shell, shellArgs := "sh", []string{"-c", command}
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("pwsh"); err == nil {
			shell = "pwsh"
			shellArgs = []string{"-NoProfile", "-Command", command}
		} else if _, err := exec.LookPath("powershell"); err == nil {
			shell = "powershell"
			shellArgs = []string{"-NoProfile", "-Command", command}
		}
	}

	cmd := exec.Command(shell, shellArgs...)
	cmd.Stdin = nil // close stdin so the child never blocks waiting for input
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	id := m.nextProcessID()

	info := &Info{
		ID:          id,
		Command:     command,
		Description: description,
		StartedAt:   time.Now(),
		status:      "running",
		cmd:         cmd,
	}

	if err := cmd.Start(); err != nil {
		info.mu.Lock()
		info.status = "error"
		info.mu.Unlock()
		info.appendLog(fmt.Sprintf("start error: %v\n", err))
		return info, err
	}

	m.mu.Lock()
	m.procs[id] = info
	m.mu.Unlock()

	// Read stdout/stderr in background, then wait for the process to exit.
	// Per Go docs, cmd.Wait must not be called before all pipe reads complete.
	var pipeWg sync.WaitGroup
	pipeWg.Add(2)
	go func() {
		defer pipeWg.Done()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			info.appendLog(scanner.Text() + "\n")
		}
	}()
	go func() {
		defer pipeWg.Done()
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			info.appendLog(scanner.Text() + "\n")
		}
	}()

	go func() {
		pipeWg.Wait()
		err := cmd.Wait()
		info.mu.Lock()
		if info.status == "running" {
			if err != nil {
				info.status = "error"
				// Write directly to the ring buffer since we already hold
				// info.mu (appendLog would deadlock — sync.Mutex is not
				// reentrant).
				info.appendLogLocked(fmt.Sprintf("exit error: %v\n", err))
			} else {
				info.status = "finished"
			}
		}
		info.mu.Unlock()
	}()

	return info, nil
}

// List returns a copy of all tracked processes.
func (m *Manager) List() []*Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Info, 0, len(m.procs))
	for _, p := range m.procs {
		out = append(out, p)
	}
	return out
}

// Get returns a process by ID.
func (m *Manager) Get(id string) *Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.procs[id]
}

// Stop terminates a running process.
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	info := m.procs[id]
	m.mu.Unlock()
	if info == nil {
		return fmt.Errorf("process %s not found", id)
	}
	info.mu.Lock()
	if info.status != "running" {
		info.mu.Unlock()
		return fmt.Errorf("process %s is not running", id)
	}
	info.status = "stopped"
	info.mu.Unlock()

	if info.cmd != nil && info.cmd.Process != nil {
		return info.cmd.Process.Kill()
	}
	return nil
}
