// Package assert is the fixed registry of named deterministic checks a `gate`
// node may name (yaah-0u0v.21).
//
// # An allowlist, not a namespace
//
// A document names one of the checks below by name. A name this package does
// not hold makes the gate unrunnable — it does not fall back to an expression
// evaluator, a shell command, or a model. That is the deliberate half of the
// gate design ("named asserts only"): a deterministic scheduler can only
// promise a deterministic answer for code it ships, and a free-form expression
// language is a second language to specify, sandbox, and validate. The
// validator (validate's WF037) and the scheduler both read this ONE list, so a
// document that validates is a document the scheduler can evaluate, and there
// is no second copy of the vocabulary to drift.
//
// # The signature
//
//	func(Input) (bool, string)
//
// Input carries the check's subject and its declared arguments; the answer is
// the verdict plus one short sentence saying why. Every check is pure: no
// clock, no I/O, no randomness, and no map iteration whose order could reach
// the answer. The scheduler evaluates a check inside a reducer cycle and
// records both values it returned; nothing re-runs it.
//
// # The subject
//
// A check that reads a value (Spec.ReadsValue) is handed it in Input.Value. The
// subject is the recorded value of the step named by the reserved argument
// "from" — and, when the author names none, the recorded values of the gate's
// own needs, in declaration order. Input.Value is nil when the subject step
// produced nothing, so a check can tell "no value at all" from "an empty value"
// and both answers stay honest: an assert that cannot be proven is not a pass.
//
// # Arguments
//
// Spec.Args declares the argument keys a check reads and the kind each must
// have; every declared argument is required, every undeclared one is refused.
// Problem answers "would this assert run at all" from the declarations alone,
// which is what makes the same sentence usable as a validation finding and as a
// run-time refusal.
package assert

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// SubjectKey is the reserved argument that names the step whose recorded value
// is the check's subject. It is not passed to the check: it selects its input.
const SubjectKey = "from"

// Input is what one check is evaluated against.
type Input struct {
	// Value is the subject: the recorded value of the step named by "from", or
	// the gate's needs' values in declaration order. nil means no value was
	// produced.
	Value any
	// Args are the check's declared arguments, exactly as the document wrote
	// them. The reserved "from" key is not present.
	Args map[string]any
}

// Check is the one fixed signature every registry entry has.
type Check func(Input) (bool, string)

// ArgKind is the shape a declared argument must have. The set is deliberately
// small: an assert's arguments are literal document data, and a kind the
// vocabulary cannot express is a check that needs a different declaration
// rather than a looser one.
type ArgKind string

// Argument kinds.
const (
	// ArgNumber is an integer or floating-point number.
	ArgNumber ArgKind = "number"
)

// Spec is one registry entry: what the check is called, what it means, what it
// reads, and the arguments it accepts.
type Spec struct {
	// Name is the allowlist key a document names.
	Name string
	// Doc states, in one line, what a pass means. It is the sentence a reader
	// of the registry needs; the checks themselves document the reasons.
	Doc string
	// ReadsValue reports that the check reads Input.Value. A check that does
	// not read one is a check over its arguments alone, and naming "from" for
	// it is refused rather than ignored.
	ReadsValue bool
	// Args declares the accepted argument keys and their required kinds. Every
	// declared argument is required; an empty map means the check takes none.
	Args map[string]ArgKind
	// Check is the evaluation.
	Check Check
}

// registry is the allowlist. Adding a check means adding an entry here and a
// case to the WF037 documentation — nowhere else, because both the validator
// and the scheduler read this map.
var registry = map[string]Spec{
	"nonempty": {
		Name:       "nonempty",
		Doc:        "the subject is present: a non-blank string, a non-empty list or mapping, or any other delivered value",
		ReadsValue: true,
		Check:      nonempty,
	},
	"all_succeeded": {
		Name:       "all_succeeded",
		Doc:        "the subject holds at least one entry, and no entry reports a failure (failed: true, status: error, or state: failed|cancelled)",
		ReadsValue: true,
		Check:      allSucceeded,
	},
	"count_ge": {
		Name:       "count_ge",
		Doc:        "the subject holds at least n entries",
		ReadsValue: true,
		Args:       map[string]ArgKind{"n": ArgNumber},
		Check:      countGe,
	},
	"count_eq": {
		Name:       "count_eq",
		Doc:        "the subject holds exactly n entries",
		ReadsValue: true,
		Args:       map[string]ArgKind{"n": ArgNumber},
		Check:      countEq,
	},
	"diff_within_limit": {
		Name:       "diff_within_limit",
		Doc:        "every entry that reports a changed-file count stays at or under max_files, and at least one entry reports one",
		ReadsValue: true,
		Args:       map[string]ArgKind{"max_files": ArgNumber},
		Check:      diffWithinLimit,
	},
}

// names is the allowlist in sorted order. It is built once so that Names, the
// refusal messages, and the validator's diagnostic all state one sequence: a
// map range would make the sentence an author reads depend on a hash seed.
var names = sortedNames()

