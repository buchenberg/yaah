package tools

import shepherd "github.com/buchenberg/shepherd-kernel-go"

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
var SharedTraceStore *shepherd.SQLiteTraceStore
