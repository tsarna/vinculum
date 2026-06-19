package conditions

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	richcty "github.com/tsarna/rich-cty-types"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// FlipflopCondition is the `condition "flipflop"` subtype. It exposes the
// standard digital-logic bistables — T, SR, gated SR, D, D-latch, JK — through
// a uniform set of "wire" attributes. Each declared wire is a boolean HCL
// expression observed reactively; combinations of wires name the variant.
//
// The flipflop resolves its wires to a single desired output value each cycle
// (applying the conflict-resolution priority: gate → set/reset dominance →
// set/reset over toggle → D-sample over toggle) and feeds that value to the
// shared StateMachine via SetRawInput. The StateMachine owns latch / invert /
// cooldown / inhibit and the four-state output model, so those behaviors carry
// over identically from timer. The flipflop deliberately does NOT use the
// temporal StateMachine behaviors (activate_after / deactivate_after / timeout
// / retentive): a flipflop responds to its inputs immediately.
//
// Edge detection is per-wire: each wire stores its previous boolean and a
// hasPrev flag; the first evaluation (at Start, or the first notification)
// establishes the baseline and reports no edge, so a source that happens to be
// asserting at boot does not spuriously drive the flipflop. start_active is the
// explicit boot-output knob.
//
// Dispatch: rather than rely on the source identity reported by OnChange (which
// for condition sources is the inner StateMachine, not the capsule a wire
// referenced — see flipflopSource), the flipflop subscribes one flipflopSource
// watcher per referenced Watchable, each carrying the wires that reference it.
// A notification thus re-evaluates only the wires fed by the source that fired;
// the gate's level is cached so non-gate notifications never re-evaluate it.
type FlipflopCondition struct {
	name   string
	config *cfg.Config
	sm     *StateMachine

	// Declared wires; nil when the corresponding attribute is absent.
	setWire    *wire
	resetWire  *wire
	toggleWire *wire
	gateWire   *wire // edge mode (rising/falling/both) or level mode (high/low)

	// set_from is a level expression sampled on demand when the gate permits —
	// it is never edge-detected, so it has no wire/prev state.
	setFromExpr *cfg.ReactiveExpr

	dominantSet bool // true => dominant="set"; false => dominant="reset" (default)

	inhibitExpr *cfg.ReactiveExpr
	hooks       *HookDispatcher

	// sources binds each referenced Watchable to the wires that depend on it.
	sources []*flipflopSource

	mu            sync.Mutex
	desired       bool // current desired output, reconciled against sm.RawInput()
	gateAsserting bool // cached level-gate assertion (meaningful for level gates)
}

// flipflopSource is the per-Watchable subscription. It captures, at subscribe
// time, which of the flipflop's wires reference this particular source, so
// dispatch never depends on the notification's source argument matching the
// subscribed object (it does not, for condition sources: TimerCondition.Watch
// forwards to its StateMachine, which notifies as itself). On a notification,
// only the flagged wires are re-evaluated.
type flipflopSource struct {
	ff        *FlipflopCondition
	watchable richcty.Watchable

	feedsSet     bool
	feedsReset   bool
	feedsToggle  bool
	feedsGate    bool
	feedsSetFrom bool
}

// OnChange implements richcty.Watcher. The source argument is intentionally
// ignored — this object already knows which wires it owns.
func (s *flipflopSource) OnChange(ctx context.Context, _ richcty.Watchable, _, _ cty.Value) {
	s.ff.onSourceChanged(ctx, s)
}

// edgeMode names the detection rule for a wire. rising/falling/both apply to
// event wires and to edge-triggered gates; high/low are level-sensitive and
// valid only for the gate.
type edgeMode int

const (
	edgeRising edgeMode = iota
	edgeFalling
	edgeBoth
	levelHigh
	levelLow
)

func (m edgeMode) isLevel() bool { return m == levelHigh || m == levelLow }

// wire is one edge-detected boolean input of the flipflop. re is used only for
// source extraction (.Sources()) and on-demand evaluation (.Eval()); the
// flipflop subscribes flipflopSource objects, so the wire's ReactiveExpr is
// never Start()ed.
type wire struct {
	name    string // attribute name, for diagnostics
	re      *cfg.ReactiveExpr
	edge    edgeMode
	prev    bool
	hasPrev bool
}

