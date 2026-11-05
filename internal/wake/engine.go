package wake

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/process"
)

// JobSource is the slice of the background-job manager the engine needs.
// *jobs.BackgroundJobs satisfies it.
type JobSource interface {
	Status(id string) (jobs.BackgroundJobStatus, bool)
	Wait(ctx context.Context, id string) (jobs.BackgroundJobStatus, bool)
	Result(id string) (string, bool)
}

// ProcessSource is the slice of the background-process manager the engine
// needs. *process.Manager satisfies it.
type ProcessSource interface {
	Get(id string) *process.Info
}

// ExecResult is one command evaluation.
type ExecResult struct {
	ExitCode int
	Output   string
}

// HTTPResult is one HTTP evaluation.
type HTTPResult struct {
	Status int
	Body   string
}

// Deps are the side-effect seams the engine evaluates predicates through.
// Every field is optional: a missing dependency makes that source report a
// clear error instead of panicking.
type Deps struct {
	Jobs      JobSource
	Processes ProcessSource

	Exec func(ctx context.Context, command string) (ExecResult, error)
	HTTP func(ctx context.Context, url string) (HTTPResult, error)
	Glob func(ctx context.Context, pattern string) ([]string, error)
	// ReadFile is used by file contains/regex conditions.
	ReadFile func(ctx context.Context, path string) ([]byte, error)
	// ReadFileLimit, when non-nil, returns at most max bytes. It keeps a
	// content predicate from materializing a large file on every poll.
	ReadFileLimit func(ctx context.Context, path string, max int) ([]byte, error)

	Now func() time.Time
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

const (
	// evidenceCap bounds any evidence string the engine returns.
	evidenceCap = 2000
	// commandAttemptTimeout bounds one command evaluation. A command that
	// runs longer belongs in background_process + process:<id>.
	commandAttemptTimeout = 60 * time.Second
	// httpAttemptTimeout bounds one HTTP probe.
	httpAttemptTimeout = 30 * time.Second
	// fileReadCap bounds how much of one file a contains/regex condition
	// inspects, and fileSampleCap bounds how many matching files it reads.
	fileReadCap   = 256 * 1024
	fileSampleCap = 50
)

// evaluate runs one predicate check. A non-nil terminal error means the
// predicate can never succeed (unknown job, missing dependency, malformed
// query); a nil error with fired=false means "not yet".
func (d Deps) evaluate(ctx context.Context, spec Spec, start time.Time) (fired bool, evidence string, terminal error) {
	src, target, cond, err := parseSpec(spec)
	if err != nil {
		return false, "", err
	}
	switch src {
	case SourceJob:
		return d.evalJob(target)
	case SourceProcess:
		return d.evalProcess(ctx, target)
	case SourceTime:
		return d.evalTime(target, start)
	case SourceCommand:
		return d.evalCommand(ctx, target, cond)
	case SourceHTTP:
		return d.evalHTTP(ctx, target, cond)
	case SourceFile:
		return d.evalFile(ctx, target, cond)
	default:
		return false, "", fmt.Errorf("unknown predicate source %q", src)
	}
}

func (d Deps) evalJob(id string) (bool, string, error) {
	if d.Jobs == nil {
		return false, "", fmt.Errorf("job predicates are not available in this session")
	}
	st, ok := d.Jobs.Status(id)
	if !ok {
		return false, "", fmt.Errorf("unknown job_id %q: it was never dispatched in this session, or its finished record was reaped", id)
	}
	if st.StillRunning {
		return false, fmt.Sprintf("job %s is still running (%s)", id, time.Duration(st.DurationMs)*time.Millisecond), nil
	}
	ev := jobEvidence{BackgroundJobStatus: st}
	if result, ok := d.Jobs.Result(id); ok {
		ev.Result = clip(result, evidenceCap)
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return true, fmt.Sprintf("job %s is %s", id, st.Status), nil
	}
	return true, string(data), nil
}

// jobEvidence is the JSON payload returned when a job predicate fires: the
// terminal snapshot plus the sub-agent's final output, so the waiter has the
// result rather than only a status label.
type jobEvidence struct {
	jobs.BackgroundJobStatus
	Result string `json:"result,omitempty"`
}

func (d Deps) evalProcess(ctx context.Context, id string) (bool, string, error) {
	if d.Processes == nil {
		return false, "", fmt.Errorf("process predicates are not available in this session")
	}
	if err := ctx.Err(); err != nil {
		return false, "", err
	}
	p := d.Processes.Get(id)
	if p == nil {
		return false, "", fmt.Errorf("unknown process id %q: start one with background_process and use its id", id)
	}
	status := p.Status()
	if status == "running" {
		return false, fmt.Sprintf("process %s is still running", id), nil
	}
	logs := p.Logs()
	if logs == "" {
		logs = "(no output)"
	}
	return true, fmt.Sprintf("process %s is %s; output tail:\n%s", id, status, tail(logs, evidenceCap)), nil
}

func (d Deps) evalTime(target string, start time.Time) (bool, string, error) {
	dur, err := time.ParseDuration(target)
	if err != nil {
		return false, "", fmt.Errorf("time predicate %q: %w", target, err)
	}
	elapsed := d.now().Sub(start)
	if elapsed >= dur {
		return true, fmt.Sprintf("waited %s (target %s)", elapsed.Round(time.Millisecond), dur), nil
	}
	return false, fmt.Sprintf("waited %s of %s", elapsed.Round(time.Millisecond), dur), nil
}

func (d Deps) evalCommand(ctx context.Context, command string, cond condition) (bool, string, error) {
	if d.Exec == nil {
		return false, "", fmt.Errorf("command predicates are not available in this session")
	}
	// Each attempt is bounded independently: a hung command must not consume
	// the caller's whole wait budget, and a non-zero exit is the common case
	// (it is the thing being polled for).
	attempt, cancel := context.WithTimeout(ctx, commandAttemptTimeout)
	defer cancel()
	res, err := d.Exec(attempt, command)
	ev := fmt.Sprintf("exit=%d\n%s", res.ExitCode, tail(res.Output, evidenceCap))
	if attempt.Err() != nil {
		// This attempt hit its own bound (or the caller's). That is not a
		// reason to abort the whole wait: keep polling until the caller's
		// timeout, and say why this attempt was killed.
		return false, "command attempt interrupted (" + attempt.Err().Error() + ")\n" + ev, nil
	}
	if err != nil && res.ExitCode < 0 {
		return false, "", fmt.Errorf("command could not run: %w", err)
	}
	switch cond.kind {
	case condExitZero:
		return res.ExitCode == 0, ev, nil
	case condExitCode:
		return res.ExitCode == cond.exitCode, ev, nil
	case condOutputRegex:
		return cond.re.MatchString(res.Output), ev, nil
	case condOutputContains:
		return strings.Contains(res.Output, cond.text), ev, nil
	default:
		return false, "", fmt.Errorf("unsupported command condition")
	}
}

func (d Deps) evalHTTP(ctx context.Context, url string, cond condition) (bool, string, error) {
	if d.HTTP == nil {
		return false, "", fmt.Errorf("http predicates are not available in this session")
	}
	attempt, cancel := context.WithTimeout(ctx, httpAttemptTimeout)
	defer cancel()
	res, err := d.HTTP(attempt, url)
	if err != nil {
		// Network errors are expected while a service is starting; report
		// them as evidence and keep polling until the caller's bound.
		return false, "request error: " + clip(err.Error(), evidenceCap), nil
	}
	ev := fmt.Sprintf("HTTP %d\n%s", res.Status, tail(res.Body, evidenceCap))
	switch cond.kind {
	case condStatus:
		return res.Status == cond.status, ev, nil
	case condStatusClass:
		return res.Status/100 == cond.statusX, ev, nil
	case condBodyRegex:
		return res.Status < 400 && cond.re.MatchString(res.Body), ev, nil
	case condBodyContains:
		return res.Status < 400 && strings.Contains(res.Body, cond.text), ev, nil
	default:
		return false, "", fmt.Errorf("unsupported http condition")
	}
}

func (d Deps) evalFile(ctx context.Context, pattern string, cond condition) (bool, string, error) {
	if d.Glob == nil {
		return false, "", fmt.Errorf("file predicates are not available in this session")
	}
	paths, err := d.Glob(ctx, pattern)
	if err != nil {
		return false, "", fmt.Errorf("file predicate %q: %w", pattern, err)
	}
	if len(paths) == 0 {
		return false, fmt.Sprintf("no files match %q", pattern), nil
	}
	summary := fmt.Sprintf("%d file(s) match %q: %s", len(paths), pattern, strings.Join(head(paths, 5), ", "))
	switch cond.kind {
	case condExists:
		return true, summary, nil
	case condFileContains, condFileRegex:
		if d.ReadFile == nil && d.ReadFileLimit == nil {
			return false, "", fmt.Errorf("file content conditions need a workspace reader")
		}
		sampled := paths
		truncated := false
		if len(sampled) > fileSampleCap {
			sampled = sampled[:fileSampleCap]
			truncated = true
		}
		for _, p := range sampled {
			data, rerr := d.readFileCapped(ctx, p, fileReadCap)
			if rerr != nil {
				continue
			}
			text := string(data)
			matched := strings.Contains(text, cond.text)
			if cond.kind == condFileRegex {
				matched = cond.re.MatchString(text)
			}
			if matched {
				return true, fmt.Sprintf("%s matched %q:\n%s", p, condLabel(cond), clip(text, evidenceCap)), nil
			}
		}
		if truncated {
			summary += fmt.Sprintf(" (inspected the first %d of %d files)", fileSampleCap, len(paths))
		}
		return false, summary + "; none matched " + condLabel(cond), nil
	default:
		return false, "", fmt.Errorf("unsupported file condition")
	}
}

// readFileCapped prefers the bounded reader so a large file is never fully
// materialized; a Deps without one still gets the cap, just after the read.
func (d Deps) readFileCapped(ctx context.Context, path string, max int) ([]byte, error) {
	if d.ReadFileLimit != nil {
		return d.ReadFileLimit(ctx, path, max)
	}
	data, err := d.ReadFile(ctx, path)
	if err != nil {
		return nil, err
	}
	if len(data) > max {
		data = data[:max]
	}
	return data, nil
}

func condLabel(c condition) string {
	switch c.kind {
	case condFileRegex:
		return "regex:" + c.re.String()
	case condFileContains:
		return "contains:" + c.text
	default:
		return ""
	}
}

// clip bounds s by bytes, preserving valid UTF-8 at the cut.
func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "") + "…[truncated]"
}

// tail bounds s to its last max bytes: the newest output is the diagnostic
// part of a log or command result. Earlier output is marked as dropped.
func tail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := len(s) - max
	// Do not begin in the middle of a UTF-8 continuation byte.
	for cut < len(s) && (s[cut]&0xC0) == 0x80 {
		cut++
	}
	return "...[earlier output dropped]...\n" + s[cut:]
}

// head returns at most n elements of xs.
func head(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return xs[:n]
}
