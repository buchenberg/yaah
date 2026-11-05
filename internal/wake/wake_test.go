package wake

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/process"
)

// --- fakes ---

type fakeJobs struct {
	mu      sync.Mutex
	status  map[string]jobs.BackgroundJobStatus
	results map[string]string
}

func (f *fakeJobs) Status(id string) (jobs.BackgroundJobStatus, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.status[id]
	return st, ok
}

func (f *fakeJobs) Wait(ctx context.Context, id string) (jobs.BackgroundJobStatus, bool) {
	st, ok := f.Status(id)
	if !ok {
		return jobs.BackgroundJobStatus{}, false
	}
	return st, true
}

func (f *fakeJobs) Result(id string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.results[id]
	return r, ok
}

type fakeProcs struct{ procs map[string]*process.Info }

func (f *fakeProcs) Get(id string) *process.Info { return f.procs[id] }

func fileDeps(files map[string]string) Deps {
	return Deps{
		Glob: func(ctx context.Context, pattern string) ([]string, error) {
			var out []string
			for p := range files {
				out = append(out, p)
			}
			return out, nil
		},
		ReadFile: func(ctx context.Context, path string) ([]byte, error) {
			v, ok := files[path]
			if !ok {
				return nil, errors.New("not found")
			}
			return []byte(v), nil
		},
	}
}

// --- spec parsing ---

func TestParseOn(t *testing.T) {
	cases := []struct {
		in     string
		source Source
		target string
	}{
		{"job:bg-1", SourceJob, "bg-1"},
		{"process:proc-2", SourceProcess, "proc-2"},
		{"time:30s", SourceTime, "30s"},
		{"command:go test ./...", SourceCommand, "go test ./..."},
		{"http:https://example.com/health", SourceHTTP, "https://example.com/health"},
		{"file:tmp/ci-green", SourceFile, "tmp/ci-green"},
		{"file:C:\\tmp\\x.log", SourceFile, `C:\tmp\x.log`},
	}
	for _, tc := range cases {
		src, target, err := ParseOn(tc.in)
		if err != nil {
			t.Fatalf("ParseOn(%q): %v", tc.in, err)
		}
		if src != tc.source || target != tc.target {
			t.Errorf("ParseOn(%q) = (%s, %q), want (%s, %q)", tc.in, src, target, tc.source, tc.target)
		}
	}
	for _, bad := range []string{"", "job:", "nonsense", "bogus:x"} {
		if _, _, err := ParseOn(bad); err == nil {
			t.Errorf("ParseOn(%q) accepted an invalid predicate", bad)
		}
	}
}

func TestParseCondition(t *testing.T) {
	valid := []struct {
		source Source
		until  string
	}{
		{SourceCommand, ""},
		{SourceCommand, "exit_zero"},
		{SourceCommand, "exit:2"},
		{SourceCommand, "regex:PASS"},
		{SourceCommand, "contains:ok"},
		{SourceHTTP, ""},
		{SourceHTTP, "status:200"},
		{SourceHTTP, "status:3xx"},
		{SourceHTTP, "regex:<title>"},
		{SourceFile, ""},
		{SourceFile, "exists"},
		{SourceFile, "contains:done"},
		{SourceFile, "regex:go1\\.2[0-9]"},
		{SourceJob, ""},
	}
	for _, tc := range valid {
		if _, err := parseCondition(tc.source, "x", tc.until); err != nil {
			t.Errorf("parseCondition(%s, %q) = %v, want nil", tc.source, tc.until, err)
		}
	}
	invalid := []struct {
		source Source
		until  string
	}{
		{SourceCommand, "status:200"},
		{SourceCommand, "exit:"},
		{SourceCommand, "regex:("},
		{SourceHTTP, "exit:0"},
		{SourceHTTP, "status:abc"},
		{SourceFile, "exit_zero"},
		{SourceJob, "exists"},
	}
	for _, tc := range invalid {
		if _, err := parseCondition(tc.source, "x", tc.until); err == nil {
			t.Errorf("parseCondition(%s, %q) accepted an invalid condition", tc.source, tc.until)
		}
	}
}

