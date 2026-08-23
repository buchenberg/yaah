package events_test

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/events"
)

// allEvents returns one instance of every concrete Event type.
// When you add an Event, ADD IT HERE. The exhaustive tests below
// then force you to acknowledge it in every View consumer.
func allEvents() []events.Event {
	return []events.Event{
		&events.TokenDeltaEvent{},
		&events.ThinkingEvent{},
		&events.FlushEvent{},
		&events.ToolStartEvent{},
		&events.ToolEndEvent{},
		&events.SubAgentStartEvent{},
		&events.SubAgentEndEvent{},
		&events.EscalationEvent{},
		&events.DoneEvent{},
		&events.CompactionStartedEvent{},
		&events.CompactionDoneEvent{},
	}
}

// TestAllEventsRegistryIsComplete cross-checks the allEvents registry
// against every "*Event struct" declaration in events.go so a new
// event type cannot silently dodge registration.
func TestAllEventsRegistryIsComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range allEvents() {
		name := reflect.TypeOf(e).Elem().Name()
		if seen[name] {
			t.Errorf("duplicate event %s in allEvents()", name)
		}
		seen[name] = true
	}

	eventsSrc := readFile(t, "events.go")
	sc := bufio.NewScanner(strings.NewReader(eventsSrc))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "type ") || !strings.HasSuffix(line, "Event struct") {
			continue
		}
		if strings.HasPrefix(line, "type Event") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(line, "type "), " struct")
		if !seen[name] {
			t.Errorf("event %s declared in events.go but missing from allEvents()", name)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan events.go: %v", err)
	}
}

// TestConsumersHandleEveryEvent asserts each View consumer's HandleEvent
// type switch references every event type. Deliberate ignoring must be
// an explicit `case *agent.XxxEvent:` with a comment — not silent fallthrough.
func TestConsumersHandleEveryEvent(t *testing.T) {
	consumers := []string{
		"../../tui/proxy.go",
		"../../acp/view.go",
		"../../../cmd/yaah/view_terminal.go",
	}
	for _, rel := range consumers {
		src := readFile(t, rel)
		for _, e := range allEvents() {
			name := reflect.TypeOf(e).Elem().Name()
			pat := regexp.MustCompile(`\*(agent\.)?` + name + `\b`)
			if !pat.MatchString(src) {
				t.Errorf("%s HandleEvent does not reference %s (add a case, or an explicit 'intentionally ignored' case)",
					filepath.Base(rel), name)
			}
		}
	}
}

func readFile(t *testing.T, rel string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(thisFile), rel)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
