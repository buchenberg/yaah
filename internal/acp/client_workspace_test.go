package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/tools"
)

func clientWorkspaceHarness(t *testing.T) (*acpHarness, *fakeSession, *tools.Registry, string) {
	t.Helper()
	cwd := t.TempDir()
	reg := tools.NewRegistry()
	pv := tools.NewPathValidator(cwd, false, nil)
	pv.WorkDir = pv.WorkspaceRoot
	if err := reg.SetWorkspace(tools.NewLocalWorkspace(pv)); err != nil {
		t.Fatalf("set workspace: %v", err)
	}
	sess := &fakeSession{id: "sess-client", reg: reg}
	h := newACPHarness(t, sess)
	initialize(t, h)
	if got := newSession(t, h); got != "sess-client" {
		t.Fatalf("session id = %q", got)
	}
	return h, sess, reg, cwd
}

func TestClientWorkspace_ReadUsesUnsavedClientContent(t *testing.T) {
	h, _, reg, cwd := clientWorkspaceHarness(t)
	path := filepath.Join(cwd, "open.go")
	if err := os.WriteFile(path, []byte("on disk\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := reg.Execute(context.Background(), "read", fmt.Sprintf(`{"filePath":%q}`, path))
		done <- result{out, err}
	}()

	req := h.recvRequest(t, "fs/read_text_file")
	var params ReadTextFileParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.SessionID != "sess-client" || params.Path != path {
		t.Fatalf("read params = %+v", params)
	}
	if err := h.reply(req.ID, ReadTextFileResult{Content: "unsaved buffer\n"}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("read: %v", got.err)
		}
		if got.out != "unsaved buffer\n" {
			t.Fatalf("read output = %q, want unsaved buffer", got.out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read did not finish")
	}
}

func TestClientWorkspace_WriteUsesClient(t *testing.T) {
	h, _, reg, cwd := clientWorkspaceHarness(t)
	path := filepath.Join(cwd, "new.txt")
	done := make(chan error, 1)
	go func() {
		_, err := reg.Execute(context.Background(), "write", fmt.Sprintf(`{"filePath":%q,"content":"editor buffer"}`, path))
		done <- err
	}()
	req := h.recvRequest(t, "fs/write_text_file")
	var params WriteTextFileParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Path != path || params.Content != "editor buffer" {
		t.Fatalf("write params = %+v", params)
	}
	if err := h.reply(req.ID, struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestClientWorkspace_TerminalExecAndRelease(t *testing.T) {
	h, _, reg, cwd := clientWorkspaceHarness(t)
	updates := make(chan any, 2)
	h.server.sendUpdate = func(_ string, update any) { updates <- update }
	done := make(chan struct {
		out string
		err error
	}, 1)
	go func() {
		ctx := tools.WithToolCallID(context.Background(), "call-1")
		out, err := reg.Execute(ctx, "bash", `{"command":"echo hi"}`)
		done <- struct {
			out string
			err error
		}{out, err}
	}()

	create := h.recvRequest(t, "terminal/create")
	var cp TerminalCreateParams
	if err := json.Unmarshal(create.Params, &cp); err != nil {
		t.Fatal(err)
	}
	if cp.SessionID != "sess-client" || cp.Command == "" || cp.Cwd != cwd {
		t.Fatalf("create params = %+v", cp)
	}
	if err := h.reply(create.ID, TerminalCreateResult{TerminalID: "term-1"}); err != nil {
		t.Fatal(err)
	}
	var update any
	select {
	case update = <-updates:
	case <-time.After(time.Second):
		t.Fatal("terminal create did not emit a tool-call update")
	}
	if u, ok := update.(ToolCallUpdate); !ok || u.ToolCallID != "call-1" || len(u.Content) != 1 || u.Content[0].TerminalID != "term-1" {
		t.Fatalf("terminal update = %+v", update)
	}

	wait := h.recvRequest(t, "terminal/wait_for_exit")
	code := 0
	if err := h.reply(wait.ID, TerminalWaitResult{ExitCode: &code}); err != nil {
		t.Fatal(err)
	}
	output := h.recvRequest(t, "terminal/output")
	if err := h.reply(output.ID, TerminalOutputResult{Output: "hi\n"}); err != nil {
		t.Fatal(err)
	}
	release := h.recvRequest(t, "terminal/release")
	if err := h.reply(release.ID, struct{}{}); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("bash: %v", got.err)
		}
		if strings.TrimSpace(got.out) != "hi" {
			t.Fatalf("bash output = %q", got.out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bash did not finish")
	}
}

func TestClientWorkspace_NonzeroExitIsAnError(t *testing.T) {
	h, _, reg, _ := clientWorkspaceHarness(t)
	done := make(chan struct {
		res tools.ExecResult
		err error
	}, 1)
	go func() {
		res, err := reg.Workspace.Exec(tools.WithToolName(context.Background(), "bash"), tools.ExecRequest{Command: "sh", Args: []string{"-c", "exit 3"}})
		done <- struct {
			res tools.ExecResult
			err error
		}{res, err}
	}()

	create := h.recvRequest(t, "terminal/create")
	if err := h.reply(create.ID, TerminalCreateResult{TerminalID: "term-2"}); err != nil {
		t.Fatal(err)
	}
	wait := h.recvRequest(t, "terminal/wait_for_exit")
	code := 3
	if err := h.reply(wait.ID, TerminalWaitResult{ExitCode: &code}); err != nil {
		t.Fatal(err)
	}
	output := h.recvRequest(t, "terminal/output")
	if err := h.reply(output.ID, TerminalOutputResult{Output: "boom\n"}); err != nil {
		t.Fatal(err)
	}
	release := h.recvRequest(t, "terminal/release")
	if err := h.reply(release.ID, struct{}{}); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-done:
		if got.err == nil || !strings.Contains(got.err.Error(), "exit status 3") {
			t.Fatalf("exec error = %v, want exit status 3", got.err)
		}
		if got.res.ExitCode != 3 || got.res.Stdout != "boom\n" {
			t.Fatalf("exec result = %+v", got.res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("exec did not finish")
	}
}

func TestClientWorkspace_NoCapabilitiesStaysLocal(t *testing.T) {
	cwd := t.TempDir()
	reg := tools.NewRegistry()
	pv := tools.NewPathValidator(cwd, false, nil)
	pv.WorkDir = pv.WorkspaceRoot
	if err := reg.SetWorkspace(tools.NewLocalWorkspace(pv)); err != nil {
		t.Fatalf("set workspace: %v", err)
	}
	h := newACPHarness(t, &fakeSession{id: "sess-local", reg: reg})
	if err := h.send("initialize", 1, `{"protocolVersion":1,"clientInfo":{"name":"test-client","version":"1"}}`); err != nil {
		t.Fatal(err)
	}
	_ = h.recv(t)
	if got := newSession(t, h); got != "sess-local" {
		t.Fatalf("session id = %q", got)
	}
	path := filepath.Join(cwd, "local.txt")
	if err := os.WriteFile(path, []byte("on disk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), "read", fmt.Sprintf(`{"filePath":%q}`, path))
	if err != nil {
		t.Fatalf("local read: %v", err)
	}
	if out != "on disk\n" {
		t.Fatalf("local read output = %q", out)
	}
}

func TestClientWorkspace_InternalCommandsStayLocal(t *testing.T) {
	_, _, reg, _ := clientWorkspaceHarness(t)
	ctx := tools.WithToolName(context.Background(), "grep")
	res, err := reg.Workspace.Exec(ctx, tools.ExecRequest{Command: "go", Args: []string{"version"}})
	if err != nil {
		t.Fatalf("local internal command: %v", err)
	}
	if !strings.Contains(res.Stdout, "go version") {
		t.Fatalf("local internal output = %q", res.Stdout)
	}
}