func TestMaxIntervalFor(t *testing.T) {
	cases := map[Source]time.Duration{
		SourceJob:     0,
		SourceTime:    time.Second,
		SourceFile:    5 * time.Second,
		SourceHTTP:    15 * time.Second,
		SourceCommand: MaxInterval,
		SourceProcess: MaxInterval,
	}
	for src, want := range cases {
		if got := MaxIntervalFor(src); got != want {
			t.Errorf("MaxIntervalFor(%s) = %v, want %v", src, got, want)
		}
	}
}

func TestIntervalForClampsToSourceCeiling(t *testing.T) {
	if got := intervalFor(SourceFile, 30*time.Second); got != 5*time.Second {
		t.Errorf("file interval = %v, want the 5s ceiling", got)
	}
	if got := intervalFor(SourceHTTP, time.Second); got != time.Second {
		t.Errorf("http interval = %v, want the requested 1s", got)
	}
	if got := intervalFor(SourceCommand, time.Minute); got != MaxInterval {
		t.Errorf("command interval = %v, want %v", got, MaxInterval)
	}
	if got := intervalFor(SourceFile, 0); got != 500*time.Millisecond {
		t.Errorf("file default interval = %v, want 500ms", got)
	}
}

func TestNextIntervalCapsPerSource(t *testing.T) {
	d := 500 * time.Millisecond
	for i := 0; i < 10; i++ {
		d = nextInterval(d, MaxIntervalFor(SourceFile))
	}
	if d != 5*time.Second {
		t.Fatalf("file backoff settled at %v, want 5s", d)
	}
}

// fakeClock drives Wait's poll delays without sleeping. After publishes the
// requested delay and records the channel the test advances.
type fakeClock struct {
	mu       sync.Mutex
	now      time.Time
	last     chan time.Time
	requests chan time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Now(), requests: make(chan time.Duration, 16)}
}

func (f *fakeClock) Now() time.Time { return f.now }

func (f *fakeClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	f.mu.Lock()
	f.last = ch
	f.mu.Unlock()
	f.requests <- d
	return ch
}

func (f *fakeClock) AfterFunc(time.Duration, func()) Timer { return fakeTimer{} }

type fakeTimer struct{}

func (fakeTimer) Stop() bool { return true }

func (f *fakeClock) advance() {
	f.mu.Lock()
	ch := f.last
	f.mu.Unlock()
	if ch != nil {
		ch <- f.now
	}
}

func TestWaitBackoffUsesTheSourceCeiling(t *testing.T) {
	clock := newFakeClock()
	m := NewManager(Options{Deps: Deps{Clock: clock, Glob: func(context.Context, string) ([]string, error) {
		return nil, nil // never fires
	}}})
	defer m.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = m.Wait(ctx, WaitRequest{On: "file:never", Timeout: 5 * time.Second})
	}()

	want := []time.Duration{
		500 * time.Millisecond,
		time.Second,
		2 * time.Second,
		4 * time.Second,
		5 * time.Second,
		5 * time.Second,
	}
	for _, expected := range want {
		select {
		case got := <-clock.requests:
			if got != expected {
				t.Fatalf("poll delay = %v, want %v", got, expected)
			}
			clock.advance()
		case <-time.After(2 * time.Second):
			t.Fatalf("Wait did not request a poll for %v", expected)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after cancel")
	}
}

func TestParseSpecRejectsNonPositiveTime(t *testing.T) {
	for _, on := range []string{"time:0s", "time:-5s"} {
		if _, _, _, err := parseSpec(Spec{On: on}); err == nil {
			t.Errorf("parseSpec(%q) accepted a non-positive duration", on)
		}
	}
}

func TestHandleRoundTrip(t *testing.T) {
	spec := Spec{On: "file:ci/green", Until: "exists"}
	start := time.Unix(1700000000, 123)
	h := encodeHandle(spec, start)
	got, gotStart, err := decodeHandle(h, Spec{})
	if err != nil {
		t.Fatalf("decodeHandle: %v", err)
	}
	if got != spec || !gotStart.Equal(start) {
		t.Fatalf("round trip = (%+v, %v), want (%+v, %v)", got, gotStart, spec, start)
	}
	if _, _, err := decodeHandle(h, Spec{On: "file:other"}); err == nil {
		t.Error("decodeHandle accepted a handle for a different predicate")
	}
	if _, _, err := decodeHandle("not-base64", Spec{}); err == nil {
		t.Error("decodeHandle accepted garbage")
	}
}

