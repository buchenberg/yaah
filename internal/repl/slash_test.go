package repl

import "testing"

func TestParseSlashCommand_recognizesBuiltinCommands(t *testing.T) {
	tests := []struct {
		input string
		want  SlashCommand
	}{
		{"/exit", CmdExit},
		{"/quit", CmdExit},
		{"/clear", CmdClear},
		{"/?", CmdHelp},
		{"/help", CmdHelp},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ParseSlashCommand(tt.input)
			if got != tt.want {
				t.Errorf("ParseSlashCommand(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseSlashCommand_returnsNoneForRegularInput(t *testing.T) {
	tests := []string{
		"hello world",
		"/unknown-command",
		"",
		"just text",
	}

	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			got := ParseSlashCommand(input)
			if got != CmdNone {
				t.Errorf("ParseSlashCommand(%q) = %v, want CmdNone", input, got)
			}
		})
	}
}
