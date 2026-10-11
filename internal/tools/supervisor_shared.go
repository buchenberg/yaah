package tools

import (
	shepherd "github.com/buchenberg/shepherd-kernel-go"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"

	"github.com/buchenberg/yaah/internal/agent/supervision"
)

// SharedScopeManager is set during wiring (InitShepherdInfrastructure)
// so the SupervisorTool, the supervised_task tool, and sub-agent scope
// creation all use the same ScopeManager. This keeps scopes in memory
// across tool calls and avoids SQLITE_BUSY from opening separate store
// connections.
//
// Set once during session initialization. Nil when tracing is disabled.
var SharedScopeManager *shepherd.ScopeManager

// SharedTraceStore is the session-wide Shepherd trace store opened during
// wiring. Sub-agent trace middleware writes through this shared connection
// instead of opening their own — separate writers on the same trace.sqlite
// contend and stall on busy_timeout.
//
// Set once during session initialization. Nil when tracing is disabled.
var SharedTraceStore *shepherdtrace.SQLiteTraceStore

// SharedRunRegistry is the supervision bridge (shepherd-adoption plan
// A2): scope IDs mapped to the steering channels of the loops currently
// running under them. The runner registers each sub-agent dispatch; the
// wiring registers the session root against the session's own steering
// channel; the supervisor tool's inject delivers through it, so an
// intervention reaches the running loop instead of only the trace.
//
// Set once during session initialization, alongside the scope manager.
// Nil when tracing is disabled.
var SharedRunRegistry *supervision.Registry