// edgeFired reports whether the prev→cur transition matches the edge mode.
func edgeFired(prev, cur bool, mode edgeMode) bool {
	switch mode {
	case edgeRising:
		return !prev && cur
	case edgeFalling:
		return prev && !cur
	case edgeBoth:
		return prev != cur
	}
	return false
}

// --- richcty interface methods (output delegates to the state machine) ---

func (f *FlipflopCondition) Get(ctx context.Context, args []cty.Value) (cty.Value, error) {
	return f.sm.Get(ctx, args)
}
func (f *FlipflopCondition) State(ctx context.Context) (string, error) { return f.sm.State(ctx) }
func (f *FlipflopCondition) Watch(w richcty.Watcher)                   { f.sm.Watch(w) }
func (f *FlipflopCondition) Unwatch(w richcty.Watcher)                 { f.sm.Unwatch(w) }

// Set implements richcty.Settable. Unlike timer (which rejects set() once an
// input= is declared), a flipflop accepts imperative set() even with wires
// declared — the wires are additional event sources, not the only sources.
// Honors latch / inhibit / cooldown via the normal SetRawInput path (e.g.
// set(false) is ignored while latched-active).
func (f *FlipflopCondition) Set(ctx context.Context, args []cty.Value) (cty.Value, error) {
	if len(args) < 1 {
		return cty.NilVal, fmt.Errorf("set(condition.%s): missing value argument", f.name)
	}
	v := args[0]
	if v.Type() != cty.Bool || v.IsNull() {
		return cty.NilVal, fmt.Errorf("set(condition.%s): value must be a boolean, got %s", f.name, v.Type().FriendlyName())
	}
	f.mu.Lock()
	f.reconcileDesiredLocked()
	f.desired = v.True()
	f.mu.Unlock()
	f.sm.SetRawInput(ctx, v.True())
	return v, nil
}

// Toggle implements richcty.Toggleable. Equivalent to set(condition.x, !current),
// flipping the desired output directly. Always permitted. Returns the new value.
func (f *FlipflopCondition) Toggle(ctx context.Context, _ []cty.Value) (cty.Value, error) {
	f.mu.Lock()
	f.reconcileDesiredLocked()
	newVal := !f.desired
	f.desired = newVal
	f.mu.Unlock()
	f.sm.SetRawInput(ctx, newVal)
	return cty.BoolVal(newVal), nil
}

// Clear implements richcty.Clearable. Resets the desired output to false and
// resets the state machine (releasing any latch, restarting cooldown). The
// wires keep their edge baselines, so the next genuine edge re-drives output.
func (f *FlipflopCondition) Clear(ctx context.Context) error {
	f.mu.Lock()
	f.desired = false
	f.mu.Unlock()
	return f.sm.Clear(ctx)
}

// reconcileDesiredLocked syncs the flipflop's cached desired output with the
// state machine's raw input. The SM resets rawInput on Clear(); reconciling
// before reading desired keeps toggle/set coherent. Called with f.mu held.
func (f *FlipflopCondition) reconcileDesiredLocked() {
	if smRaw := f.sm.RawInput(); smRaw != f.desired {
		f.desired = smRaw
	}
}

// onSourceChanged handles a notification from one subscribed source: it
// re-evaluates only that source's wires, resolves the desired output, and (if
// it changed) drives the state machine.
func (f *FlipflopCondition) onSourceChanged(ctx context.Context, s *flipflopSource) {
	f.mu.Lock()
	f.reconcileDesiredLocked()
	newDesired, changed := f.recomputeLocked(s)
	if !changed {
		f.mu.Unlock()
		return
	}
	f.desired = newDesired
	f.mu.Unlock()
	f.sm.SetRawInput(ctx, newDesired)
}

