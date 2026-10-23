package command

import "strings"

// maxCompletions caps how many suggestions the composer offers at once.
const maxCompletions = 8

// Completion is one typeahead suggestion for the composer.
type Completion struct {
	Label  string // command name, without the leading ':'
	Desc   string // one-line description shown beside the name
	Insert string // text to place in the composer, e.g. ":steer "
}

// Complete returns typeahead suggestions for text typed in the composer.
//
// Suggestions are offered only while a command *name* is being typed: text that
// does not start with ':' — or that already contains a space, meaning an
// argument is in progress — yields none.
func Complete(text string, entries []Entry) []Completion {
	body, ok := strings.CutPrefix(text, ":")
	if !ok || strings.ContainsAny(body, " \t") {
		return nil
	}
	prefix := strings.ToLower(body)
	var out []Completion
	for _, e := range entries {
		if e.Label == "" || !strings.HasPrefix(strings.ToLower(e.Label), prefix) {
			continue
		}
		insert := ":" + e.Label
		if RequiresArg(e.Cmd) || e.Cmd == CmdModel {
			// Leave the cursor where the argument starts.
			insert += " "
		}
		out = append(out, Completion{Label: e.Label, Desc: e.Desc, Insert: insert})
		if len(out) == maxCompletions {
			break
		}
	}
	return out
}
