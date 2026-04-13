// Package conditions implements the condition "timer"/"threshold"/"counter"
// block family.
//
// Phase 1 provides the shared infrastructure only: the behavior attribute
// bundle common to every subtype, and the state machine that drives
// activate_after / deactivate_after / timeout / cooldown / latch / invert
// semantics. Subtype registrations (timer, threshold, counter) are added in
// later phases.
package conditions

import (
	"time"

	"github.com/hashicorp/hcl/v2"
)

// Behavior holds the attributes common to every condition subtype. Zero-valued
// durations disable their respective delays. Inhibit is captured as the raw
// HCL expression; Phase 2 (reactive expression infrastructure) wires it into
// the state machine.
type Behavior struct {
	ActivateAfter   time.Duration
	DeactivateAfter time.Duration
	Timeout         time.Duration
	Cooldown        time.Duration
	Latch           bool
	Invert          bool
	Retentive       bool
	Inhibit         hcl.Expression
}