// recomputeLocked applies the conflict-resolution priority for the source that
// fired and returns the resolved desired output plus whether it changed. Only
// wires fed by s are re-evaluated; the gate level is read from cache unless s
// feeds the gate. Called with f.mu held; mutates wire prev/hasPrev and the
// cached gate assertion as side effects.
func (f *FlipflopCondition) recomputeLocked(s *flipflopSource) (bool, bool) {
	// 1. Gate: re-evaluate only when this source feeds it; otherwise the cached
	//    f.gateAsserting (for level gates) remains valid since only the gate's
	//    own source can change it.
	gateEdgeFiredNow := false
	gateJustAsserted := false
	if f.gateWire != nil && s.feedsGate {
		if gv, ok := f.evalBool(f.gateWire.re, f.gateWire.name); ok {
			w := f.gateWire
			if w.edge.isLevel() {
				asserting := gv
				if w.edge == levelLow {
					asserting = !gv
				}
				if w.hasPrev {
					prevAssert := w.prev
					if w.edge == levelLow {
						prevAssert = !w.prev
					}
					gateJustAsserted = asserting && !prevAssert
				}
				f.gateAsserting = asserting
			} else if w.hasPrev {
				gateEdgeFiredNow = edgeFired(w.prev, gv, w.edge)
			}
			w.prev = gv
			w.hasPrev = true
		}
	}

	// 2. Window: for a level gate, open while asserting (cached); for an edge
	//    gate, open only on the cycle its edge fires; with no gate, always open.
	gateOpen := true
	levelGate := false
	if f.gateWire != nil {
		if f.gateWire.edge.isLevel() {
			levelGate = true
			gateOpen = f.gateAsserting
		} else {
			gateOpen = gateEdgeFiredNow
		}
	}

	newDesired := f.desired

	// 3. D-sample: sample set_from (a level) before toggle (spec rule 4). For a
	//    level gate, sample while asserting whenever set_from changed or the
	//    gate just asserted; for an edge gate, sample on the gate edge.
	if f.setFromExpr != nil {
		var sample bool
		if levelGate {
			sample = f.gateAsserting && (s.feedsSetFrom || gateJustAsserted)
		} else {
			sample = gateEdgeFiredNow
		}
		if sample {
			if v, ok := f.evalBool(f.setFromExpr, "set_from"); ok {
				newDesired = v
			}
		}
	}

	// 4. Event edges: detect only for wires this source feeds (updating prev so
	//    the baseline stays current), honored only when the gate window is open.
	setFired := s.feedsSet && f.edgeFiredFor(f.setWire)
	resetFired := s.feedsReset && f.edgeFiredFor(f.resetWire)
	toggleFired := s.feedsToggle && f.edgeFiredFor(f.toggleWire)
	if !gateOpen {
		setFired, resetFired, toggleFired = false, false, false
	}

	// 5. Set/Reset dominance, then set/reset over toggle.
	switch {
	case setFired && resetFired:
		newDesired = f.dominantSet
	case setFired:
		newDesired = true
	case resetFired:
		newDesired = false
	}
	if !setFired && !resetFired && toggleFired {
		newDesired = !newDesired
	}

	return newDesired, newDesired != f.desired
}

// edgeFiredFor re-evaluates an event wire, updates its baseline, and reports
// whether its configured edge fired this cycle. Returns false for a nil wire, a
// non-boolean value, or the first (baseline) evaluation.
func (f *FlipflopCondition) edgeFiredFor(w *wire) bool {
	if w == nil {
		return false
	}
	cur, ok := f.evalBool(w.re, w.name)
	if !ok {
		return false
	}
	fired := w.hasPrev && edgeFired(w.prev, cur, w.edge)
	w.prev = cur
	w.hasPrev = true
	return fired
}

// evalBool evaluates a wire expression to a boolean. Null / unknown /
// non-boolean results are logged to the user log and reported as ok=false
// (caller treats as "no edge this cycle, baseline unchanged") — same policy as
// timer's input=.
func (f *FlipflopCondition) evalBool(re *cfg.ReactiveExpr, wireName string) (bool, bool) {
	v, diags := re.Eval()
	if diags.HasErrors() {
		f.config.UserLogger.Warn("flipflop wire failed to evaluate",
			zap.String("name", f.name), zap.String("wire", wireName), zap.Error(diags))
		return false, false
	}
	if v.IsNull() || !v.IsKnown() || v.Type() != cty.Bool {
		f.config.UserLogger.Warn("flipflop wire did not produce a boolean",
			zap.String("name", f.name), zap.String("wire", wireName),
			zap.String("type", v.Type().FriendlyName()))
		return false, false
	}
	return v.True(), true
}

