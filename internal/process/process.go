package process

import (
	"bufio"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// logBufSize caps the retained log output per process.
const logBufSize = 200 * 1024 // 200 KB

// Info holds state for a single tracked background process.
type Info struct {
	ID          string
	Command     string
	Description string
	StartedAt   time.Time
	Status      string // "running", "finished", "stopped", "error"
	logs        strings.Builder
	cmd         *exec.Cmd
	mu          sync.Mutex
}

// Logs returns a copy of the process output buffer.
func (p *Info) Logs() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.logs.String()
}

// appendLog appends text to the log buffer, capping at logBufSize.
func (p *Info) appendLog(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	remaining := logBufSize - p.logs.Len()
	if remaining <= 0 {
		return
	}
	if len(s) > remaining {
		s = s[:remaining]
	}
	p.logs.WriteString(s)
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

// Start launches a command as a tracked background process.
func (m *Manager) Start(command, description string) (*Info, error) {
	// Use powershell on Windows, sh on Unix for the shell wrapper
	shell, shellFlag := "sh", "-c"
	if _, err := exec.LookPath("pwsh"); err == nil {
		shell = "pwsh"
		shellFlag = "-Command"
	} else if _, err := exec.LookPath("powershell"); err == nil {
		shell = "powershell"
		shellFlag = "-Command"
	}

	cmd := exec.Command(shell, shellFlag, command)
	cmd.Stdin = nil // close stdin so the child never blocks waiting for input
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	m.nextID.Add(1)
	id := fmt.Sprintf("proc-%d", m.nextID.Load())

	info := &Info{
		ID:          id,
		Command:     command,
		Description: description,
		StartedAt:   time.Now(),
		Status:      "running",
		cmd:         cmd,
	}

	if err := cmd.Start(); err != nil {
		info.Status = "error"
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
		if info.Status == "running" {
			if err != nil {
				info.Status = "error"
				// Write directly to logs since we already hold info.mu
				// (appendLog would deadlock — sync.Mutex is not reentrant).
				info.logs.WriteString(fmt.Sprintf("exit error: %v\n", err))
			} else {
				info.Status = "finished"
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
	if info.Status != "running" {
		info.mu.Unlock()
		return fmt.Errorf("process %s is not running", id)
	}
	info.Status = "stopped"
	info.mu.Unlock()

	if info.cmd != nil && info.cmd.Process != nil {
		return info.cmd.Process.Kill()
	}
	return nil
}
