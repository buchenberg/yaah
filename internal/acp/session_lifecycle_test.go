package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

// wireStore installs canned session/list and session/delete callbacks on
// the harness server (must happen before traffic starts).
func wireStore(h *acpHarness, pages [][]SessionInfo, deleted *[]string) {
	h.server.ListSessions = func(cwdFilter, cursor string) ([]SessionInfo, string, error) {
		switch cursor {
		case "":
			return append([]SessionInfo(nil), pages[0]...), "page-2", nil
		case "page-2":
			if len(pages) > 1 {
				return append([]SessionInfo(nil), pages[1]...), "", nil
			}
			return []SessionInfo{}, "", nil
		default:
			return nil, "", fmt.Errorf("%w: cursor %q", ErrInvalidCursor, cursor)
		}
	}
	h.server.DeleteSession = func(id string) error {
		if deleted != nil {
			*deleted = append(*deleted, id)
		}
		// Unknown ids surface as ErrNoSession: session/delete succeeds
		// silently for them.
		return fmt.Errorf("%w: %s", ErrNoSession, id)
	}
}

// listResult decodes a session/list result.
func listResult(t *testing.T, msg Message) ListSessionsResult {
	t.Helper()
	if msg.Error != nil {
		t.Fatalf("session/list error: %+v", msg.Error)
	}
	var result ListSessionsResult
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		t.Fatalf("session/list result: %v (%s)", err, msg.Result)
	}
	return result
}

// TestServer_capabilitiesReflectWiring pins the advertisement rule: a
// capability is advertised only when its plumbing is wired, so clients
// never call a method the server cannot execute.
func TestServer_capabilitiesReflectWiring(t *testing.T) {
	s := NewServer(func(BuildOptions) (Session, error) { return nil, fmt.Errorf("not used") }, "test", nil)

	caps := s.capabilities()
	if !caps.LoadSession {
		t.Error("loadSession must always be advertised")
	}
	if caps.Session == nil {
		t.Fatal("sessionCapabilities missing; resume/close have no store dependency")
	}
	if caps.Session.List != nil {
		t.Error("list advertised without a ListSessions callback")
	}
	if caps.Session.Delete != nil {
		t.Error("delete advertised without a DeleteSession callback")
	}
	if caps.Session.Resume == nil || caps.Session.Close == nil {
		t.Error("resume/close must be advertised: they need no store plumbing")
	}

	s.ListSessions = func(string, string) ([]SessionInfo, string, error) { return nil, "", nil }
	s.DeleteSession = func(string) error { return nil }
	caps = s.capabilities()
	if caps.Session.List == nil || caps.Session.Delete == nil {
		t.Error("wired callbacks must advertise list/delete")
	}

	// The wire shape the capability tests bind on: each flip is {}.
	raw, err := json.Marshal(caps.Session)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"list":{}`, `"resume":{}`, `"close":{}`, `"delete":{}`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("sessionCapabilities wire missing %s: %s", want, raw)
		}
	}
}

// TestServer_sessionResumeAttachesWithoutReplay pins the resume
// contract: no history replay precedes the response, the response is the
// empty object, and a foreign (store-unknown) id is invalid params.
func TestServer_sessionResumeAttachesWithoutReplay(t *testing.T) {
	t.Run("live attach, no history replay", func(t *testing.T) {
		sess := &fakeSession{
			id: "sess-test",
			history: []types.Message{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi there"},
			},
		}
		h := newACPHarness(t, sess)
		initialize(t, h)
		sid := newSession(t, h)

		if err := h.send("session/resume", 3, loadParams(t, h, sid)); err != nil {
			t.Fatal(err)
		}
		msg := h.recv(t)
		if msg.Method == "session/update" {
			t.Fatalf("session/resume replayed history before responding: %+v", msg)
		}
		if msg.Error != nil {
			t.Fatalf("session/resume error: %+v", msg.Error)
		}
		if got := strings.TrimSpace(string(msg.Result)); got != "{}" {
			t.Fatalf("session/resume result = %s, want empty object", got)
		}
	})

	t.Run("foreign id rejected with invalid params", func(t *testing.T) {
		h := newACPHarnessFactory(t, func(resumeID string) (Session, error) {
			if resumeID != "sess-known" {
				return nil, fmt.Errorf("session not found: %s", resumeID)
			}
			return &fakeSession{id: "sess-known"}, nil
		})
		initialize(t, h)
		if err := h.send("session/resume", 3, loadParams(t, h, "sess-foreign")); err != nil {
			t.Fatal(err)
		}
		msg := h.recv(t)
		if msg.Error == nil {
			t.Fatalf("expected invalid-params error, got result %s", msg.Result)
		}
		if msg.Error.Code != codeInvalidParams {
			t.Fatalf("error code = %d, want %d", msg.Error.Code, codeInvalidParams)
		}
	})

	t.Run("missing sessionId rejected", func(t *testing.T) {
		h := newACPHarness(t, nil)
		initialize(t, h)
		if err := h.send("session/resume", 3, `{}`); err != nil {
			t.Fatal(err)
		}
		msg := h.recv(t)
		if msg.Error == nil || msg.Error.Code != codeInvalidParams {
			t.Fatalf("expected invalid params for missing sessionId, got %+v", msg)
		}
	})
}

// TestServer_sessionResumeOnBusySession pins the shared no-overlap rule:
// a resume on a session with an in-flight prompt is rejected busy.
func TestServer_sessionResumeOnBusySession(t *testing.T) {
	started := make(chan struct{})
	released := make(chan struct{})
	sess := &fakeSession{
		id: "sess-test",
		prompt: func(ctx context.Context, prompt string) (string, bool, error) {
			close(started)
			<-released
			return "late", false, nil
		},
	}
	h := newACPHarness(t, sess)
	initialize(t, h)
	sid := newSession(t, h)

	if err := h.send("session/prompt", 3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"long"}]}`, sid)); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := h.send("session/resume", 4, loadParams(t, h, sid)); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if msg.Error == nil {
		t.Fatalf("expected busy error while prompt in flight, got %s", msg.Result)
	}
	if msg.Error.Code != codeBusy {
		t.Fatalf("error code = %d, want %d", msg.Error.Code, codeBusy)
	}
	close(released)
}