// eventAndGateWires returns the declared edge-detected wires (set/reset/toggle/
// gate) in a stable order, for baseline evaluation.
func (f *FlipflopCondition) eventAndGateWires() []*wire {
	var ws []*wire
	for _, w := range []*wire{f.setWire, f.resetWire, f.toggleWire, f.gateWire} {
		if w != nil {
			ws = append(ws, w)
		}
	}
	return ws
}

// Start bootstraps the state machine, establishes the per-wire baselines
// without firing any edge (including the cached gate assertion), starts the
// inhibit expression, and subscribes each source. No synthetic boot transition
// is produced — start_active is the only boot-output knob.
func (f *FlipflopCondition) Start() error {
	f.sm.Bootstrap()
	f.mu.Lock()
	if f.sm.behavior.StartActive {
		f.desired = true
	}
	for _, w := range f.eventAndGateWires() {
		if v, ok := f.evalBool(w.re, w.name); ok {
			w.prev = v
			w.hasPrev = true
			if w == f.gateWire && w.edge.isLevel() {
				f.gateAsserting = v
				if w.edge == levelLow {
					f.gateAsserting = !v
				}
			}
		}
	}
	f.mu.Unlock()

	if f.inhibitExpr != nil {
		if diags := f.inhibitExpr.Start(context.Background()); diags.HasErrors() {
			return fmt.Errorf("condition %q: inhibit: %s", f.name, diags.Error())
		}
	}
	for _, s := range f.sources {
		s.watchable.Watch(s)
	}
	return nil
}

// PostStart fires the on_init hook after all Startables have bootstrapped.
func (f *FlipflopCondition) PostStart() error {
	f.hooks.FireInit(f.sm.Output())
	return nil
}

// Stop unsubscribes every source and stops the inhibit expression.
func (f *FlipflopCondition) Stop() error {
	for _, s := range f.sources {
		s.watchable.Unwatch(s)
	}
	if f.inhibitExpr != nil {
		f.inhibitExpr.Stop()
	}
	return nil
}

// --- capsule type ---

var FlipflopConditionCapsuleType = cty.CapsuleWithOps("flipflop_condition",
	reflect.TypeOf((*FlipflopCondition)(nil)).Elem(), &cty.CapsuleOps{
		GoString:     func(v interface{}) string { return fmt.Sprintf("flipflop_condition(%p)", v) },
		TypeGoString: func(_ reflect.Type) string { return "FlipflopCondition" },
	})

func newFlipflopConditionCapsule(f *FlipflopCondition) cty.Value {
	return cty.CapsuleVal(FlipflopConditionCapsuleType, f)
}

// --- HCL decode ---

// flipflopBody decodes the flipflop block. There is no `,remain` field, so
// gohcl rejects any attribute not listed here — which is how the unsupported
// common attributes (debounce / retentive / timeout / activate_after /
// deactivate_after / input) are kept off the flipflop with no extra code.
type flipflopBody struct {
	SetOn   hcl.Expression `hcl:"set_on,optional"`
	SetEdge *string        `hcl:"set_edge,optional"`

	ResetOn   hcl.Expression `hcl:"reset_on,optional"`
	ResetEdge *string        `hcl:"reset_edge,optional"`

	ToggleOn   hcl.Expression `hcl:"toggle_on,optional"`
	ToggleEdge *string        `hcl:"toggle_edge,optional"`

	SetFrom  hcl.Expression `hcl:"set_from,optional"`
	GateOn   hcl.Expression `hcl:"gate_on,optional"`
	GateEdge *string        `hcl:"gate_edge,optional"`

	Dominant *string `hcl:"dominant,optional"`

	StartActive *bool          `hcl:"start_active,optional"`
	Invert      *bool          `hcl:"invert,optional"`
	Latch       *bool          `hcl:"latch,optional"`
	Cooldown    hcl.Expression `hcl:"cooldown,optional"`
	Inhibit     hcl.Expression `hcl:"inhibit,optional"`

	OnInit       hcl.Expression `hcl:"on_init,optional"`
	OnActivate   hcl.Expression `hcl:"on_activate,optional"`
	OnDeactivate hcl.Expression `hcl:"on_deactivate,optional"`
}

