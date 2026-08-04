package tool

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

func TestStart(t *testing.T) {
	tests := []struct {
		name string
		args string
		want string
	}{
		{
			name: "basic tool call",
			args: "arg1",
			want: colors.Dim + "🔧 read arg1" + colors.Reset + "\n",
		},
		{
			name: "no args",
			args: "",
			want: colors.Dim + "🔧 read " + colors.Reset + "\n",
		},
		{
			name: "multiple args",
			args: "path/to/file limit=10",
			want: colors.Dim + "🔧 read path/to/file limit=10" + colors.Reset + "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Start("read", tt.args)
			if got != tt.want {
				t.Errorf("Start() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStartDimsEntireMessage(t *testing.T) {
	got := Start("bash", "echo hello")
	if !strings.HasPrefix(got, colors.Dim) {
		t.Errorf("Start() output should start with Dim color tag, got %q", got)
	}
	if !strings.HasSuffix(got, colors.Reset+"\n") {
		t.Errorf("Start() output should end with Reset + newline, got %q", got)
	}
	if got != colors.Dim+"🔧 bash echo hello"+colors.Reset+"\n" {
		t.Errorf("Start() = %q, want %q", got, colors.Dim+"🔧 bash echo hello"+colors.Reset+"\n")
	}
}

func TestEnd(t *testing.T) {
	tests := []struct {
		name   string
		result string
		want   string
	}{
		{
			name:   "basic completion",
			result: "",
			want:   colors.Dim + "✅ read done" + colors.Reset + "\n",
		},
		{
			name:   "with result",
			result: "output",
			want:   colors.Dim + "✅ read done" + colors.Reset + "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := End("read", tt.result)
			if got != tt.want {
				t.Errorf("End() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEndDimsEntireMessage(t *testing.T) {
	got := End("bash", "output")
	if !strings.HasPrefix(got, colors.Dim) {
		t.Errorf("End() output should start with Dim color tag, got %q", got)
	}
	if !strings.HasSuffix(got, colors.Reset+"\n") {
		t.Errorf("End() output should end with Reset + newline, got %q", got)
	}
	if got != colors.Dim+"✅ bash done"+colors.Reset+"\n" {
		t.Errorf("End() = %q, want %q", got, colors.Dim+"✅ bash done"+colors.Reset+"\n")
	}
}

func TestSummary(t *testing.T) {
	got := Summary("read", "file contents")
	if !strings.Contains(got, "✅ read") {
		t.Errorf("Summary() should contain tool icon and name, got %q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("Summary() should end with newline, got %q", got)
	}
}
