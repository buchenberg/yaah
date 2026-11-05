// Package wake implements yaah's event-driven waiting: one predicate engine
// with two faces —
//
//   - wait_for: blocking and turn-scoped, so a wait costs zero model turns;
//   - watch: armed and session-scoped, delivered as a wake follow-up when the
//     predicate fires.
//
// A predicate is a source plus a target ("job:bg-1", "file:tmp/ci-green")
// and, for the polling sources, an optional condition ("regex:PASS",
// "status:2xx"). The engine depends on a small Deps seam so the same code
// evaluates jobs, background processes, local commands, HTTP endpoints and
// workspace files without this package importing the tool layer.
package wake

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Source is the kind of thing a predicate observes.
type Source string

const (
	SourceJob     Source = "job"
	SourceProcess Source = "process"
	SourceTime    Source = "time"
	SourceCommand Source = "command"
	SourceHTTP    Source = "http"
	SourceFile    Source = "file"
)

// Spec is one raw predicate, exactly as the model supplied it.
type Spec struct {
	// On is "<source>:<target>", for example "job:bg-2" or
	// "command:go test ./...".
	On string
	// Until is the optional condition, for example "regex:PASS" or
	// "status:200". Its meaning depends on the source.
	Until string
}

// ParseOn splits a raw "on" value into its source and target. It splits on
// the first colon only, so targets that contain colons — an https URL, a
// Windows drive path — survive intact.
func ParseOn(on string) (Source, string, error) {
	on = strings.TrimSpace(on)
	if on == "" {
		return "", "", fmt.Errorf("on is required (for example \"job:bg-1\" or \"time:30s\")")
	}
	i := strings.IndexByte(on, ':')
	if i <= 0 {
		return "", "", fmt.Errorf("on %q must name a source: target — job:<id>, process:<id>, time:<dur>, command:<cmd>, http:<url>, or file:<glob>", on)
	}
	src := Source(strings.ToLower(strings.TrimSpace(on[:i])))
	target := strings.TrimSpace(on[i+1:])
	if target == "" {
		return "", "", fmt.Errorf("on %q has no target after %q", on, src)
	}
	switch src {
	case SourceJob, SourceProcess, SourceTime, SourceCommand, SourceHTTP, SourceFile:
		return src, target, nil
	default:
		return "", "", fmt.Errorf("unknown predicate source %q (want job, process, time, command, http, or file)", src)
	}
}

// SourceOf reports the source of s without validating its target.
func (s Spec) SourceOf() Source {
	src, _, err := ParseOn(s.On)
	if err != nil {
		return ""
	}
	return src
}

// DefaultInterval is the first poll delay for a source. Backoff doubles it up
// to MaxInterval.
func DefaultInterval(src Source) time.Duration {
	switch src {
	case SourceJob:
		return 0 // blocking wait, no polling
	case SourceTime:
		return time.Second
	case SourceFile:
		return 500 * time.Millisecond
	case SourceCommand, SourceHTTP:
		return 2 * time.Second
	default:
		return time.Second
	}
}

// MinInterval and MaxInterval bound a caller-supplied poll interval. The
// per-source ceilings below are usually tighter: a cheap file or health check
// should not inherit the 30s ceiling a slow command needs.
const (
	MinInterval = 100 * time.Millisecond
	MaxInterval = 30 * time.Second
)

// MaxIntervalFor is the backoff ceiling for a source. Zero means the source
// does not poll (jobs block on their completion channel).
func MaxIntervalFor(src Source) time.Duration {
	switch src {
	case SourceJob:
		return 0
	case SourceTime:
		return time.Second
	case SourceFile:
		return 5 * time.Second
	case SourceHTTP:
		return 15 * time.Second
	case SourceCommand, SourceProcess:
		return MaxInterval
	default:
		return MaxInterval
	}
}

// intervalFor normalizes a requested first interval for the source, applying
// both the global bounds and the source's tighter ceiling.
func intervalFor(src Source, requested time.Duration) time.Duration {
	d := requested
	if d <= 0 {
		d = DefaultInterval(src)
	}
	d = clampInterval(d)
	if max := MaxIntervalFor(src); max > 0 && d > max {
		d = max
	}
	return d
}