func init() {
	cfg.RegisterConditionSubtype("flipflop", cfg.ConditionRegistration{
		Process:         processFlipflopCondition,
		HasDependencyId: true,
	})
}

// parseEventEdge parses a set/reset/toggle edge string. nil defaults to rising.
func parseEventEdge(edge *string, attr string, defRange hcl.Range) (edgeMode, hcl.Diagnostics) {
	if edge == nil {
		return edgeRising, nil
	}
	switch *edge {
	case "rising":
		return edgeRising, nil
	case "falling":
		return edgeFalling, nil
	case "both":
		return edgeBoth, nil
	}
	return edgeRising, hcl.Diagnostics{&hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  "Invalid edge mode",
		Detail:   fmt.Sprintf("%s must be one of \"rising\", \"falling\", or \"both\"; got %q", attr, *edge),
		Subject:  &defRange,
	}}
}

// parseGateEdge parses gate_edge, which additionally accepts the level-
// sensitive "high" and "low" modes. nil defaults to rising.
func parseGateEdge(edge *string, defRange hcl.Range) (edgeMode, hcl.Diagnostics) {
	if edge == nil {
		return edgeRising, nil
	}
	switch *edge {
	case "rising":
		return edgeRising, nil
	case "falling":
		return edgeFalling, nil
	case "both":
		return edgeBoth, nil
	case "high":
		return levelHigh, nil
	case "low":
		return levelLow, nil
	}
	return edgeRising, hcl.Diagnostics{&hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  "Invalid gate edge mode",
		Detail:   fmt.Sprintf("gate_edge must be one of \"rising\", \"falling\", \"both\", \"high\", or \"low\"; got %q", *edge),
		Subject:  &defRange,
	}}
}

