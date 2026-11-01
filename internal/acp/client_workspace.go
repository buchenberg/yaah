package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/buchenberg/yaah/internal/tools"
)

const terminalOutputByteLimit = 1 << 20

// clientWorkspace overlays the client's filesystem/terminal capabilities on
// the session's normal workspace. The embedded workspace still provides
// containment, metadata, and any unsupported operation; only text reads,
// writes, and shell execution are delegated.
type clientWorkspace struct {
	tools.Workspace

	server    *Server
	sessionID string
	fsRead    bool
	fsWrite   bool
	terminal  bool
}

func (w *clientWorkspace) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if !w.fsRead {
		return w.Workspace.ReadFile(ctx, path)
	}
	abs, err := w.Workspace.ResolvePath(path)
	if err != nil {
		return nil, err
	}
	raw, err := w.server.callClient(ctx, "fs/read_text_file", ReadTextFileParams{
		SessionID: w.sessionID,
		Path:      abs,
	})
	if err == nil {
		if raw == nil {
			err = errors.New("empty response")
		} else {
			var result struct {
				Content *string `json:"content"`
			}
			if uerr := json.Unmarshal(raw, &result); uerr != nil {
				err = uerr
			} else if result.Content == nil {
				err = errors.New("response has no content")
			} else {
				return []byte(*result.Content), nil
			}
		}
	}
	// ACP fs/read_text_file is text-only. If the client cannot represent the
	// file (binary content, a path outside its root, or a broken response),
	// fall back to the local read rather than turning a readable file into an
	// error.
	data, lerr := w.Workspace.ReadFile(ctx, abs)
	if lerr != nil {
		return nil, fmt.Errorf("client read %s: %w (local fallback: %v)", abs, err, lerr)
	}
	return data, nil
}

func (w *clientWorkspace) Stat(ctx context.Context, path string) (fs.FileInfo, error) {
	info, err := w.Workspace.Stat(ctx, path)
	if err == nil || !w.fsRead || !readToolContext(ctx) || !errors.Is(err, fs.ErrNotExist) {
		return info, err
	}
	return w.unsavedFileInfo(path, err)
}

func (w *clientWorkspace) Lstat(ctx context.Context, path string) (fs.FileInfo, error) {
	info, err := w.Workspace.Lstat(ctx, path)
	if err == nil || !w.fsRead || !readToolContext(ctx) || !errors.Is(err, fs.ErrNotExist) {
		return info, err
	}
	return w.unsavedFileInfo(path, err)
}

// readToolContext limits the synthetic-stat fallback to the read tool. Other
// callers must keep the local filesystem's answer: a directory walk must not
// invent paths from editor buffers, and write's existence check should not
// claim a brand-new file already existed.
func readToolContext(ctx context.Context) bool {
	return tools.ToolNameFromContext(ctx) == "read"
}

// unsavedFileInfo reports a missing host path as a regular file when the
// read tool is looking for it. The client may hold an unsaved buffer the host
// does not have; the following ReadFile is what confirms and returns it.
func (w *clientWorkspace) unsavedFileInfo(path string, statErr error) (fs.FileInfo, error) {
	abs, err := w.Workspace.ResolvePath(path)
	if err != nil {
		return nil, statErr
	}
	return clientFileInfo{name: filepath.Base(abs)}, nil
}

type clientFileInfo struct {
	name string
	size int64
}

func (f clientFileInfo) Name() string       { return f.name }
func (f clientFileInfo) Size() int64        { return f.size }
func (f clientFileInfo) Mode() fs.FileMode  { return 0o444 }
func (f clientFileInfo) ModTime() time.Time { return time.Time{} }
func (f clientFileInfo) IsDir() bool        { return false }
func (f clientFileInfo) Sys() any           { return nil }

func (w *clientWorkspace) WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
	if !w.fsWrite || !utf8.Valid(data) {
		return w.Workspace.WriteFile(ctx, path, data, perm)
	}
	abs, err := w.Workspace.ResolvePath(path)
	if err != nil {
		return err
	}
	if err := w.Workspace.MkdirAll(ctx, filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("client write %s: %w", abs, err)
	}
	// ACP fs/write_text_file has no mode parameter and no atomic rename. Keep
	// an existing host mode when possible; atomicity is the client's
	// responsibility because the editor owns the buffer.
	mode := fs.FileMode(0)
	hadMode := false
	if info, statErr := w.Workspace.Stat(ctx, abs); statErr == nil {
		mode = info.Mode().Perm()
		hadMode = true
	}
	raw, err := w.server.callClient(ctx, "fs/write_text_file", WriteTextFileParams{
		SessionID: w.sessionID,
		Path:      abs,
		Content:   string(data),
	})
	if err != nil {
		return fmt.Errorf("client write %s: %w", abs, err)
	}
	_ = raw
	if hadMode {
		// The client already accepted the write; a best-effort mode restore
		// must not turn that success into a failure.
		_ = os.Chmod(abs, mode)
	}
	return nil
}

