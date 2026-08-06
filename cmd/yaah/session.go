package yaah

// UI Driver Pattern
//
// To attach a new UI to a session:
//
//  1. Create a chan types.CtrlMsg and call sess.SetCtrlCh(ch).
//     This wires todos and status messages to your channel.
//
//  2. Implement agent.View (HandleEvent) and call sess.SetView(your view).
//     Events: TokenDelta, Thinking, Flush, ToolStart, ToolEnd,
//             SubAgentStart, SubAgentEnd, Done.
//
//  3. Read from the control channel in a goroutine and dispatch
//     CtrlStatus, CtrlError, CtrlQuestion, CtrlApproval,
//     CtrlModelList, CtrlTodos, CtrlContextInfo, CtrlDone.
//
//  4. Call sess.RunPrompt(ctx, text) for each turn.
//     ctx cancellation aborts the in-flight agent loop.
//
// See: terminalView (REPL), agentViewFwd (Bubble Tea TUI).

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/buchenberg/yaah/internal/memory"
	processpkg "github.com/buchenberg/yaah/internal/process"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// Session is the stable contract between UI drivers and the shared

// agent session. Any driver (TUI, REPL, web, gRPC) should depend only
// on this interface, not on *agentSession directly.
type Session interface {
	RunPrompt(ctx context.Context, prompt string) (string, bool, error)
	Compact()
	Steer(string)
	FollowUp(string)
	SetView(agent.View)
	SetCtrlCh(chan<- types.CtrlMsg)
	SetApproveFn(func(name, args string) bool)
	SetModel(providerName, modelName string)
	ProviderName() string
	ModelName() string
	MCPInfos() []mcp.ServerInfo
	Close()
}

// Compile-time check: agentSession satisfies Session.
var _ Session = (*agentSession)(nil)

// agentSession holds the long-lived infrastructure shared across REPL and
// one-shot prompts. Building it once avoids re-opening the database,
// re-spawning MCP servers, and re-discovering skills on every turn.
type agentSession struct {
	cfg          *config.Config
	provider     agent.Provider
	providerName string
	modelName    string
	systemPrompt string
	// mainPrompt is systemPrompt plus the default-agent directives
	// injected after the identity block. Only the top-level loop sees
	// it; sub-agents receive systemPrompt as their base so default
	// directives never leak into child prompts.
	mainPrompt   string
	toolReg      *tools.Registry
	db           *memory.DB
	mcpClients   []mcp.MCPClient
	mcpInfos     []mcp.ServerInfo
	procMgr      *processpkg.Manager
	messages     []types.Message
	sessionID    string
	msgIdx       int
	otelShutdown func(context.Context) error
	tracker      *tools.ConflictTracker

	// backgroundJobs manages background sub-agents for this session:
	// owns their session-rooted contexts, tracks them for status/cancel,
	// attributes their usage to totalUsage, and delivers results as
	// follow-ups. Cancelled at session close.
	backgroundJobs *tools.BackgroundJobs

	cwd string

	view      agent.View
	ctrlCh    chan<- types.CtrlMsg
	approveFn func(name, args string) bool
	mu        sync.RWMutex

	stdinMu sync.Mutex // serialises stdin reads for approve/prompt fallbacks

	steerCh    chan string
	followupCh chan string
	totalUsage types.Usage
}

func (s *agentSession) close() {
	ctx := context.Background()
	if s.steerCh != nil {
		close(s.steerCh)
	}
	if s.followupCh != nil {
		close(s.followupCh)
	}
	// Cancel any still-running background sub-agents and unblock their
	// result deliveries before tearing down the rest of the session.
	if s.backgroundJobs != nil {
		s.backgroundJobs.Close()
	}
	s.mu.RLock()
	ch := s.ctrlCh
	s.mu.RUnlock()
	if ch != nil {
		ch <- &types.CtrlDone{}
	}
	if s.otelShutdown != nil {
		s.otelShutdown(ctx)
	}
	if s.db != nil {
		s.db.EndSession(s.sessionID, time.Now().Unix(), s.totalUsage.PromptTokens, s.totalUsage.CompletionTokens)
		s.db.Close()
	}
	for _, c := range s.mcpClients {
		c.Close()
	}
}

func (s *agentSession) Close()   { s.close() }
func (s *agentSession) Compact() { s.compactContext() }
func (s *agentSession) ProviderName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.providerName
}
func (s *agentSession) ModelName() string          { s.mu.RLock(); defer s.mu.RUnlock(); return s.modelName }
func (s *agentSession) MCPInfos() []mcp.ServerInfo { return s.mcpInfos }
func (s *agentSession) RunPrompt(ctx context.Context, prompt string) (string, bool, error) {
	return s.runPrompt(ctx, prompt)
}

func (s *agentSession) Steer(text string) {
	select {
	case s.steerCh <- text:
	default:
		s.sendCtrl(&types.CtrlStatus{Text: "steer queue full"})
	}
}

func (s *agentSession) FollowUp(text string) {
	select {
	case s.followupCh <- text:
	default:
		s.sendCtrl(&types.CtrlStatus{Text: "follow-up queue full"})
	}
}

func (s *agentSession) sendCtrl(msg types.CtrlMsg) {
	s.mu.RLock()
	ch := s.ctrlCh
	s.mu.RUnlock()
	if ch == nil {
		return
	}
	select {
	case ch <- msg:
	default:
	}
}

func (s *agentSession) SetView(v agent.View) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.view = v
}

func (s *agentSession) SetCtrlCh(ch chan<- types.CtrlMsg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctrlCh = ch

	if tt := s.toolReg.Get("todowrite"); tt != nil {
		if ttp, ok := tt.(*tools.TodoWriteTool); ok {
			ttp.OnWrite = func() {
				ch <- &types.CtrlTodos{Items: ttp.Store.List()}
			}
		}
	}
}

func (s *agentSession) GetCtrlCh() chan<- types.CtrlMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ctrlCh
}

func (s *agentSession) SetApproveFn(fn func(name, args string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approveFn = fn
}

// promptWorkspaceAccess is the PathValidator's ask fallback: it routes
// out-of-workspace access decisions through the driver's approval UI
// when one is attached (TUI, web) and falls back to a stdin prompt
// otherwise, mirroring the loop's approveTool behaviour.
func (s *agentSession) promptWorkspaceAccess(path, reason string) bool {
	s.mu.RLock()
	fn := s.approveFn
	s.mu.RUnlock()
	if fn != nil {
		return fn("workspace_access", fmt.Sprintf("%s (%s)", path, reason))
	}

	s.stdinMu.Lock()
	defer s.stdinMu.Unlock()

	fi, err := os.Stdin.Stat()
	if err != nil || (fi.Mode()&os.ModeCharDevice) == 0 {
		return false
	}

	fmt.Fprintf(os.Stderr, "\n  ⚠ Path %s is %s. Allow access? [y/N]: ", path, reason)
	os.Stderr.Sync()

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 1024), 1024)
	if scanner.Scan() {
		input := strings.ToLower(strings.TrimSpace(scanner.Text()))
		return input == "y" || input == "yes"
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "  read error: %v\n", err)
	}
	return false
}

func (s *agentSession) SetModel(providerName, modelName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prov := s.provider
	if p, ok := s.cfg.Providers[providerName]; ok {
		if pv, ok2 := makeProvider(providerName, p); ok2 {
			prov = pv
		}
	}
	s.provider = prov
	s.providerName = providerName
	s.modelName = modelName
}