// --- engine ---

func TestEngineTime(t *testing.T) {
	now := time.Unix(1000, 0)
	d := Deps{Now: func() time.Time { return now }}
	fired, ev, err := d.evaluate(context.Background(), Spec{On: "time:30s"}, now.Add(-31*time.Second))
	if err != nil || !fired {
		t.Fatalf("elapsed time predicate: fired=%v err=%v evidence=%+v", fired, err, ev)
	}
	fired, _, err = d.evaluate(context.Background(), Spec{On: "time:30s"}, now)
	if err != nil || fired {
		t.Fatalf("fresh time predicate fired early: fired=%v err=%v", fired, err)
	}
}

func TestEngineCommand(t *testing.T) {
	now := time.Now()
	d := Deps{
		Now: func() time.Time { return now },
		Exec: func(ctx context.Context, command string) (ExecResult, error) {
			return ExecResult{ExitCode: 0, Output: "build ok\nPASS\n"}, nil
		},
	}
	for _, until := range []string{"", "exit_zero", "regex:PASS", "contains:PASS"} {
		fired, ev, err := d.evaluate(context.Background(), Spec{On: "command:make", Until: until}, now)
		if err != nil || !fired {
			t.Errorf("until %q: fired=%v err=%v evidence=%+v", until, fired, err, ev)
		}
	}
	fired, _, err := d.evaluate(context.Background(), Spec{On: "command:make", Until: "exit:2"}, now)
	if err != nil || fired {
		t.Errorf("exit:2 fired on a zero exit: fired=%v err=%v", fired, err)
	}
}

func TestEngineHTTP(t *testing.T) {
	now := time.Now()
	d := Deps{
		Now: func() time.Time { return now },
		HTTP: func(ctx context.Context, url string) (HTTPResult, error) {
			return HTTPResult{Status: 200, Body: "<html>ready</html>"}, nil
		},
	}
	for _, until := range []string{"", "status:200", "status:2xx", "regex:ready", "contains:ready"} {
		fired, _, err := d.evaluate(context.Background(), Spec{On: "http:https://example.com", Until: until}, now)
		if err != nil || !fired {
			t.Errorf("until %q: fired=%v err=%v", until, fired, err)
		}
	}
	fired, _, err := d.evaluate(context.Background(), Spec{On: "http:https://example.com", Until: "status:503"}, now)
	if err != nil || fired {
		t.Errorf("status:503 fired on a 200: fired=%v err=%v", fired, err)
	}
}

func TestEngineFile(t *testing.T) {
	now := time.Now()
	d := fileDeps(map[string]string{
		"/w/ci-green": "state: green\n",
	})
	fired, ev, err := d.evaluate(context.Background(), Spec{On: "file:ci*", Until: "exists"}, now)
	if err != nil || !fired || !strings.Contains(ev.Render(), "/w/ci-green") {
		t.Fatalf("exists: fired=%v err=%v evidence=%+v", fired, err, ev)
	}
	fired, ev, err = d.evaluate(context.Background(), Spec{On: "file:ci*", Until: "contains:green"}, now)
	if err != nil || !fired || !strings.Contains(ev.Excerpt, "state: green") {
		t.Fatalf("contains: fired=%v err=%v evidence=%+v", fired, err, ev)
	}
	fired, _, err = d.evaluate(context.Background(), Spec{On: "file:ci*", Until: "contains:red"}, now)
	if err != nil || fired {
		t.Fatalf("contains:red fired: fired=%v err=%v", fired, err)
	}
}

func TestEngineFileUsesBoundedReader(t *testing.T) {
	d := Deps{
		Now: time.Now,
		Glob: func(ctx context.Context, pattern string) ([]string, error) {
			return []string{"/w/ci-green"}, nil
		},
		ReadFileLimit: func(ctx context.Context, path string, max int) ([]byte, error) {
			if max != fileReadCap {
				t.Errorf("bounded read cap = %d, want %d", max, fileReadCap)
			}
			return []byte("state: green\n"), nil
		},
		ReadFile: func(ctx context.Context, path string) ([]byte, error) {
			t.Fatal("file predicate used the unbounded reader despite a bounded one")
			return nil, nil
		},
	}
	fired, _, err := d.evaluate(context.Background(), Spec{On: "file:*", Until: "contains:green"}, time.Now())
	if err != nil || !fired {
		t.Fatalf("contains: fired=%v err=%v", fired, err)
	}
}

