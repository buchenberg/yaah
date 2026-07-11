package spinner

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestSpinner_startsAndStops(t *testing.T) {
	var buf bytes.Buffer
	s := New(&buf, "Thinking")

	s.Start()
	time.Sleep(150 * time.Millisecond)
	s.Stop()

	output := buf.String()
	// After stop, the line should be cleared
	if !strings.Contains(output, "\r") {
		t.Error("expected carriage return in output")
	}
}

func TestSpinner_stopIsIdempotent(t *testing.T) {
	var buf bytes.Buffer
	s := New(&buf, "Thinking")

	s.Start()
	time.Sleep(50 * time.Millisecond)
	s.Stop()
	s.Stop() // should not panic

	output := buf.String()
	if len(output) == 0 {
		t.Error("expected some output")
	}
}

func TestSpinner_worksWithNilWriter(t *testing.T) {
	// Should not panic with nil writer (uses os.Stderr)
	s := New(nil, "Thinking")
	s.Start()
	time.Sleep(50 * time.Millisecond)
	s.Stop()
}