// Condition is a parsed "until" clause.
type condition struct {
	kind     condKind
	exitCode int
	status   int
	statusX  int // status class: 2 for "2xx", 0 for an exact code
	text     string
	re       *regexp.Regexp
}

type condKind int

const (
	condTerminal condKind = iota // the source's own terminal state
	condExitZero
	condExitCode
	condStatus
	condStatusClass
	condBodyRegex
	condBodyContains
	condExists
	condFileRegex
	condFileContains
	condOutputRegex
	condOutputContains
)

// parseCondition validates until for the given source and target.
func parseCondition(src Source, target, until string) (condition, error) {
	until = strings.TrimSpace(until)
	switch src {
	case SourceJob, SourceProcess, SourceTime:
		if until != "" {
			return condition{}, fmt.Errorf("until is not supported for %s predicates: they fire on their own terminal state", src)
		}
		return condition{kind: condTerminal}, nil

	case SourceCommand:
		switch {
		case until == "" || until == "exit_zero":
			return condition{kind: condExitZero}, nil
		case strings.HasPrefix(until, "exit:"):
			n, err := parseNonNegative(until[len("exit:"):])
			if err != nil {
				return condition{}, fmt.Errorf("until %q: %w", until, err)
			}
			return condition{kind: condExitCode, exitCode: n}, nil
		case strings.HasPrefix(until, "regex:"):
			re, err := compileRegex(until[len("regex:"):])
			if err != nil {
				return condition{}, fmt.Errorf("until %q: %w", until, err)
			}
			return condition{kind: condOutputRegex, re: re}, nil
		case strings.HasPrefix(until, "contains:"):
			text := strings.TrimSpace(until[len("contains:"):])
			if text == "" {
				return condition{}, fmt.Errorf("until %q has no text after contains", until)
			}
			return condition{kind: condOutputContains, text: text}, nil
		default:
			return condition{}, fmt.Errorf("until %q is not valid for a command predicate (want exit_zero, exit:<n>, regex:<re>, or contains:<text>)", until)
		}

	case SourceHTTP:
		switch {
		case until == "":
			return condition{kind: condStatusClass, statusX: 2}, nil
		case strings.HasPrefix(until, "status:"):
			raw := strings.TrimSpace(until[len("status:"):])
			if len(raw) == 3 && strings.HasSuffix(raw, "xx") {
				n, err := parseNonNegative(strings.TrimSuffix(raw, "xx"))
				if err != nil || n < 1 || n > 5 {
					return condition{}, fmt.Errorf("until %q: status class must be 1xx through 5xx", until)
				}
				return condition{kind: condStatusClass, statusX: n}, nil
			}
			n, err := parseNonNegative(raw)
			if err != nil {
				return condition{}, fmt.Errorf("until %q: %w", until, err)
			}
			if n < 100 || n > 599 {
				return condition{}, fmt.Errorf("until %q: status must be 100..599", until)
			}
			return condition{kind: condStatus, status: n}, nil
		case strings.HasPrefix(until, "regex:"):
			re, err := compileRegex(until[len("regex:"):])
			if err != nil {
				return condition{}, fmt.Errorf("until %q: %w", until, err)
			}
			return condition{kind: condBodyRegex, re: re}, nil
		case strings.HasPrefix(until, "contains:"):
			text := strings.TrimSpace(until[len("contains:"):])
			if text == "" {
				return condition{}, fmt.Errorf("until %q has no text after contains", until)
			}
			return condition{kind: condBodyContains, text: text}, nil
		default:
			return condition{}, fmt.Errorf("until %q is not valid for an http predicate (want status:<code>, status:<n>xx, regex:<re>, or contains:<text>)", until)
		}

	case SourceFile:
		switch {
		case until == "" || until == "exists":
			return condition{kind: condExists}, nil
		case strings.HasPrefix(until, "regex:"):
			re, err := compileRegex(until[len("regex:"):])
			if err != nil {
				return condition{}, fmt.Errorf("until %q: %w", until, err)
			}
			return condition{kind: condFileRegex, re: re}, nil
		case strings.HasPrefix(until, "contains:"):
			text := strings.TrimSpace(until[len("contains:"):])
			if text == "" {
				return condition{}, fmt.Errorf("until %q has no text after contains", until)
			}
			return condition{kind: condFileContains, text: text}, nil
		default:
			return condition{}, fmt.Errorf("until %q is not valid for a file predicate (want exists, contains:<text>, or regex:<re>)", until)
		}
	}
	return condition{}, fmt.Errorf("unknown predicate source %q", src)
}

