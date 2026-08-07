package types

// Control-plane message type aliases. The canonical definitions live in
// internal/control. These aliases preserve backward compatibility for code
// that still references types.CtrlMsg, types.CtrlStatus, etc.
//
// New code should import internal/control directly.

import "github.com/buchenberg/yaah/internal/control"

// CtrlMsg is an alias for control.Msg.
type CtrlMsg = control.Msg

// CtrlStatus is an alias for control.Status.
type CtrlStatus = control.Status

// CtrlError is an alias for control.Error.
type CtrlError = control.Error

// CtrlOption is an alias for control.Option.
type CtrlOption = control.Option

// CtrlQuestion is an alias for control.Question.
type CtrlQuestion = control.Question

// CtrlApproval is an alias for control.Approval.
type CtrlApproval = control.Approval

// CtrlContinue is an alias for control.Continue.
type CtrlContinue = control.Continue

// CtrlModelList is an alias for control.ModelList.
type CtrlModelList = control.ModelList

// CtrlTodos is an alias for control.Todos.
type CtrlTodos = control.Todos

// CtrlContextInfo is an alias for control.ContextInfo.
type CtrlContextInfo = control.ContextInfo

// CtrlFallback is an alias for control.Fallback.
type CtrlFallback = control.Fallback

// CtrlDone is an alias for control.Done.
type CtrlDone = control.Done