func TestEngineJobIncludesResult(t *testing.T) {
	now := time.Now()
	d := Deps{
		Now: func() time.Time { return now },
		Jobs: &fakeJobs{
			status:  map[string]jobs.BackgroundJobStatus{"bg-1": {ID: "bg-1", Status: jobs.BGStatusCompleted}},
			results: map[string]string{"bg-1": "the report body"},
		},
	}
	fired, ev, err := d.evaluate(context.Background(), Spec{On: "job:bg-1"}, now)
	if err != nil || !fired {
		t.Fatalf("job: fired=%v err=%v", fired, err)
	}
	if !strings.Contains(ev.Result, "the report body") {
		t.Errorf("job evidence omits the result: %+v", ev)
	}
	if _, _, err := d.evaluate(context.Background(), Spec{On: "job:missing"}, now); err == nil {
		t.Error("unknown job id was not a terminal error")
	}
}

func TestEngineProcess(t *testing.T) {
	now := time.Now()
	d := Deps{Now: func() time.Time { return now }, Processes: &fakeProcs{procs: map[string]*process.Info{
		"proc-1": {ID: "proc-1", Command: "make"},
	}}}
	fired, ev, err := d.evaluate(context.Background(), Spec{On: "process:proc-1"}, now)
	if err != nil || !fired {
		t.Fatalf("process: fired=%v err=%v evidence=%+v", fired, err, ev)
	}
	if _, _, err := d.evaluate(context.Background(), Spec{On: "process:nope"}, now); err == nil {
		t.Error("unknown process id was not a terminal error")
	}
}

// --- manager: blocking wait ---

func TestManagerWaitFires(t *testing.T) {
	m := NewManager(Options{Deps: fileDeps(map[string]string{"/w/go": ""})})
	defer m.Close()
	res, err := m.Wait(context.Background(), WaitRequest{On: "file:*", Timeout: time.Second})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !res.Fired || res.Evidence == nil {
		t.Fatalf("Wait = %+v, want fired with evidence", res)
	}
}

