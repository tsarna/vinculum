package procedure

import "github.com/zclconf/go-cty/cty"

// SignalKind identifies the type of control-flow signal.
type SignalKind int

const (
	SignalNone     SignalKind = iota
	SignalReturn              // procedure exit with a value
	SignalBreak               // exit innermost loop
	SignalContinue            // skip to next loop iteration
)

// Signal carries a control-flow signal up the call stack.
type Signal struct {
	Kind  SignalKind
	Value cty.Value // meaningful only for SignalReturn
}
