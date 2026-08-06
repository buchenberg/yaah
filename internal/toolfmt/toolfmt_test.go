package toolfmt

import (
	"testing"
)

// ---------- Num ----------

func TestNum(t *testing.T) {
	tests := []struct {
		name  string
		input int
		want  string
	}{
		{"zero", 0, "0"},
		{"one", 1, "1"},
		{"single_digit", 5, "5"},
		{"hundreds", 123, "123"},
		{"thousands", 1234, "1,234"},
		{"ten_thousands", 12345, "12,345"},
		{"hundred_thousands", 123456, "123,456"},
		{"millions", 1234567, "1,234,567"},
		{"ten_millions", 12345678, "12,345,678"},
		{"negative_one", -1, "-1"},
		{"negative_thousands", -1234, "-1,234"},
		{"edge_999", 999, "999"},
		{"edge_1000", 1000, "1,000"},
		{"edge_1000000", 1000000, "1,000,000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Num(tt.input)
			if got != tt.want {
				t.Errorf("Num(%d) = %q; want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------- FilePath ----------

func TestFilePath(t *testing.T) {
	tests := []struct {
		name string
		args string
		want string
	}{
		{"absolute_path", `{"filePath": "/absolute/path/to/file.txt"}`, "file.txt"},
		{"relative_path", `{"filePath": "relative/path/file.txt"}`, "file.txt"},
		{"path_field", `{"path": "/some/path/thing.go"}`, "thing.go"},
		{"just_filename", `{"filePath": "justfile.txt"}`, "justfile.txt"},
		{"no_match", `{"other": "value"}`, ""},
		{"empty_json", "", ""},
		{"empty_filepath", `{"filePath": ""}`, ""},
		{"deep_path", `{"filePath": "/a/b/c/d.go"}`, "d.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilePath(tt.args)
			if got != tt.want {
				t.Errorf("FilePath(%q) = %q; want %q", tt.args, got, tt.want)
			}
		})
	}
}

// ---------- MatchJSONField ----------

func TestMatchJSONField(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
		field   string
		want    string
	}{
		{"simple_match", `{"name": "hello"}`, "name", "hello"},
		{"second_field", `{"a": "1", "b": "2"}`, "b", "2"},
		{"not_present", `{"x": "y"}`, "z", ""},
		{"empty_json", "", "anything", ""},
		{"numeric_value", `{"age": 42}`, "age", ""},
		{"field_with_special_chars", `{"my.field": "val"}`, "my.field", "val"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchJSONField(tt.jsonStr, tt.field)
			if got != tt.want {
				t.Errorf("MatchJSONField(%q, %q) = %q; want %q", tt.jsonStr, tt.field, got, tt.want)
			}
		})
	}
}

// ---------- GrepSummary ----------

func TestGrepSummary(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"empty", "", "0 matches"},
		{"single_match", "1: match line", "1 matches in 1 files"},
		{"two_matches", "1: line one\n2: line two", "2 matches in 1 files"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GrepSummary(tt.content)
			if got != tt.want {
				t.Errorf("GrepSummary(%q) = %q; want %q", tt.content, got, tt.want)
			}
		})
	}
}

// ---------- GlobSummary ----------

func TestGlobSummary(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"empty", "", "0 files"},
		{"one_file", "file1.go", "1 files"},
		{"three_files", "file1.go\nfile2.go\nfile3.go", "3 files"},
		{"trailing_newline", "a.go\nb.go\n", "2 files"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GlobSummary(tt.content)
			if got != tt.want {
				t.Errorf("GlobSummary(%q) = %q; want %q", tt.content, got, tt.want)
			}
		})
	}
}

// ---------- LsSummary ----------

func TestLsSummary(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"empty", "", "0 entries"},
		{"one_entry", "entry1", "1 entries"},
		{"three_entries", "entry1\nentry2\nentry3", "3 entries"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LsSummary(tt.content)
			if got != tt.want {
				t.Errorf("LsSummary(%q) = %q; want %q", tt.content, got, tt.want)
			}
		})
	}
}

// ---------- BashSummary ----------

func TestBashSummary(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"empty", "", ""},
		{"whitespace_only", "   \t\n  ", ""},
		{"short_line", "hello", "hello"},
		{"multi_line_first_short", "line1\nline2\nline3", "line1"},
		{"first_line_long", "this is a very long first line that exceeds sixty characters total", "this is a very long first line that exceeds sixty charact..."},
		{"exactly_60", "123456789012345678901234567890123456789012345678901234567890", "123456789012345678901234567890123456789012345678901234567890"},
		{"61_chars", "1234567890123456789012345678901234567890123456789012345678901", "123456789012345678901234567890123456789012345678901234567..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BashSummary(tt.content)
			if got != tt.want {
				t.Errorf("BashSummary(%q) = %q; want %q", tt.content, got, tt.want)
			}
		})
	}
}

// ---------- SubagentLabel ----------

