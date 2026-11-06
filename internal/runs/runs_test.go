package runs

import "testing"

func TestStateString(t *testing.T) {
	tests := []struct {
		name  string
		state State
		want  string
	}{
		{name: "queued", state: StateQueued, want: "queued"},
		{name: "working", state: StateWorking, want: "working"},
		{name: "input_required", state: StateInputRequired, want: "input_required"},
		{name: "completed", state: StateCompleted, want: "completed"},
		{name: "failed", state: StateFailed, want: "failed"},
		{name: "cancelled", state: StateCancelled, want: "cancelled"},
		{name: "unknown value is visible", state: State(99), want: "state(99)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.state.String(); got != tt.want {
				t.Errorf("State(%d).String() = %q, want %q", int(tt.state), got, tt.want)
			}
		})
	}
}

func TestRunTerminal(t *testing.T) {
	tests := []struct {
		name  string
		state State
		want  bool
	}{
		{name: "queued is live", state: StateQueued, want: false},
		{name: "working is live", state: StateWorking, want: false},
		{name: "input_required is live", state: StateInputRequired, want: false},
		{name: "completed is terminal", state: StateCompleted, want: true},
		{name: "failed is terminal", state: StateFailed, want: true},
		{name: "cancelled is terminal", state: StateCancelled, want: true},
		{name: "unknown state is not terminal", state: State(42), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Run{RunID: "run-1", SessionID: "sess-1", State: tt.state}
			if got := r.Terminal(); got != tt.want {
				t.Errorf("Run{State: %s}.Terminal() = %v, want %v", tt.state, got, tt.want)
			}
		})
	}
}

// TestRunTerminalIgnoresContent pins the lifecycle to State alone: terminal
// text or a pending request must not make a live run look finished, and a
// settled run with an empty result is still settled.
func TestRunTerminalIgnoresContent(t *testing.T) {
	t.Run("live run with result and pending is not terminal", func(t *testing.T) {
		r := Run{
			State:   StateInputRequired,
			Result:  "partial text",
			Pending: []Pending{{Key: "q1", Prompt: "which branch?"}},
		}
		if r.Terminal() {
			t.Error("Terminal() = true for input_required run, want false")
		}
	})
	t.Run("completed run with no result is terminal", func(t *testing.T) {
		r := Run{State: StateCompleted}
		if !r.Terminal() {
			t.Error("Terminal() = false for completed run, want true")
		}
	})
}

func TestRunPendingByKey(t *testing.T) {
	run := Run{
		State: StateInputRequired,
		Pending: []Pending{
			{Key: "q1", Prompt: "which branch?"},
			{Key: "q2", Prompt: "force push?"},
		},
	}

	t.Run("finds an existing key", func(t *testing.T) {
		got, ok := run.PendingByKey("q2")
		if !ok {
			t.Fatal("PendingByKey(q2) not found, want found")
		}
		if got.Prompt != "force push?" {
			t.Errorf("PendingByKey(q2).Prompt = %q, want %q", got.Prompt, "force push?")
		}
	})

	t.Run("reports a missing key", func(t *testing.T) {
		if got, ok := run.PendingByKey("q3"); ok {
			t.Errorf("PendingByKey(q3) = %+v, true; want zero value, false", got)
		}
	})

	t.Run("empty run has no pending key", func(t *testing.T) {
		var empty Run
		if _, ok := empty.PendingByKey("q1"); ok {
			t.Error("PendingByKey(q1) on empty run reported found, want not found")
		}
	})
}

func TestRunDuplicatePendingKeys(t *testing.T) {
	tests := []struct {
		name    string
		pending []Pending
		want    []string
	}{
		{name: "no pending", pending: nil, want: nil},
		{
			name:    "unique keys",
			pending: []Pending{{Key: "q1"}, {Key: "q2"}, {Key: "q3"}},
			want:    nil,
		},
		{
			name:    "one duplicated key",
			pending: []Pending{{Key: "q1"}, {Key: "q2"}, {Key: "q1"}},
			want:    []string{"q1"},
		},
		{
			name:    "repeated key reported once",
			pending: []Pending{{Key: "q1"}, {Key: "q1"}, {Key: "q1"}},
			want:    []string{"q1"},
		},
		{
			name:    "two duplicated keys in first-duplicate order",
			pending: []Pending{{Key: "q2"}, {Key: "q1"}, {Key: "q2"}, {Key: "q1"}},
			want:    []string{"q2", "q1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Run{Pending: tt.pending}.DuplicatePendingKeys()
			if len(got) != len(tt.want) {
				t.Fatalf("DuplicatePendingKeys() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("DuplicatePendingKeys() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// TestZeroRun documents the zero value: a Run that was never filled in is a
// queued, live run with no content — never a finished one.
func TestZeroRun(t *testing.T) {
	var r Run
	if r.State != StateQueued {
		t.Errorf("zero Run.State = %s, want queued", r.State)
	}
	if r.Terminal() {
		t.Error("zero Run.Terminal() = true, want false")
	}
	if len(r.Pending) != 0 || r.Result != "" || r.Err != "" {
		t.Errorf("zero Run has content: %+v", r)
	}
}
