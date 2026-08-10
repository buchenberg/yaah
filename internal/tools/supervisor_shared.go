package tools

import shepherd "github.com/buchenberg/shepherd-kernel-go"

// SharedScopeManager is set by the shepherd_trace pipeline builder
// so the SupervisorTool can use the same ScopeManager as the trace
// middleware. This avoids SQLITE_BUSY from opening separate store
// connections and keeps scopes in memory across tool calls.
//
// Set once during pipeline initialization. Nil when tracing is disabled.
var SharedScopeManager *shepherd.ScopeManager