// TestServer_sessionListPagingAndFilterValidation pins the paging
// contract: pages chain through the opaque cursor, the final page
// carries no nextCursor, sessions is an array (never null), an
// unparsable cursor is invalid params, a non-absolute cwd filter is
// rejected outright, and the unwired capability stays method-not-found.
func TestServer_sessionListPagingAndFilterValidation(t *testing.T) {
	t.Run("paged chain ends without nextCursor", func(t *testing.T) {
		h := newACPHarness(t, nil)
		wireStore(h, [][]SessionInfo{
			{{SessionID: "sess-a", Cwd: h.cwd, Title: "First"}},
			{{SessionID: "sess-b", Cwd: h.cwd}},
		}, nil)
		initialize(t, h)

		if err := h.send("session/list", 3, `{}`); err != nil {
			t.Fatal(err)
		}
		page := listResult(t, h.recv(t))
		if len(page.Sessions) != 1 || page.Sessions[0].SessionID != "sess-a" {
			t.Fatalf("first page = %+v", page.Sessions)
		}
		if page.NextCursor != "page-2" {
			t.Fatalf("nextCursor = %q, want page-2", page.NextCursor)
		}

		if err := h.send("session/list", 4, `{"cursor":"page-2"}`); err != nil {
			t.Fatal(err)
		}
		page = listResult(t, h.recv(t))
		if len(page.Sessions) != 1 || page.Sessions[0].SessionID != "sess-b" {
			t.Fatalf("second page = %+v", page.Sessions)
		}
		if page.NextCursor != "" {
			t.Fatalf("final page still advertises nextCursor %q", page.NextCursor)
		}
	})

	t.Run("empty page is an array not null", func(t *testing.T) {
		h := newACPHarness(t, nil)
		wireStore(h, [][]SessionInfo{{}}, nil)
		initialize(t, h)
		if err := h.send("session/list", 3, `{}`); err != nil {
			t.Fatal(err)
		}
		raw := string(h.recv(t).Result)
		if !strings.Contains(raw, `"sessions":[]`) {
			t.Fatalf("empty page must serialize as [], got %s", raw)
		}
	})

	t.Run("unparsable cursor is invalid params", func(t *testing.T) {
		h := newACPHarness(t, nil)
		wireStore(h, [][]SessionInfo{{}}, nil)
		initialize(t, h)
		if err := h.send("session/list", 3, `{"cursor":"@@@garbage@@@"}`); err != nil {
			t.Fatal(err)
		}
		msg := h.recv(t)
		if msg.Error == nil || msg.Error.Code != codeInvalidParams {
			t.Fatalf("expected invalid params for garbage cursor, got %+v", msg)
		}
	})

	t.Run("relative cwd filter is invalid params", func(t *testing.T) {
		h := newACPHarness(t, nil)
		wireStore(h, [][]SessionInfo{{}}, nil)
		initialize(t, h)
		if err := h.send("session/list", 3, `{"cwd":"relative/path"}`); err != nil {
			t.Fatal(err)
		}
		msg := h.recv(t)
		if msg.Error == nil || msg.Error.Code != codeInvalidParams {
			t.Fatalf("expected invalid params for relative cwd, got %+v", msg)
		}
	})

	t.Run("unwired list is method not found", func(t *testing.T) {
		h := newACPHarness(t, nil)
		initialize(t, h)
		if err := h.send("session/list", 3, `{}`); err != nil {
			t.Fatal(err)
		}
		msg := h.recv(t)
		if msg.Error == nil || msg.Error.Code != codeMethodNotFound {
			t.Fatalf("expected method-not-found without wiring, got %+v", msg)
		}
	})
}