func TestManagerWaitTimeoutThenResume(t *testing.T) {
	files := map[string]string{}
	m := NewManager(Options{Deps: fileDeps(files)})
	defer m.Close()

	res, err := m.Wait(context.Background(), WaitRequest{
		On: "file:*", Timeout: 40 * time.Millisecond, Interval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !res.TimedOut || res.Fired || res.Handle == "" {
		t.Fatalf("Wait = %+v, want a timeout with a handle", res)
	}
	if res.Evidence == nil {
		t.Fatal("timed-out wait lost the last attempt's evidence")
	}

	files["/w/late"] = ""
	res, err = m.Wait(context.Background(), WaitRequest{
		Handle: res.Handle, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("resumed Wait: %v", err)
	}
	if !res.Fired {
		t.Fatalf("resumed Wait = %+v, want fired once the file appeared", res)
	}
}

func TestManagerWaitInterrupted(t *testing.T) {
	m := NewManager(Options{Deps: fileDeps(nil)})
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	res, err := m.Wait(ctx, WaitRequest{On: "file:none", Timeout: time.Second, Interval: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !res.Interrupted || res.TimedOut {
		t.Fatalf("Wait = %+v, want interrupted (not timed out)", res)
	}
}

func TestManagerWaitUnknownJobIsAnError(t *testing.T) {
	m := NewManager(Options{Deps: Deps{Jobs: &fakeJobs{status: map[string]jobs.BackgroundJobStatus{}}}})
	defer m.Close()
	if _, err := m.Wait(context.Background(), WaitRequest{On: "job:bg-9", Timeout: time.Second}); err == nil {
		t.Fatal("Wait accepted an unknown job id")
	}
}

// --- manager: watches ---

func waitForDigest(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a wake digest")
		return ""
	}
}

func TestManagerArmFiresAndDigests(t *testing.T) {
	digests := make(chan string, 4)
	m := NewManager(Options{
		Deps:         Deps{Now: time.Now},
		DigestWindow: 20 * time.Millisecond,
		OnWake:       func(s string) { digests <- s },
	})
	defer m.Close()

	info, err := m.Arm(ArmRequest{On: "time:30ms", ExpiresIn: time.Second})
	if err != nil {
		t.Fatalf("Arm: %v", err)
	}
	if info.ID == "" || info.State != StateArmed {
		t.Fatalf("Arm = %+v, want an armed watch with an id", info)
	}
	msg := waitForDigest(t, digests)
	if !strings.Contains(msg, info.ID) || !strings.Contains(msg, "time:30ms") {
		t.Fatalf("digest does not name the watch: %q", msg)
	}
	if !strings.Contains(msg, "harness wake") {
		t.Errorf("digest lacks the harness-wake framing: %q", msg)
	}
}

func TestManagerBatchesSimultaneousFires(t *testing.T) {
	digests := make(chan string, 4)
	m := NewManager(Options{
		Deps:         Deps{Now: time.Now},
		DigestWindow: 150 * time.Millisecond,
		OnWake:       func(s string) { digests <- s },
	})
	defer m.Close()

	for _, d := range []string{"time:20ms", "time:25ms"} {
		if _, err := m.Arm(ArmRequest{On: d, ExpiresIn: time.Second}); err != nil {
			t.Fatalf("Arm(%s): %v", d, err)
		}
	}
	msg := waitForDigest(t, digests)
	if !strings.Contains(msg, "2 armed watches fired") {
		t.Fatalf("digest = %q, want one batch of two watches", msg)
	}
	select {
	case extra := <-digests:
		t.Fatalf("second digest unexpectedly delivered: %q", extra)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestManagerCancel(t *testing.T) {
	m := NewManager(Options{Deps: Deps{Now: time.Now}, DigestWindow: 10 * time.Millisecond})
	defer m.Close()
	info, err := m.Arm(ArmRequest{On: "time:1h", ExpiresIn: time.Hour})
	if err != nil {
		t.Fatalf("Arm: %v", err)
	}
	if !m.Cancel(info.ID) {
		t.Fatal("Cancel returned false for an armed watch")
	}
	if m.Cancel(info.ID) {
		t.Fatal("Cancel returned true for an unknown watch")
	}
	if got := m.List(); len(got) != 0 {
		t.Fatalf("List after cancel = %+v, want empty", got)
	}
}

func TestManagerRejectsBadPredicate(t *testing.T) {
	m := NewManager(Options{Deps: Deps{Now: time.Now}})
	defer m.Close()
	for _, on := range []string{"", "job:", "bogus:x", "time:not-a-duration"} {
		if _, err := m.Arm(ArmRequest{On: on}); err == nil {
			t.Errorf("Arm(%q) accepted an invalid predicate", on)
		}
	}
}

func TestManagerMaxWatches(t *testing.T) {
	m := NewManager(Options{Deps: Deps{Now: time.Now}, MaxWatches: 1})
	defer m.Close()
	if _, err := m.Arm(ArmRequest{On: "time:1h"}); err != nil {
		t.Fatalf("first Arm: %v", err)
	}
	if _, err := m.Arm(ArmRequest{On: "time:1h"}); err == nil {
		t.Fatal("Arm accepted a second watch past the limit")
	}
}

// --- persistence + rehydrate ---

type fakeStore struct {
	mu   sync.Mutex
	rows map[string]WatchRecord
}

func newFakeStore() *fakeStore { return &fakeStore{rows: map[string]WatchRecord{}} }

func (s *fakeStore) SaveWatch(sessionID string, rec WatchRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[rec.ID] = rec
	return nil
}

func (s *fakeStore) ListWatches(sessionID string) ([]WatchRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]WatchRecord, 0, len(s.rows))
	for _, r := range s.rows {
		out = append(out, r)
	}
	return out, nil
}

func (s *fakeStore) DeleteWatch(sessionID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, id)
	return nil
}

func TestManagerPersistsAndDeletesOnDelivery(t *testing.T) {
	store := newFakeStore()
	digests := make(chan string, 1)
	m := NewManager(Options{
		SessionID:    "sess-1",
		Deps:         Deps{Now: time.Now},
		Store:        store,
		DigestWindow: 20 * time.Millisecond,
		OnWake:       func(s string) { digests <- s },
	})
	defer m.Close()

	info, err := m.Arm(ArmRequest{On: "time:20ms", ExpiresIn: time.Second})
	if err != nil {
		t.Fatalf("Arm: %v", err)
	}
	store.mu.Lock()
	armed, ok := store.rows[info.ID]
	store.mu.Unlock()
	if !ok || armed.State != StateArmed {
		t.Fatalf("watch was not persisted as armed: %+v", armed)
	}
	_ = waitForDigest(t, digests)
	deadline := time.Now().Add(time.Second)
	for {
		store.mu.Lock()
		_, still := store.rows[info.ID]
		store.mu.Unlock()
		if !still {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("delivered watch was not deleted from the store")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestManagerRehydrateReportsMissedWakes(t *testing.T) {
	store := newFakeStore()
	now := time.Now()
	_ = store.SaveWatch("sess-1", WatchRecord{
		ID: "w1", On: "time:1m", State: StateFired, Evidence: Evidence{Kind: "watch", Detail: "fired while away"},
		ArmedAt: now.Add(-2 * time.Minute).UnixNano(), FiredAt: now.UnixNano(),
	})
	_ = store.SaveWatch("sess-1", WatchRecord{
		ID: "w2", On: "job:bg-1", State: StateArmed,
		ArmedAt: now.UnixNano(), ExpiresAt: now.Add(time.Hour).UnixNano(),
	})
	digests := make(chan string, 1)
	m := NewManager(Options{
		SessionID:    "sess-1",
		Deps:         Deps{Now: time.Now},
		Store:        store,
		DigestWindow: 20 * time.Millisecond,
		OnWake:       func(s string) { digests <- s },
	})
	defer m.Close()
	if err := m.Rehydrate(context.Background()); err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	msg := waitForDigest(t, digests)
	for _, want := range []string{"w1", "w2", "fired while away", "process-local"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missed-wake digest does not mention %q: %q", want, msg)
		}
	}
}

func TestManagerRehydrateRearmsTimeWatch(t *testing.T) {
	store := newFakeStore()
	now := time.Now()
	// Armed 10s ago for one second: it elapsed while the session was down,
	// so re-arming must fire promptly rather than wait another second.
	_ = store.SaveWatch("sess-1", WatchRecord{
		ID: "w1", On: "time:1s", State: StateArmed,
		ArmedAt:   now.Add(-10 * time.Second).UnixNano(),
		ExpiresAt: now.Add(time.Hour).UnixNano(),
	})
	digests := make(chan string, 1)
	m := NewManager(Options{
		SessionID:    "sess-1",
		Deps:         Deps{Now: time.Now},
		Store:        store,
		DigestWindow: 20 * time.Millisecond,
		OnWake:       func(s string) { digests <- s },
	})
	defer m.Close()
	start := time.Now()
	if err := m.Rehydrate(context.Background()); err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	_ = waitForDigest(t, digests)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("re-armed elapsed time watch took %v to fire", elapsed)
	}
}

// TestManagerCloseDropsPendingDigest pins the Close contract: a fired watch
// whose digest window has not elapsed is not delivered after Close returns.
func TestManagerCloseDropsPendingDigest(t *testing.T) {
	wakes := make(chan string, 4)
	m := NewManager(Options{
		Deps:         Deps{Now: time.Now},
		DigestWindow: time.Hour,
		OnWake:       func(s string) { wakes <- s },
	})
	if _, err := m.Arm(ArmRequest{On: "time:10ms", ExpiresIn: time.Second}); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		list := m.List()
		if len(list) == 1 && list[0].State == StateFired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("watch did not fire into the pending digest: %+v", list)
		}
		time.Sleep(5 * time.Millisecond)
	}
	m.Close()
	select {
	case s := <-wakes:
		t.Fatalf("digest delivered after Close: %q", s)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestManagerCloseStopsPollers(t *testing.T) {
	m := NewManager(Options{Deps: Deps{Now: time.Now}, DigestWindow: time.Hour})
	info, err := m.Arm(ArmRequest{On: "time:1h"})
	if err != nil {
		t.Fatalf("Arm: %v", err)
	}
	done := make(chan struct{})
	go func() { m.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return promptly")
	}
	if m.Cancel(info.ID) {
		t.Fatal("Cancel on a closed manager reported a live watch")
	}
}