func TestSubagentLabel(t *testing.T) {
	tests := []struct {
		name string
		role string
		desc string
		want string
	}{
		{"both_role_and_desc", "developer", "my task", "sub-agent: developer \u00b7 my task"},
		{"role_only", "analyst", "", "sub-agent: analyst"},
		{"desc_only", "", "my task", "sub-agent \u00b7 my task"},
		{"both_empty", "", "", "sub-agent"},
		{"tester_role", "tester", "run tests", "sub-agent: tester \u00b7 run tests"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SubagentLabel(tt.role, tt.desc)
			if got != tt.want {
				t.Errorf("SubagentLabel(%q, %q) = %q; want %q", tt.role, tt.desc, got, tt.want)
			}
		})
	}
}

// ---------- Summary ----------

func TestSummary(t *testing.T) {
	t.Run("grep", func(t *testing.T) {
		got := Summary("grep", `{"pattern":"foo"}`, "1: match line")
		want := "1 matches in 1 files"
		if got != want {
			t.Errorf("Summary(grep) = %q; want %q", got, want)
		}
	})

	t.Run("glob", func(t *testing.T) {
		got := Summary("glob", `{"pattern":"*.go"}`, "a.go\nb.go")
		want := "2 files"
		if got != want {
			t.Errorf("Summary(glob) = %q; want %q", got, want)
		}
	})

	t.Run("ls", func(t *testing.T) {
		got := Summary("ls", `{"path":"."}`, "dir1\ndir2\nfile1")
		want := "3 entries"
		if got != want {
			t.Errorf("Summary(ls) = %q; want %q", got, want)
		}
	})

	t.Run("bash", func(t *testing.T) {
		got := Summary("bash", `{"command":"echo hi"}`, "hi")
		want := "hi"
		if got != want {
			t.Errorf("Summary(bash) = %q; want %q", got, want)
		}
	})

	t.Run("read", func(t *testing.T) {
		got := Summary("read", `{"filePath":"/path/to/foo.go"}`, "package foo\n")
		want := "read foo.go (12 chars)"
		if got != want {
			t.Errorf("Summary(read) = %q; want %q", got, want)
		}
	})

	t.Run("write", func(t *testing.T) {
		got := Summary("write", `{"filePath":"/path/to/bar.go"}`, "package bar\n\nfunc main() {}")
		want := "wrote bar.go (27 chars)"
		if got != want {
			t.Errorf("Summary(write) = %q; want %q", got, want)
		}
	})

	t.Run("edit", func(t *testing.T) {
		got := Summary("edit", `{"filePath":"/path/to/file.go"}`, "...")
		want := "edited file.go"
		if got != want {
			t.Errorf("Summary(edit) = %q; want %q", got, want)
		}
	})

	t.Run("delete", func(t *testing.T) {
		got := Summary("delete", `{"filePath":"/path/to/old.go"}`, "deleted")
		want := "deleted old.go"
		if got != want {
			t.Errorf("Summary(delete) = %q; want %q", got, want)
		}
	})

	t.Run("http", func(t *testing.T) {
		got := Summary("http", `{"url":"https://example.com/api"}`, "response body")
		want := "https://example.com/api"
		if got != want {
			t.Errorf("Summary(http) = %q; want %q", got, want)
		}
	})

	t.Run("webfetch", func(t *testing.T) {
		got := Summary("webfetch", `{"url":"https://docs.example.com"}`, "markdown content")
		want := "https://docs.example.com"
		if got != want {
			t.Errorf("Summary(webfetch) = %q; want %q", got, want)
		}
	})

	t.Run("replace", func(t *testing.T) {
		got := Summary("replace", `{"filePath":"/src/foo.go"}`, "done")
		want := "replaced in foo.go"
		if got != want {
			t.Errorf("Summary(replace) = %q; want %q", got, want)
		}
	})

	t.Run("spawn_subagent", func(t *testing.T) {
		got := Summary("spawn_subagent", `{"role":"developer","description":"fix bug"}`, "")
		want := "sub-agent: developer \u00b7 fix bug"
		if got != want {
			t.Errorf("Summary(spawn_subagent) = %q; want %q", got, want)
		}
	})

	t.Run("git", func(t *testing.T) {
		got := Summary("git", `{"action":"commit"}`, "done")
		want := "commit"
		if got != want {
			t.Errorf("Summary(git) = %q; want %q", got, want)
		}
	})

	t.Run("unknown_tool_short", func(t *testing.T) {
		got := Summary("mystery_tool", `{}`, "first line\nsecond line")
		want := "first line"
		if got != want {
			t.Errorf("Summary(unknown) = %q; want %q", got, want)
		}
	})

	t.Run("unknown_tool_long_first_line", func(t *testing.T) {
		long := "this first line is over eighty characters long and should be truncated properly by the code"
		got := Summary("mystery", `{}`, long+"\nsecond")
		want := long[:77] + "..."
		if got != want {
			t.Errorf("Summary(unknown long) = %q; want %q", got, want)
		}
	})
}
