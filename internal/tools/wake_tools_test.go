package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/wake"
)

func testWakeManager(t *testing.T) *wake.Manager {
	t.Helper()
	m := wake.NewManager(wake.Options{
		Deps: wake.Deps{
			Now: time.Now,
			Glob: func(ctx context.Context, pattern string) ([]string, error) {
				return []string{"/w/ci-green"}, nil
			},
			ReadFile: func(ctx context.Context, path string) ([]byte, error) {
				return []byte("state: green\n"), nil
			},
			Exec: func(ctx context.Context, command string) (wake.ExecResult, error) {
				return wake.ExecResult{ExitCode: 1, Output: "not yet"}, nil
			},
		},
		DigestWindow: 10 * time.Millisecond,
		OnWake:       func(string) {},
	})
	t.Cleanup(m.Close)
	return m
}

func TestWaitForToolMissingOn(t *testing.T) {
	tool := &WaitForTool{Manager: testWakeManager(t)}
	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Fatal("wait_for accepted a call with neither on nor handle")
	}
	unconfigured := &WaitForTool{}
	if _, err := unconfigured.Execute(context.Background(), `{"on":"time:1s"}`); err == nil {
		t.Fatal("wait_for accepted a session with no wake engine")
	}
}

func TestWaitForToolFiresWithEvidence(t *testing.T) {
	tool := &WaitForTool{Manager: testWakeManager(t)}
	out, err := tool.Execute(context.Background(), `{"on":"file:*","until":"contains:green","timeout_seconds":1}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var res wake.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("result is not JSON: %v (%q)", err, out)
	}
	if !res.Fired || res.Evidence == nil || !strings.Contains(res.Evidence.Excerpt, "state: green") {
		t.Fatalf("result = %+v, want fired with file evidence", res)
	}
}

func TestWaitForToolDangerClassification(t *testing.T) {
	tool := &WaitForTool{}
	cases := map[string]bool{
		`{"on":"file:ci-green"}`:       false,
		`{"on":"job:bg-1"}`:            false,
		`{"on":"time:30s"}`:            false,
		`{"on":"http:https://x.test"}`: false,
		`{"on":"command:git status"}`:  true,
		`not-json`:                     true,
	}
	for args, want := range cases {
		if got := tool.IsDangerous(args); got != want {
			t.Errorf("IsDangerous(%s) = %v, want %v", args, got, want)
		}
	}
}

// TestWaitForToolClassifiesHandleResumes pins the approval rule for a
// handle-only call: the handle must be decoded and classified, because the
// token itself is not an authorization grant (and is trivially forgeable).
func TestWaitForToolClassifiesHandleResumes(t *testing.T) {
	tool := &WaitForTool{Manager: testWakeManager(t)}

	res, err := tool.Manager.Wait(context.Background(), wake.WaitRequest{
		On: "command:gh pr checks", Timeout: 10 * time.Millisecond, Interval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Handle == "" {
		t.Fatal("command wait did not produce a handle")
	}
	args, _ := json.Marshal(map[string]string{"handle": res.Handle})
	if !tool.IsDangerous(string(args)) {
		t.Fatal("a handle naming a command predicate was not classified as dangerous")
	}
	if got := tool.IsDangerous(`{"handle":"not-a-handle"}`); !got {
		t.Fatal("an unusable handle was not classified conservatively as dangerous")
	}

	// A file handle is not dangerous.
	fileMgr := wake.NewManager(wake.Options{Deps: wake.Deps{
		Now: time.Now,
		Glob: func(ctx context.Context, pattern string) ([]string, error) {
			return nil, nil
		},
	}})
	defer fileMgr.Close()
	res, err = fileMgr.Wait(context.Background(), wake.WaitRequest{
		On: "file:ci/green", Timeout: 10 * time.Millisecond, Interval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("file Wait: %v", err)
	}
	args, _ = json.Marshal(map[string]string{"handle": res.Handle})
	if tool.IsDangerous(string(args)) {
		t.Fatal("a handle naming a file predicate was classified as dangerous")
	}
}

func TestWatchToolArmListCancel(t *testing.T) {
	m := testWakeManager(t)
	tool := &WatchTool{Manager: m}

	out, err := tool.Execute(context.Background(), `{"action":"arm","on":"time:1h","id":"ci","expires_in_seconds":60}`)
	if err != nil {
		t.Fatalf("arm: %v", err)
	}
	var info wake.WatchInfo
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("arm result is not JSON: %v (%q)", err, out)
	}
	if info.ID != "ci" || info.State != wake.StateArmed {
		t.Fatalf("arm = %+v", info)
	}

	out, err = tool.Execute(context.Background(), `{"action":"list"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var list struct {
		Watches []wake.WatchInfo `json:"watches"`
		Armed   int              `json:"armed"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("list result is not JSON: %v (%q)", err, out)
	}
	if list.Armed != 1 || len(list.Watches) != 1 {
		t.Fatalf("list = %+v", list)
	}

	out, err = tool.Execute(context.Background(), `{"action":"cancel","id":"ci"}`)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if !strings.Contains(out, `"cancelled":true`) {
		t.Fatalf("cancel = %q", out)
	}
	out, err = tool.Execute(context.Background(), `{"action":"cancel","id":"ci"}`)
	if err != nil {
		t.Fatalf("second cancel: %v", err)
	}
	if !strings.Contains(out, `"cancelled":false`) {
		t.Fatalf("second cancel = %q, want an honest false", out)
	}
}

func TestWatchToolValidation(t *testing.T) {
	tool := &WatchTool{Manager: testWakeManager(t)}
	for _, args := range []string{
		`{"action":"arm"}`,
		`{"action":"cancel"}`,
		`{"action":"bogus"}`,
		`{"action":"arm","on":"time:not-a-duration"}`,
		`not-json`,
	} {
		if _, err := tool.Execute(context.Background(), args); err == nil {
			t.Errorf("watch accepted %s", args)
		}
	}
}

func TestWatchToolDangerClassification(t *testing.T) {
	tool := &WatchTool{}
	cases := map[string]bool{
		`{"action":"arm","on":"file:ci-green"}`:      false,
		`{"action":"arm","on":"command:gh pr view"}`: true,
		`{"action":"list"}`:                          false,
		`{"action":"cancel","id":"w1"}`:              false,
	}
	for args, want := range cases {
		if got := tool.IsDangerous(args); got != want {
			t.Errorf("IsDangerous(%s) = %v, want %v", args, got, want)
		}
	}
}