// TestServer_sessionCloseCancelsInFlightAndFrees pins the close
// contract: an in-flight prompt resolves cancelled, the close request
// answers {}, the underlying session is freed, and the retired id is no
// longer closable.
func TestServer_sessionCloseCancelsInFlightAndFrees(t *testing.T) {
	started := make(chan struct{})
	sess := &fakeSession{
		id: "sess-test",
		prompt: func(ctx context.Context, prompt string) (string, bool, error) {
			close(started)
			<-ctx.Done()
			return "", false, ctx.Err()
		},
	}
	h := newACPHarness(t, sess)
	initialize(t, h)
	sid := newSession(t, h)

	if err := h.send("session/prompt", 3, fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"long task"}]}`, sid)); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := h.send("session/close", 4, fmt.Sprintf(`{"sessionId":%q}`, sid)); err != nil {
		t.Fatal(err)
	}

	promptDone, closeDone := false, false
	for i := 0; i < 8 && !(promptDone && closeDone); i++ {
		msg := h.recv(t)
		switch {
		case msg.ID != nil && strings.Contains(string(msg.ID), "3") && msg.Error == nil:
			var result PromptResult
			if err := json.Unmarshal(msg.Result, &result); err != nil {
				t.Fatalf("prompt result: %v", err)
			}
			if result.StopReason != StopCancelled {
				t.Fatalf("prompt stopReason = %q, want cancelled", result.StopReason)
			}
			promptDone = true
		case msg.ID != nil && strings.Contains(string(msg.ID), "4"):
			if msg.Error != nil {
				t.Fatalf("session/close error: %+v", msg.Error)
			}
			if got := strings.TrimSpace(string(msg.Result)); got != "{}" {
				t.Fatalf("session/close result = %s, want empty object", got)
			}
			closeDone = true
		default:
			t.Fatalf("unexpected message: %+v", msg)
		}
	}
	if !promptDone || !closeDone {
		t.Fatalf("missing responses (prompt=%v close=%v)", promptDone, closeDone)
	}
	if n := sess.closedCount(); n != 1 {
		t.Fatalf("session closed %d time(s), want 1", n)
	}

	// The session was freed; the id is not closable again.
	if err := h.send("session/close", 5, fmt.Sprintf(`{"sessionId":%q}`, sid)); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if msg.Error == nil || msg.Error.Code != codeInvalidParams {
		t.Fatalf("expected invalid params closing a retired session, got %+v", msg)
	}
}

// TestServer_sessionDeletePrunesStoreAndSucceedsSilently pins delete: a
// live session is torn down first (close contract), the store callback
// receives the id, and unknown or already-deleted ids succeed silently
// without raising an error to the client.
func TestServer_sessionDeletePrunesStoreAndSucceedsSilently(t *testing.T) {
	sess := &fakeSession{id: "sess-live"}
	h := newACPHarness(t, sess)
	var deleted []string
	wireStore(h, nil, &deleted)
	initialize(t, h)
	sid := newSession(t, h)

	if err := h.send("session/delete", 3, fmt.Sprintf(`{"sessionId":%q}`, sid)); err != nil {
		t.Fatal(err)
	}
	msg := h.recv(t)
	if msg.Error != nil {
		t.Fatalf("session/delete error: %+v", msg.Error)
	}
	if got := strings.TrimSpace(string(msg.Result)); got != "{}" {
		t.Fatalf("session/delete result = %s, want empty object", got)
	}
	if n := sess.closedCount(); n != 1 {
		t.Fatalf("live session closed %d time(s), want 1", n)
	}
	if len(deleted) != 1 || deleted[0] != sid {
		t.Fatalf("deleted ids = %v, want [%s]", deleted, sid)
	}

	// A never-created id also succeeds silently (spec D2).
	if err := h.send("session/delete", 4, `{"sessionId":"tck-never-created"}`); err != nil {
		t.Fatal(err)
	}
	msg = h.recv(t)
	if msg.Error != nil {
		t.Fatalf("delete of unknown id must succeed silently: %+v", msg.Error)
	}
	if got := strings.TrimSpace(string(msg.Result)); got != "{}" {
		t.Fatalf("delete of unknown id result = %s, want empty object", got)
	}
	if len(deleted) != 2 || deleted[1] != "tck-never-created" {
		t.Fatalf("store callback saw %v, want both ids", deleted)
	}
}

// TestServer_canonicalFilterCwd pins the list-filter rules: empty is
// allowed (matches everything), relative paths are rejected, absolute
// paths are canonicalized, and a missing directory still canonicalizes
// (it must match nothing rather than error).
func TestServer_canonicalFilterCwd(t *testing.T) {
	cwd := t.TempDir()
	got, err := canonicalFilterCwd("")
	if err != nil || got != "" {
		t.Fatalf("empty filter = (%q, %v), want (\"\", nil)", got, err)
	}
	if _, err := canonicalFilterCwd("relative"); err == nil {
		t.Fatal("relative filter must be rejected")
	}
	got, err = canonicalFilterCwd(cwd + "/../" + path.Base(cwd))
	if err != nil {
		t.Fatalf("absolute filter: %v", err)
	}
	if got != cwd {
		t.Fatalf("canonicalized filter = %q, want %q", got, cwd)
	}
	if _, err = canonicalFilterCwd(cwd + "/does-not-exist"); err != nil {
		t.Fatalf("nonexistent filter must still canonicalize: %v", err)
	}
}