func sortedNames() []string {
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Names returns every registered assert name, sorted. The slice is a copy: the
// allowlist is a package invariant, not a caller's to edit.
func Names() []string { return append([]string(nil), names...) }

// Lookup returns the entry for one name.
func Lookup(name string) (Spec, bool) {
	s, ok := registry[name]
	return s, ok
}

// Problem reports why one assert declaration cannot run, or "" when it can. It
// answers from the declaration alone — the name, the argument keys, the
// argument kinds, and the shape of the subject reference — so a validator can
// report it before a run and a scheduler can refuse it without ever calling the
// check. The message is a phrase, not a sentence with a leading code: the
// caller states which rule or which node found it.
func Problem(name string, args map[string]any) string {
	if strings.TrimSpace(name) == "" {
		return "assert names no check; the registry is an allowlist, and it holds " + strings.Join(names, ", ")
	}
	spec, ok := registry[name]
	if !ok {
		return "assert " + strconv.Quote(name) + " is not one of the registered checks (" +
			strings.Join(names, ", ") + "); the registry is an allowlist, not a namespace of free-form names"
	}
	if why := unknownArgs(spec, args); why != "" {
		return why
	}
	for _, key := range specArgs(spec) {
		v, given := args[key]
		if !given {
			return "assert " + strconv.Quote(name) + " needs the argument " + strconv.Quote(key) +
				" (" + string(spec.Args[key]) + ")"
		}
		if why := kindProblem(key, spec.Args[key], v); why != "" {
			return "assert " + strconv.Quote(name) + ": " + why
		}
	}
	if raw, given := args[SubjectKey]; given {
		s, isString := raw.(string)
		if !isString || strings.TrimSpace(s) == "" {
			return "assert " + strconv.Quote(name) + " reads " + strconv.Quote(SubjectKey) +
				" as a step id, and it must be a non-empty string"
		}
	}
	return ""
}

// unknownArgs names every argument key the check does not read, in sorted
// order. "from" is accepted only by a check that reads a value: a subject
// reference handed to a check that ignores it would be a declaration the run
// silently drops, which is the class of defect WF014 reports elsewhere.
func unknownArgs(spec Spec, args map[string]any) string {
	var unknown []string
	for key := range args {
		if key == SubjectKey && spec.ReadsValue {
			continue
		}
		if _, known := spec.Args[key]; !known {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return ""
	}
	sort.Strings(unknown)
	return "assert " + strconv.Quote(spec.Name) + " does not read " + quoteList(unknown)
}

// specArgs lists a spec's declared argument keys in sorted order.
func specArgs(spec Spec) []string {
	out := make([]string, 0, len(spec.Args))
	for k := range spec.Args {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// kindProblem reports an argument whose value is not the declared kind.
func kindProblem(key string, kind ArgKind, v any) string {
	switch kind {
	case ArgNumber:
		if _, ok := number(v); !ok {
			return "arg " + strconv.Quote(key) + " must be a number, got " + kindOf(v)
		}
		if f, ok := v.(float64); ok && math.Trunc(f) != f {
			return "arg " + strconv.Quote(key) + " must be a whole number (a count), got a fractional value"
		}
		if f, ok := v.(float32); ok && float32(math.Trunc(float64(f))) != f {
			return "arg " + strconv.Quote(key) + " must be a whole number (a count), got a fractional value"
		}
	}
	return ""
}

// quoteList renders keys as `"a", "b"` for a message.
func quoteList(keys []string) string {
	quoted := make([]string, 0, len(keys))
	for _, k := range keys {
		quoted = append(quoted, strconv.Quote(k))
	}
	return strings.Join(quoted, ", ")
}

// kindOf names a value's shape for a message, without ever printing the value:
// an argument may hold anything an author wrote, and a diagnostic is not the
// place to quote it.
func kindOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case []any:
		return "a list"
	case map[string]any:
		return "a mapping"
	}
	if _, ok := number(v); ok {
		return "a number"
	}
	return "an unrecognised value"
}

// number reads a numeric argument or element count. A decoded YAML document
// gives int, a decoded JSON one gives float64, and a Go-built fixture may give
// any integer width, so all three are read as the same number.
func number(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int32:
		return int(t), true
	case int64:
		return int(t), true
	case float32:
		return int(t), true
	case float64:
		return int(t), true
	}
	return 0, false
}

// entriesOf normalizes a subject into the entry list a collection-shaped check
// reads: a list is its own entries, and anything else — a single mapping, a
// string, a number — is ONE entry, because a gate over one workstream is a gate
// over one entry and not over its values. nil is no entries at all, which every
// collection-shaped check refuses rather than reporting success over nothing.
func entriesOf(v any) []any {
	switch t := v.(type) {
	case nil:
		return nil
	case []any:
		return append([]any(nil), t...)
	case []string:
		out := make([]any, 0, len(t))
		for _, s := range t {
			out = append(out, s)
		}
		return out
	}
	return []any{v}
}

// nonempty reports that the subject is present. Presence is a fact about the
// container, not about truth: a delivered 0, false, or a mapping with keys is
// present, while a blank string and an empty list or mapping are not.
func nonempty(in Input) (bool, string) {
	switch t := in.Value.(type) {
	case nil:
		return false, "the subject is missing: no step produced a value for it"
	case string:
		if strings.TrimSpace(t) == "" {
			return false, "the subject is an empty string"
		}
		return true, "the subject carries text"
	case []any:
		if len(t) == 0 {
			return false, "the subject is an empty list"
		}
		return true, "the subject has " + strconv.Itoa(len(t)) + " entries"
	case []string:
		if len(t) == 0 {
			return false, "the subject is an empty list"
		}
		return true, "the subject has " + strconv.Itoa(len(t)) + " entries"
	case map[string]any:
		if len(t) == 0 {
			return false, "the subject is an empty mapping"
		}
		return true, "the subject has " + strconv.Itoa(len(t)) + " keys"
	}
	return true, "the subject is present"
}

// allSucceeded reports that the subject holds work and none of it failed. The
// failure markers are the three shapes a producer's record uses: fan-out's
// on_item_failure: collect_failures writes `failed: true` in place of a lost
// item, an envelope status of "error" arrives as `status`, and a correlated
// node's lifecycle state reads as `state`.
func allSucceeded(in Input) (bool, string) {
	entries := entriesOf(in.Value)
	if len(entries) == 0 {
		return false, "the subject holds no entries to judge"
	}
	var failed []string
	for i, e := range entries {
		if e == nil || entryFailed(e) {
			failed = append(failed, strconv.Itoa(i))
		}
	}
	if len(failed) > 0 {
		return false, "entries " + strings.Join(failed, ",") + " of " + strconv.Itoa(len(entries)) +
			" report a failure"
	}
	return true, "all " + strconv.Itoa(len(entries)) + " entries succeeded"
}

// entryFailed reports whether one entry says it failed.
func entryFailed(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	if failed, ok := m["failed"].(bool); ok && failed {
		return true
	}
	if status, ok := m["status"].(string); ok && strings.EqualFold(strings.TrimSpace(status), "error") {
		return true
	}
	if state, ok := m["state"].(string); ok {
		switch strings.ToLower(strings.TrimSpace(state)) {
		case "failed", "error", "cancelled":
			return true
		}
	}
	return false
}

// countGe reports at least n entries. Both answers state the same two numbers,
// so a log line and a refusal read alike.
func countGe(in Input) (bool, string) {
	want, _ := number(in.Args["n"])
	got := len(entriesOf(in.Value))
	return got >= want, entryCount(got, "at least", want)
}

// countEq reports exactly n entries.
func countEq(in Input) (bool, string) {
	want, _ := number(in.Args["n"])
	got := len(entriesOf(in.Value))
	return got == want, entryCount(got, "exactly", want)
}

// entryCount renders "3 entries, at least 2 required".
func entryCount(got int, relation string, want int) string {
	return strconv.Itoa(got) + " entries, " + relation + " " + strconv.Itoa(want) + " required"
}

// diffWithinLimit reports that no entry changes more files than the limit. An
// entry that reports no file count is not a pass: the check's whole input is
// that count, so a set where nobody states one has nothing to certify, which is
// the same refusal select's smallest_diff states when no candidate carries a
// diff size.
func diffWithinLimit(in Input) (bool, string) {
	max, _ := number(in.Args["max_files"])
	entries := entriesOf(in.Value)
	if len(entries) == 0 {
		return false, "the subject holds no entries to inspect"
	}
	counted := 0
	var over []string
	for i, e := range entries {
		n, ok := fileCount(e)
		if !ok {
			continue
		}
		counted++
		if n > max {
			over = append(over, "entry "+strconv.Itoa(i)+" changes "+strconv.Itoa(n)+" files")
		}
	}
	if len(over) > 0 {
		return false, strings.Join(over, ", ") + ", over the limit of " + strconv.Itoa(max)
	}
	if counted == 0 {
		return false, "no entry reports how many files it changes, so the limit cannot be checked"
	}
	return true, strconv.Itoa(counted) + " entries change at most " + strconv.Itoa(max) + " files each"
}

// fileCount reads the changed-file count one entry carries, under the three
// spellings a lane's delivered payload uses, plus a nested `diff` object.
func fileCount(v any) (int, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return 0, false
	}
	for _, key := range []string{"files", "files_changed", "changed_files"} {
		if n, ok := number(m[key]); ok {
			return n, true
		}
	}
	if diff, ok := m["diff"].(map[string]any); ok {
		for _, key := range []string{"files", "file_count"} {
			if n, ok := number(diff[key]); ok {
				return n, true
			}
		}
	}
	return 0, false
}