func (w *clientWorkspace) Exec(ctx context.Context, req tools.ExecRequest) (tools.ExecResult, error) {
	if !w.terminal || req.Stdin != nil || req.SeparateStreams || !clientTerminalTool(ctx) {
		return w.Workspace.Exec(ctx, req)
	}
	cwd := req.Cwd
	if cwd == "" {
		cwd = w.WorkDir()
	}
	raw, err := w.server.callClientWithLate(ctx, "terminal/create", TerminalCreateParams{
		SessionID:       w.sessionID,
		Command:         req.Command,
		Args:            req.Args,
		Cwd:             cwd,
		OutputByteLimit: terminalOutputByteLimit,
	}, func(late json.RawMessage) {
		// The create response arrived after this call gave up (timeout or
		// cancellation). Release the terminal the client actually created.
		var created TerminalCreateResult
		if json.Unmarshal(late, &created) == nil && created.TerminalID != "" {
			w.releaseTerminal(created.TerminalID)
		}
	})
	if err != nil {
		// Do not fall back to local execution after the client refused or
		// failed a terminal create: that would run the command anyway,
		// bypassing the client-side approval the capability implies. If the
		// client never answers at all, the terminal id is unknowable here;
		// the client owns cleanup for an unacknowledged create.
		return tools.ExecResult{ExitCode: -1}, fmt.Errorf("terminal/create: %w", err)
	}
	var created TerminalCreateResult
	if err := json.Unmarshal(raw, &created); err != nil || created.TerminalID == "" {
		if created.TerminalID != "" {
			// A malformed response still carried a handle; release it rather
			// than leaking the client's terminal.
			w.releaseTerminal(created.TerminalID)
		}
		return tools.ExecResult{ExitCode: -1}, fmt.Errorf("terminal/create: invalid response")
	}
	terminalID := created.TerminalID
	defer w.releaseTerminal(terminalID)

	w.server.mu.Lock()
	send := w.server.sendUpdate
	w.server.mu.Unlock()
	if callID := tools.ToolCallIDFromContext(ctx); callID != "" && send != nil {
		send(w.sessionID, ToolCallUpdate{
			SessionUpdate: UpdateToolCallUpdate,
			ToolCallID:    callID,
			Content: []UpdateContent{{
				Type:       "terminal",
				TerminalID: terminalID,
			}},
		})
	}

	waitRaw, waitErr := w.server.callClient(ctx, "terminal/wait_for_exit", TerminalIDParams{
		SessionID: w.sessionID, TerminalID: terminalID,
	})
	if waitErr != nil {
		// A cancelled/timed-out command is killed, its partial output is
		// still returned, and the error is preserved for the shell tool.
		// Bound the follow-up output call: ctx is already cancelled here,
		// so WithoutCancel alone would let a silent client stall the turn.
		w.killTerminal(terminalID)
		outCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		output, _ := w.terminalOutput(outCtx, terminalID)
		cancel()
		return tools.ExecResult{ExitCode: -1, Stdout: output}, waitErr
	}
	var wait TerminalWaitResult
	if err := json.Unmarshal(waitRaw, &wait); err != nil {
		return tools.ExecResult{ExitCode: -1}, fmt.Errorf("terminal/wait_for_exit: %w", err)
	}
	exitCode := -1
	if wait.ExitCode != nil {
		exitCode = *wait.ExitCode
	}
	output, outErr := w.terminalOutput(ctx, terminalID)
	res := tools.ExecResult{ExitCode: exitCode, Stdout: output}
	if outErr != nil {
		return res, outErr
	}
	if wait.Signal != nil && *wait.Signal != "" {
		return res, fmt.Errorf("terminated by signal %s", *wait.Signal)
	}
	if exitCode != 0 {
		return res, fmt.Errorf("exit status %d", exitCode)
	}
	return res, nil
}

