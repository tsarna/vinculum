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
	cfg "github.com/tsarna/vinculum/config"
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
	StartActive     bool
	Inhibit         hcl.Expression
}

// Hooks bundles the three condition lifecycle action expressions. Zero-valued
// (nil) expressions are treated as "not configured" and skipped. Kept as a
// shared value type even though gohcl cannot inline-promote its fields into
// subtype body structs — the runtime plumbing (HookDispatcher) is shared.
type Hooks struct {
	OnInit       hcl.Expression
	OnActivate   hcl.Expression
	OnDeactivate hcl.Expression
}

// Curated schema metadata for the attributes shared across condition subtypes.
// They are declared individually rather than as one map because no subtype
// accepts all of them: each subtype's schema composes the subset it supports,
// and states plainly which of the rest it rejects.
var (
	activateAfterAttr = cfg.AttrMeta{
		Summary: "Wait this long after the input asserts before activating.",
		Doc:     "An intentional delay, not a noise filter: the timer does not restart if the underlying signal flickers during the window. Use `debounce` to filter noise.",
		Hint:    cfg.HintDuration,
	}

	deactivateAfterAttr = cfg.AttrMeta{
		Summary: "Hold the output active this long after it would otherwise deactivate.",
		Doc:     "Prevents flapping and enforces a minimum active time.",
		Hint:    cfg.HintDuration,
	}

	timeoutAttr = cfg.AttrMeta{
		Summary: "Auto-deactivate after this long active.",
		Doc:     "The clock starts on activation and restarts whenever the input re-asserts while already active. Ignored when `latch = true`.",
		Hint:    cfg.HintDuration,
	}

	cooldownAttr = cfg.AttrMeta{
		Summary: "Minimum quiet period between activations.",
		Doc:     "After deactivating, the condition cannot re-activate until this has elapsed, even if the input immediately re-asserts. Distinct from `debounce` (which filters input noise before the first activation) and `deactivate_after` (which extends an active period).",
		Hint:    cfg.HintDuration,
	}

	latchAttr = cfg.AttrMeta{
		Summary: "Once active, stay active regardless of input.",
		Doc:     "`deactivate_after` and `timeout` are ignored while latched. Release with `clear(condition.<name>)` — or `reset()` on a counter, which also resets the count. Clearing does not silence an input that is still asserting: a declared `input` is re-sampled and may re-activate and re-latch immediately, so clearing tells you whether the cause really went away rather than masking it.",
		Hint:    cfg.HintBool,
	}

	invertAttr = cfg.AttrMeta{
		Summary: "Invert the output after every other rule applies.",
		Doc:     "`get()` returns true where the underlying state would be false, and watchers see the inverted values.",
		Hint:    cfg.HintBool,
	}

	retentiveAttr = cfg.AttrMeta{
		Summary: "Accumulate time toward `activate_after` across separate asserted intervals.",
		Doc:     "Rather than requiring continuous assertion. Accumulated time persists until the condition activates or is cleared. Corresponds to IEC 61131-3's TONR.",
		Hint:    cfg.HintBool,
	}

	startActiveAttr = cfg.AttrMeta{
		Summary: "Begin in the active state at startup.",
		Doc:     "No transition event is emitted, so `on_activate` and `trigger \"watch\"` fire only on the first transition *out of* the boot state. `activate_after`, `cooldown`, and `inhibit` do not apply to it — they govern input-driven activations. With `latch = true` this is the standard fail-safe pattern: the system comes up latched and an operator must clear it before work resumes. `clear()` and `reset()` return to inactive; they never restore this state, or a boot-latched fault could never be cleared.",
		Hint:    cfg.HintBool,
	}

	inhibitAttr = cfg.AttrMeta{
		Summary: "While true, block new activations.",
		Doc:     "A pending activation is cancelled and the condition returns to inactive; a retentive timer discards its accumulated time. An already-active condition is unaffected — inhibit prevents activation, it does not force deactivation. When it clears with the input still asserting, activation resumes from scratch, including any `activate_after` delay.",
		Hint:    cfg.HintReactiveExpression,
	}

	debounceAttr = cfg.AttrMeta{
		Summary: "The input must be stable this long before any transition begins.",
		Doc:     "The timer restarts whenever the input flips during the window, filtering transient noise: it answers \"is this change real?\". Combined with `activate_after`, debounce runs first — the input settles, then the activation delay begins.",
		Hint:    cfg.HintDuration,
	}
)

// hookAttrs are the three lifecycle hooks every subtype accepts. They are the
// locality-friendly alternative to a separate `trigger "watch"` block, and
// dispatch synchronously: a set()/clear()/reset() call blocks until the hook's
// action has evaluated.
var hookAttrs = map[string]cfg.AttrMeta{
	"on_init": {
		Summary: "Evaluated once at startup, after every startable component is ready.",
		Doc:     "Fires whatever the boot state, with `ctx.new_value` set to the condition's current output and no `ctx.old_value`. `on_activate` does not fire at boot, so this is how a dashboard learns the initial state.",
		Hint:    cfg.HintActionExpression,
		Context: "condition-hook",
	},
	"on_activate": {
		Summary: "Evaluated on each transition to active.",
		Doc:     "Fires on the user-visible edge, after `invert` applies, inline on the goroutine that caused the transition.",
		Hint:    cfg.HintActionExpression,
		Context: "condition-hook",
	},
	"on_deactivate": {
		Summary: "Evaluated on each transition to inactive.",
		Doc:     "Fires on the user-visible edge, after `invert` applies, inline on the goroutine that caused the transition.",
		Hint:    cfg.HintActionExpression,
		Context: "condition-hook",
	},
}

// conditionDoc is the shared tail of every subtype's Doc: the behavior common
// to all four is documented on the `condition` block itself, so each subtype's
// prose covers only what makes it different.
const conditionDoc = `
Hook errors are logged and non-fatal, and hooks fire outside the state-machine
lock, so a hook may safely call ` + "`set()`" + ` / ` + "`clear()`" + ` / ` + "`reset()`" + ` on its own
condition. Doing so can of course build a flicker loop.`