func processFlipflopCondition(config *cfg.Config, block *hcl.Block, def *cfg.ConditionDefinition) hcl.Diagnostics {
	body := flipflopBody{}
	diags := gohcl.DecodeBody(def.RemainingBody, config.EvalCtx(), &body)
	if diags.HasErrors() {
		return diags
	}

	hasSet := cfg.IsExpressionProvided(body.SetOn)
	hasReset := cfg.IsExpressionProvided(body.ResetOn)
	hasToggle := cfg.IsExpressionProvided(body.ToggleOn)
	hasSetFrom := cfg.IsExpressionProvided(body.SetFrom)
	hasGate := cfg.IsExpressionProvided(body.GateOn)

	// --- structural validation ---
	if !hasSet && !hasReset && !hasToggle && !hasSetFrom {
		// gate_on alone is a special case of "no driven wire"; give it a
		// message that points at what the gate is missing.
		if hasGate {
			return append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Gate has nothing to gate",
				Detail:   "gate_on requires a driven wire (set_on, reset_on, toggle_on, or set_from)",
				Subject:  &def.DefRange,
			})
		}
		return append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Flipflop has no driven wire",
			Detail:   "a flipflop requires at least one of set_on, reset_on, toggle_on, or set_from",
			Subject:  &def.DefRange,
		})
	}
	if hasSetFrom && !hasGate {
		return append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "set_from requires gate_on",
			Detail:   "set_from is sampled when the gate fires and so requires gate_on; for continuous level-tracking use condition \"timer\" with input =",
			Subject:  &def.DefRange,
		})
	}

	// --- edges ---
	var moreDiags hcl.Diagnostics
	setEdge, moreDiags := parseEventEdge(body.SetEdge, "set_edge", def.DefRange)
	diags = diags.Extend(moreDiags)
	resetEdge, moreDiags := parseEventEdge(body.ResetEdge, "reset_edge", def.DefRange)
	diags = diags.Extend(moreDiags)
	toggleEdge, moreDiags := parseEventEdge(body.ToggleEdge, "toggle_edge", def.DefRange)
	diags = diags.Extend(moreDiags)
	gateEdge, moreDiags := parseGateEdge(body.GateEdge, def.DefRange)
	diags = diags.Extend(moreDiags)

	// --- dominant ---
	dominantSet := false
	if body.Dominant != nil {
		switch *body.Dominant {
		case "reset":
			dominantSet = false
		case "set":
			dominantSet = true
		default:
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid dominant",
				Detail:   fmt.Sprintf("dominant must be \"set\" or \"reset\"; got %q", *body.Dominant),
				Subject:  &def.DefRange,
			})
		}
	}

	// --- behavior (only the non-temporal subset applies to a flipflop) ---
	behavior := Behavior{Inhibit: body.Inhibit}
	behavior.Cooldown, moreDiags = parseOptDuration(config, body.Cooldown)
	diags = diags.Extend(moreDiags)
	if body.Latch != nil {
		behavior.Latch = *body.Latch
	}
	if body.Invert != nil {
		behavior.Invert = *body.Invert
	}
	if body.StartActive != nil {
		behavior.StartActive = *body.StartActive
	}
	if diags.HasErrors() {
		return diags
	}

	f := &FlipflopCondition{
		name:        def.Name,
		config:      config,
		sm:          NewStateMachine(behavior, RealClock{}),
		dominantSet: dominantSet,
	}

	// --- build wires and bind each referenced source to the wires using it ---
	byWatchable := map[richcty.Watchable]*flipflopSource{}
	src := func(w richcty.Watchable) *flipflopSource {
		s := byWatchable[w]
		if s == nil {
			s = &flipflopSource{ff: f, watchable: w}
			byWatchable[w] = s
			f.sources = append(f.sources, s)
		}
		return s
	}
	makeWire := func(expr hcl.Expression, edge edgeMode, name string, mark func(*flipflopSource)) *wire {
		re, d := cfg.NewReactiveExpr(expr, config.EvalCtx(), nil)
		diags = diags.Extend(d)
		for _, w := range re.Sources() {
			mark(src(w))
		}
		return &wire{name: name, re: re, edge: edge}
	}

	if hasSet {
		f.setWire = makeWire(body.SetOn, setEdge, "set_on", func(s *flipflopSource) { s.feedsSet = true })
	}
	if hasReset {
		f.resetWire = makeWire(body.ResetOn, resetEdge, "reset_on", func(s *flipflopSource) { s.feedsReset = true })
	}
	if hasToggle {
		f.toggleWire = makeWire(body.ToggleOn, toggleEdge, "toggle_on", func(s *flipflopSource) { s.feedsToggle = true })
	}
	if hasGate {
		f.gateWire = makeWire(body.GateOn, gateEdge, "gate_on", func(s *flipflopSource) { s.feedsGate = true })
	}
	if hasSetFrom {
		re, d := cfg.NewReactiveExpr(body.SetFrom, config.EvalCtx(), nil)
		diags = diags.Extend(d)
		for _, w := range re.Sources() {
			src(w).feedsSetFrom = true
		}
		f.setFromExpr = re
	}

	if cfg.IsExpressionProvided(body.Inhibit) {
		re, d := cfg.NewReactiveExpr(body.Inhibit, config.EvalCtx(), func(ctx context.Context, v cty.Value) {
			if v.Type() != cty.Bool || v.IsNull() || !v.IsKnown() {
				config.UserLogger.Warn("condition inhibit expression did not produce a boolean",
					zap.String("name", f.name), zap.String("type", v.Type().FriendlyName()))
				return
			}
			f.sm.SetInhibited(ctx, v.True())
		})
		diags = diags.Extend(d)
		f.inhibitExpr = re
	}
	if diags.HasErrors() {
		return diags
	}

	tp, _ := config.ResolveTracerProvider(hcl.Expression(nil))
	f.hooks = NewHookDispatcher(def.Name, Hooks{
		OnInit:       body.OnInit,
		OnActivate:   body.OnActivate,
		OnDeactivate: body.OnDeactivate,
	}, config, tp)
	if f.hooks != nil {
		f.sm.Watch(f.hooks)
	}

	config.CtyConditionMap[def.Name] = newFlipflopConditionCapsule(f)
	config.EvalCtx().Variables["condition"] = cfg.CtyObjectOrEmpty(config.CtyConditionMap)
	config.Startables = append(config.Startables, f)
	if f.hooks != nil {
		config.PostStartables = append(config.PostStartables, f)
	}
	config.Stoppables = append(config.Stoppables, f)
	return diags
}