func parseNonNegative(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("missing number")
	}
	n := 0
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("%q is not a number", raw)
		}
		n = n*10 + int(r-'0')
		if n > 1_000_000 {
			return 0, fmt.Errorf("%q is too large", raw)
		}
	}
	return n, nil
}

func compileRegex(raw string) (*regexp.Regexp, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty regular expression")
	}
	re, err := regexp.Compile(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid regular expression: %w", err)
	}
	return re, nil
}

// validateHTTPTarget rejects a malformed URL before the first poll so a typo
// fails the call instead of burning the whole timeout. The SSRF guard runs in
// the injected probe.
func validateHTTPTarget(target string) error {
	u, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("http predicates require an http:// or https:// URL")
	}
	if u.Host == "" {
		return fmt.Errorf("URL has no host")
	}
	return nil
}

// parseSpec validates a raw predicate and returns its parsed pieces.
func parseSpec(s Spec) (Source, string, condition, error) {
	src, target, err := ParseOn(s.On)
	if err != nil {
		return "", "", condition{}, err
	}
	if src == SourceTime {
		dur, err := time.ParseDuration(target)
		if err != nil {
			return "", "", condition{}, fmt.Errorf("time predicate %q: %w", target, err)
		}
		if dur <= 0 {
			return "", "", condition{}, fmt.Errorf("time predicate %q must be a positive duration", target)
		}
	}
	if src == SourceHTTP {
		if err := validateHTTPTarget(target); err != nil {
			return "", "", condition{}, err
		}
	}
	cond, err := parseCondition(src, target, s.Until)
	if err != nil {
		return "", "", condition{}, err
	}
	return src, target, cond, nil
}

// --- resumable handle ---

type handlePayload struct {
	On    string `json:"on"`
	Until string `json:"until,omitempty"`
	Start int64  `json:"start"` // unix nanoseconds
}

// encodeHandle returns an opaque token that resumes the same logical wait.
// It is deliberately not a secret: it carries the predicate and the time the
// wait began so a timed-out call does not reset the caller's elapsed clock.
func encodeHandle(s Spec, start time.Time) string {
	raw, _ := json.Marshal(handlePayload{On: s.On, Until: s.Until, Start: start.UnixNano()})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// HandleSpec returns the predicate a handle names. It is the approval
// classifier's view of a handle-only call: a token is not an authorization
// grant, so a caller must be able to tell whether resuming it would run a
// command. It does not validate that the predicate is usable in this session.
func HandleSpec(token string) (Spec, error) {
	spec, _, err := decodeHandle(token, Spec{})
	return spec, err
}

// decodeHandle parses a token produced by encodeHandle. When the caller also
// supplied on/until they must agree with the token: silently waiting on a
// different predicate than the handle names would make the token a lie.
func decodeHandle(token string, s Spec) (Spec, time.Time, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return Spec{}, time.Time{}, fmt.Errorf("invalid handle: %w", err)
	}
	var p handlePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Spec{}, time.Time{}, fmt.Errorf("invalid handle: %w", err)
	}
	if p.On == "" || p.Start <= 0 {
		return Spec{}, time.Time{}, fmt.Errorf("invalid handle: missing predicate or start time")
	}
	if s.On != "" && s.On != p.On {
		return Spec{}, time.Time{}, fmt.Errorf("handle names %q, not %q", p.On, s.On)
	}
	if s.Until != "" && s.Until != p.Until {
		return Spec{}, time.Time{}, fmt.Errorf("handle names until %q, not %q", p.Until, s.Until)
	}
	return Spec{On: p.On, Until: p.Until}, time.Unix(0, p.Start), nil
}