// clientTerminalTool reports whether this command belongs in a visible client
// terminal. Internal rg/git/staticcheck plumbing stays local so a terminal
// pane is opened only for the user-facing shell tools.
func clientTerminalTool(ctx context.Context) bool {
	switch tools.ToolNameFromContext(ctx) {
	case "bash", "powershell":
		return true
	default:
		return false
	}
}

func (w *clientWorkspace) terminalOutput(ctx context.Context, terminalID string) (string, error) {
	raw, err := w.server.callClient(ctx, "terminal/output", TerminalIDParams{
		SessionID: w.sessionID, TerminalID: terminalID,
	})
	if err != nil {
		return "", fmt.Errorf("terminal/output: %w", err)
	}
	var output TerminalOutputResult
	if err := json.Unmarshal(raw, &output); err != nil {
		return "", fmt.Errorf("terminal/output: %w", err)
	}
	if output.Truncated {
		return output.Output + "\n...[terminal output truncated]", nil
	}
	return output.Output, nil
}

func (w *clientWorkspace) killTerminal(terminalID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = w.server.callClient(ctx, "terminal/kill", TerminalIDParams{
		SessionID: w.sessionID, TerminalID: terminalID,
	})
}

func (w *clientWorkspace) releaseTerminal(terminalID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = w.server.callClient(ctx, "terminal/release", TerminalIDParams{
		SessionID: w.sessionID, TerminalID: terminalID,
	})
}

// callClient sends one agent->client request and waits for its response. The
// caller's context bounds the wait; an abandoned response is dropped.
func (s *Server) callClient(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return s.callClientWithLate(ctx, method, params, nil)
}

// callClientWithLate is callClient with a late-result hook. When ctx ends
// first, the pending entry is kept briefly: if the client answers after the
// deadline, late receives the result so a resource-bearing method such as
// terminal/create can clean up. Without a late hook the entry is removed
// immediately, as before.
func (s *Server) callClientWithLate(ctx context.Context, method string, params any, late func(json.RawMessage)) (json.RawMessage, error) {
	s.mu.Lock()
	c := s.conn
	if c == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("%s: no client connection", method)
	}
	s.nextReqID++
	id := s.nextReqID
	ch := make(chan clientResponse, 1)
	key := permissionID(id)
	s.pendingReq[key] = ch
	s.mu.Unlock()
	cleanup := func() {
		s.mu.Lock()
		delete(s.pendingReq, key)
		s.mu.Unlock()
	}

	c.sendRequest(id, method, params)
	select {
	case resp := <-ch:
		cleanup()
		if resp.err != nil {
			return nil, resp.err
		}
		return resp.result, nil
	case <-ctx.Done():
		if late == nil {
			cleanup()
			return nil, ctx.Err()
		}
		// The request is already in flight and may have created a resource.
		// Keep routing alive briefly so a late success can be released.
		go func() {
			timer := time.NewTimer(30 * time.Second)
			defer timer.Stop()
			select {
			case resp := <-ch:
				if resp.err == nil && resp.result != nil {
					late(resp.result)
				}
			case <-timer.C:
			}
			cleanup()
		}()
		return nil, ctx.Err()
	}
}

// wrapClientWorkspace installs the client-backed overlay after a session is
// built. It is skipped for isolated/container workspaces (the client's editor
// cannot see them) and when the client advertised neither fs nor terminal.
func (s *Server) wrapClientWorkspace(st *sessionState) error {
	s.mu.Lock()
	caps := s.clientCaps
	s.mu.Unlock()
	if caps == nil || st == nil || st.sess == nil {
		return nil
	}
	fsRead := caps.Fs != nil && caps.Fs.ReadTextFile
	fsWrite := caps.Fs != nil && caps.Fs.WriteTextFile
	if !fsRead && !fsWrite && !caps.Terminal {
		return nil
	}
	reg := st.sess.ToolReg()
	if reg == nil || reg.Workspace == nil || !reg.Workspace.Local() || tools.WorkspaceConfined(reg.Workspace) {
		return nil
	}
	cw := &clientWorkspace{
		Workspace: reg.Workspace,
		server:    s,
		sessionID: st.id,
		fsRead:    fsRead,
		fsWrite:   fsWrite,
		terminal:  caps.Terminal,
	}
	if err := reg.SetWorkspace(cw); err != nil {
		return fmt.Errorf("client workspace: %w", err)
	}
	return nil
}
