// Package spinner provides a simple terminal spinner for indicating
// background processing. It writes animated dots to stderr (or any
// io.Writer) and clears the line when stopped.
package spinner

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Spinner displays an animated thinking indicator.
type Spinner struct {
	writer io.Writer
	label  string
	done   chan struct{}
	once   sync.Once
}

// New creates a new Spinner with the given label. If writer is nil,
// os.Stderr is used.
func New(writer io.Writer, label string) *Spinner {
	if writer == nil {
		writer = os.Stderr
	}
	return &Spinner{
		writer: writer,
		label:  label,
		done:   make(chan struct{}),
	}
}

// Start begins the spinner animation in a background goroutine.
func (s *Spinner) Start() {
	go s.run()
}

// Stop stops the spinner and clears the line.
func (s *Spinner) Stop() {
	s.once.Do(func() {
		close(s.done)
	})
	// Clear the spinner line
	fmt.Fprintf(s.writer, "\r%s\r", clearLine())
}

func (s *Spinner) run() {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	i := 0
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			frame := frames[i%len(frames)]
			fmt.Fprintf(s.writer, "\r\x1b[36m%s\x1b[0m %s", frame, s.label)
			i++
		}
	}
}

func clearLine() string {
	return "\r\x1b[2K"
}
