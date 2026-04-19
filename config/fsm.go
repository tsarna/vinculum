package config

import (
	"context"
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	fsm "github.com/tsarna/vinculum-fsm"
	"github.com/zclconf/go-cty/cty"

	"github.com/tsarna/vinculum/hclutil"
)

// FsmTopLevel is the gohcl-decoded envelope for an fsm block. Sub-blocks
// (state, event, storage) are parsed manually from RemainingBody because
// we need declaration-order iteration and raw hcl.Expression access.
type FsmTopLevel struct {
	Name          string         `hcl:",label"`
	Initial       string         `hcl:"initial"`
	QueueSize     *int           `hcl:"queue_size,optional"`
	ShutdownEvent *string        `hcl:"shutdown_event,optional"`
	Disabled      bool           `hcl:"disabled,optional"`
	Tracing       hcl.Expression `hcl:"tracing,optional"`
	OnChange      hcl.Expression `hcl:"on_change,optional"`
	OnError       hcl.Expression `hcl:"on_error,optional"`
	RemainingBody hcl.Body       `hcl:",remain"`
}

// FsmBlockHandler processes fsm blocks.
type FsmBlockHandler struct {
	BlockHandlerBase
	instances      map[string]*fsm.Instance
	initialStorage map[string]map[string]cty.Value    // fsmName -> key -> value
	reactiveExprs  map[string][]*ReactiveExpr          // fsmName -> reactive when exprs
	edgeState      map[string]map[string]*bool         // fsmName -> eventName -> last bool
}

func NewFsmBlockHandler() *FsmBlockHandler {
	return &FsmBlockHandler{
		instances: make(map[string]*fsm.Instance),
	}
}

func (h *FsmBlockHandler) GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics) {
	if len(block.Labels) == 1 {
		return "fsm." + block.Labels[0], nil
	}
	return "", nil
}

func (h *FsmBlockHandler) GetBlockDependencies(block *hcl.Block) ([]string, hcl.Diagnostics) {
	// Exclude runtime-only attributes from dependency extraction.
	// guard is runtime-evaluated during event processing, not at config time.
	deps := ExtractBlockDependencies(block, "action", "on_event", "guard")

	// Filter out self-references: hooks like on_entry = increment(fsm.door, ...)
	// legitimately reference the FSM itself, but that's not a config-time dependency.
	selfID := "fsm." + block.Labels[0]
	filtered := deps[:0]
	for _, d := range deps {
		if d != selfID {
			filtered = append(filtered, d)
		}
	}
	return filtered, nil
}

// Preprocess validates the block and registers the FSM name for duplicate
// detection. The actual instance is created in FinishPreprocessing.
func (h *FsmBlockHandler) Preprocess(block *hcl.Block) hcl.Diagnostics {
	if len(block.Labels) != 1 {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid fsm block",
			Detail:   "fsm blocks require exactly one label: the name",
			Subject:  block.DefRange.Ptr(),
		}}
	}

	name := block.Labels[0]
	if _, exists := h.instances[name]; exists {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Duplicate fsm",
			Detail:   fmt.Sprintf("FSM %q is already defined", name),
			Subject:  block.DefRange.Ptr(),
		}}
	}

	// Reserve the name; the instance is created in FinishPreprocessing.
	h.instances[name] = nil
	return nil
}

// FinishPreprocessing creates placeholder instances and populates the capsule
// map so that fsm.<name> references can be resolved during dependency analysis.
// Process() later calls inst.Configure() to replace the placeholder definition
// with the real one -- this works because the capsule wraps a pointer to the
// Instance, so all resolved references see the updated state.
func (h *FsmBlockHandler) FinishPreprocessing(config *Config) hcl.Diagnostics {
	placeholder := fsm.NewDefinition("__placeholder__")
	placeholder.AddState(&fsm.StateDef{Name: "__placeholder__"})
	placeholder.AddEvent(&fsm.EventDef{
		Name:        "__placeholder__",
		Transitions: []*fsm.TransitionDef{{FromState: "__placeholder__", ToState: "__placeholder__"}},
	})

	for name := range h.instances {
		inst := fsm.NewInstance(name, placeholder)
		h.instances[name] = inst
		config.CtyFsmMap[name] = fsm.NewFsmCapsule(inst)
	}
	if len(config.CtyFsmMap) > 0 {
		config.Constants["fsm"] = cty.ObjectVal(config.CtyFsmMap)
	}
	return nil
}

func (h *FsmBlockHandler) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	var topLevel FsmTopLevel
	diags := gohcl.DecodeBody(block.Body, config.evalCtx, &topLevel)
	if diags.HasErrors() {
		return diags
	}

	// gohcl.DecodeBody doesn't populate label fields from Body alone.
	topLevel.Name = block.Labels[0]

	if topLevel.Disabled {
		delete(h.instances, topLevel.Name)
		delete(config.CtyFsmMap, topLevel.Name)
		if len(config.CtyFsmMap) > 0 {
			config.Constants["fsm"] = cty.ObjectVal(config.CtyFsmMap)
		} else {
			delete(config.Constants, "fsm")
		}
		return nil
	}

	name := topLevel.Name

	// Build the definition.
	def := fsm.NewDefinition(topLevel.Initial)
	if topLevel.QueueSize != nil {
		def.QueueSize = *topLevel.QueueSize
	}
	if topLevel.ShutdownEvent != nil {
		def.ShutdownEvent = *topLevel.ShutdownEvent
	}

	// Parse sub-blocks from RemainingBody.
	syntaxBody, ok := topLevel.RemainingBody.(*hclsyntax.Body)
	if !ok {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Internal error",
			Detail:   "FSM block body is not hclsyntax.Body",
			Subject:  block.DefRange.Ptr(),
		}}
	}

	parseDiags := h.parseSubBlocks(config, def, name, syntaxBody)
	diags = diags.Extend(parseDiags)
	if diags.HasErrors() {
		return diags
	}

	// Validate the definition.
	if err := def.Validate(); err != nil {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid FSM definition",
			Detail:   err.Error(),
			Subject:  block.DefRange.Ptr(),
		}}
	}

	// Validation warnings.
	for stateName, stateDef := range def.States {
		if stateName != def.InitialState && stateDef.OnInit != nil {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagWarning,
				Summary:  "on_init on non-initial state",
				Detail:   fmt.Sprintf("on_init is declared on state %q but will never fire (initial state is %q)", stateName, def.InitialState),
				Subject:  block.DefRange.Ptr(),
			})
		}
	}

	// Wire machine-level hooks.
	if IsExpressionProvided(topLevel.OnChange) {
		expr := topLevel.OnChange
		def.OnChange = func(ctx context.Context, hookCtx *fsm.HookContext) error {
			return evalHookExpr(ctx, config, expr, hookCtx)
		}
	}

	if IsExpressionProvided(topLevel.OnError) {
		expr := topLevel.OnError
		def.OnError = func(ctx context.Context, hookCtx *fsm.HookContext) {
			evalHookExpr(ctx, config, expr, hookCtx)
		}
	}

	// Resolve tracing.
	tp, tracingDiags := config.ResolveTracerProvider(topLevel.Tracing)
	if tracingDiags.HasErrors() {
		return tracingDiags
	}
	// Configure the existing instance (created during Preprocess) with the
	// real definition. This preserves the capsule identity so that references
	// resolved during dependency analysis remain valid.
	inst := h.instances[name]
	inst.Configure(def)
	if tp != nil {
		inst.SetTracerProvider(tp)
	}

	// Apply initial storage values.
	if storageVals, ok := h.initialStorage[name]; ok {
		for k, v := range storageVals {
			inst.SetInitialStorage(k, v)
		}
	}

	// Register lifecycle. Reactive exprs are started after the instance
	// (so the event goroutine is running) and stopped before it.
	reactiveExprs := h.reactiveExprs[name]
	config.Startables = append(config.Startables, &fsmStartable{inst: inst, reactiveExprs: reactiveExprs})
	config.Stoppables = append(config.Stoppables, &fsmStoppable{inst: inst, reactiveExprs: reactiveExprs})

	return diags
}

// parseSubBlocks parses state, event, and storage sub-blocks from the FSM body.
func (h *FsmBlockHandler) parseSubBlocks(config *Config, def *fsm.Definition, fsmName string, body *hclsyntax.Body) hcl.Diagnostics {
	var diags hcl.Diagnostics

	for _, sub := range body.Blocks {
		switch sub.Type {
		case "state":
			d := h.parseStateBlock(config, def, sub)
			diags = diags.Extend(d)

		case "event":
			d := h.parseEventBlock(config, def, fsmName, sub)
			diags = diags.Extend(d)

		case "storage":
			d := h.parseStorageBlock(config, fsmName, sub)
			diags = diags.Extend(d)

		default:
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Unexpected block type",
				Detail:   fmt.Sprintf("Unexpected block type %q in fsm block; expected state, event, or storage", sub.Type),
				Subject:  sub.DefRange().Ptr(),
			})
		}
	}

	return diags
}

func (h *FsmBlockHandler) parseStateBlock(config *Config, def *fsm.Definition, block *hclsyntax.Block) hcl.Diagnostics {
	if len(block.Labels) != 1 {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid state block",
			Detail:   "state blocks require exactly one label: the state name",
			Subject:  block.DefRange().Ptr(),
		}}
	}

	stateName := block.Labels[0]
	stateDef := &fsm.StateDef{Name: stateName}

	// Parse optional hook attributes.
	for attrName, attr := range block.Body.Attributes {
		expr := attr.Expr
		switch attrName {
		case "on_init":
			stateDef.OnInit = makeHookFunc(config, expr)
		case "on_entry":
			stateDef.OnEntry = makeHookFunc(config, expr)
		case "on_exit":
			stateDef.OnExit = makeHookFunc(config, expr)
		case "on_event":
			stateDef.OnEvent = makeHookFunc(config, expr)
		default:
			return hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Unexpected attribute",
				Detail:   fmt.Sprintf("Unexpected attribute %q in state block; expected on_init, on_entry, on_exit, or on_event", attrName),
				Subject:  attr.SrcRange.Ptr(),
			}}
		}
	}

	if err := def.AddState(stateDef); err != nil {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid state",
			Detail:   err.Error(),
			Subject:  block.DefRange().Ptr(),
		}}
	}

	return nil
}

func (h *FsmBlockHandler) parseEventBlock(config *Config, def *fsm.Definition, fsmName string, block *hclsyntax.Block) hcl.Diagnostics {
	if len(block.Labels) != 1 {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid event block",
			Detail:   "event blocks require exactly one label: the event name",
			Subject:  block.DefRange().Ptr(),
		}}
	}

	eventName := block.Labels[0]
	eventDef := &fsm.EventDef{Name: eventName}

	// Parse attributes.
	for attrName, attr := range block.Body.Attributes {
		switch attrName {
		case "topic":
			val, attrDiags := attr.Expr.Value(config.evalCtx)
			if attrDiags.HasErrors() {
				return attrDiags
			}
			if val.Type() != cty.String {
				return hcl.Diagnostics{{
					Severity: hcl.DiagError,
					Summary:  "Invalid topic",
					Detail:   "topic must be a string",
					Subject:  attr.SrcRange.Ptr(),
				}}
			}
			eventDef.TopicPattern = val.AsString()

		case "when":
			eventDef.HasWhen = true
			reDiags := h.wireReactiveEvent(config, fsmName, eventName, attr.Expr)
			if reDiags.HasErrors() {
				return reDiags
			}

		default:
			return hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Unexpected attribute",
				Detail:   fmt.Sprintf("Unexpected attribute %q in event block; expected topic or when", attrName),
				Subject:  attr.SrcRange.Ptr(),
			}}
		}
	}

	// Parse transition sub-blocks.
	var diags hcl.Diagnostics
	for _, sub := range block.Body.Blocks {
		if sub.Type != "transition" {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Unexpected block type",
				Detail:   fmt.Sprintf("Unexpected block type %q in event block; expected transition", sub.Type),
				Subject:  sub.DefRange().Ptr(),
			})
			continue
		}

		trDiags := h.parseTransitionBlock(config, eventDef, sub)
		diags = diags.Extend(trDiags)
	}

	if err := def.AddEvent(eventDef); err != nil {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid event",
			Detail:   err.Error(),
			Subject:  block.DefRange().Ptr(),
		})
	}

	return diags
}

func (h *FsmBlockHandler) parseTransitionBlock(config *Config, eventDef *fsm.EventDef, block *hclsyntax.Block) hcl.Diagnostics {
	if len(block.Labels) != 2 {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid transition block",
			Detail:   "transition blocks require two labels: from-state and to-state",
			Subject:  block.DefRange().Ptr(),
		}}
	}

	tr := &fsm.TransitionDef{
		FromState: block.Labels[0],
		ToState:   block.Labels[1],
	}

	for attrName, attr := range block.Body.Attributes {
		switch attrName {
		case "guard":
			expr := attr.Expr
			tr.Guard = func(ctx context.Context, hookCtx *fsm.HookContext) (bool, error) {
				evalCtx, err := buildHookEvalContext(ctx, config, hookCtx)
				if err != nil {
					return false, err
				}
				val, diags := expr.Value(evalCtx)
				if diags.HasErrors() {
					return false, fmt.Errorf("guard evaluation failed: %s", diags.Error())
				}
				if val.Type() != cty.Bool {
					return false, fmt.Errorf("guard must return bool, got %s", val.Type().FriendlyName())
				}
				return val.True(), nil
			}

		case "action":
			tr.Action = makeHookFunc(config, attr.Expr)

		default:
			return hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Unexpected attribute",
				Detail:   fmt.Sprintf("Unexpected attribute %q in transition block; expected guard or action", attrName),
				Subject:  attr.SrcRange.Ptr(),
			}}
		}
	}

	eventDef.Transitions = append(eventDef.Transitions, tr)
	return nil
}

func (h *FsmBlockHandler) parseStorageBlock(config *Config, fsmName string, block *hclsyntax.Block) hcl.Diagnostics {
	if len(block.Labels) != 0 {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid storage block",
			Detail:   "storage blocks take no labels",
			Subject:  block.DefRange().Ptr(),
		}}
	}

	var diags hcl.Diagnostics
	for attrName, attr := range block.Body.Attributes {
		val, attrDiags := attr.Expr.Value(config.evalCtx)
		diags = diags.Extend(attrDiags)
		if attrDiags.HasErrors() {
			continue
		}
		// Store on a temporary map; will be applied to the real instance in Process().
		if h.initialStorage == nil {
			h.initialStorage = make(map[string]map[string]cty.Value)
		}
		if h.initialStorage[fsmName] == nil {
			h.initialStorage[fsmName] = make(map[string]cty.Value)
		}
		h.initialStorage[fsmName][attrName] = val
	}

	return diags
}

// wireReactiveEvent creates a ReactiveExpr for a `when` expression and
// registers it for lifecycle management. The callback implements edge-
// triggering: the event only fires on the false→true transition of the
// expression, not continuously while it remains true.
func (h *FsmBlockHandler) wireReactiveEvent(config *Config, fsmName string, eventName string, expr hclsyntax.Expression) hcl.Diagnostics {
	// Initialize edge state tracking for this FSM/event.
	if h.edgeState == nil {
		h.edgeState = make(map[string]map[string]*bool)
	}
	if h.edgeState[fsmName] == nil {
		h.edgeState[fsmName] = make(map[string]*bool)
	}
	lastWasTrue := new(bool)
	h.edgeState[fsmName][eventName] = lastWasTrue

	inst := h.instances[fsmName]

	re, diags := NewReactiveExpr(expr, config.evalCtx, func(ctx context.Context, v cty.Value) {
		// Determine if the expression is truthy.
		isTrue := false
		if v.IsKnown() && !v.IsNull() && v.Type() == cty.Bool {
			isTrue = v.True()
		}

		// Edge-trigger: only fire on false→true transition.
		wasTrueVal := *lastWasTrue
		*lastWasTrue = isTrue
		if isTrue && !wasTrueVal {
			inst.EnqueueEvent(fsm.Event{Name: eventName})
		}
	})

	if h.reactiveExprs == nil {
		h.reactiveExprs = make(map[string][]*ReactiveExpr)
	}
	h.reactiveExprs[fsmName] = append(h.reactiveExprs[fsmName], re)

	return diags
}

// makeHookFunc wraps an HCL expression as an fsm.HookFunc closure.
func makeHookFunc(config *Config, expr hclsyntax.Expression) fsm.HookFunc {
	return func(ctx context.Context, hookCtx *fsm.HookContext) error {
		return evalHookExpr(ctx, config, expr, hookCtx)
	}
}

// evalHookExpr evaluates an HCL expression in a hook context. The eval
// context is built once per transition and cached in hookCtx.UserData so
// that multiple hooks sharing the same HookContext avoid redundant work.
func evalHookExpr(ctx context.Context, config *Config, expr hcl.Expression, hookCtx *fsm.HookContext) error {
	evalCtx, ok := hookCtx.UserData.(*hcl.EvalContext)
	if !ok || evalCtx == nil {
		var err error
		evalCtx, err = buildHookEvalContext(ctx, config, hookCtx)
		if err != nil {
			return err
		}
		hookCtx.UserData = evalCtx
	}
	_, diags := expr.Value(evalCtx)
	if diags.HasErrors() {
		return fmt.Errorf("%s", diags.Error())
	}
	return nil
}

// buildHookEvalContext creates a child HCL evaluation context with ctx.* variables
// populated from the hook context.
func buildHookEvalContext(ctx context.Context, config *Config, hookCtx *fsm.HookContext) (*hcl.EvalContext, error) {
	builder := hclutil.NewEvalContext(ctx)

	if hookCtx.Event != "" {
		builder = builder.WithStringAttribute("event", hookCtx.Event)
	}
	if hookCtx.OldState != "" {
		builder = builder.WithStringAttribute("old_state", hookCtx.OldState)
	}
	if hookCtx.NewState != "" {
		builder = builder.WithStringAttribute("new_state", hookCtx.NewState)
	}
	if hookCtx.Topic != "" {
		builder = builder.WithStringAttribute("topic", hookCtx.Topic)
	}
	if hookCtx.Error != "" {
		builder = builder.WithStringAttribute("error", hookCtx.Error)
	}
	if hookCtx.Hook != "" {
		builder = builder.WithStringAttribute("hook", hookCtx.Hook)
	}

	// Event value. cty.NilVal means no value was set; treat as null.
	if hookCtx.EventValue != cty.NilVal && hookCtx.EventValue.IsKnown() && !hookCtx.EventValue.IsNull() {
		builder = builder.WithAttribute("event_value", hookCtx.EventValue)
	} else {
		builder = builder.WithAttribute("event_value", cty.NullVal(cty.DynamicPseudoType))
	}

	// Event fields.
	if len(hookCtx.EventFields) > 0 {
		ctyFields := make(map[string]cty.Value, len(hookCtx.EventFields))
		for k, v := range hookCtx.EventFields {
			ctyFields[k] = cty.StringVal(v)
		}
		builder = builder.WithAttribute("event_fields", cty.ObjectVal(ctyFields))
	} else {
		builder = builder.WithAttribute("event_fields", cty.NullVal(cty.DynamicPseudoType))
	}

	// Topic params.
	if len(hookCtx.TopicParams) > 0 {
		ctyParams := make(map[string]cty.Value, len(hookCtx.TopicParams))
		for k, v := range hookCtx.TopicParams {
			ctyParams[k] = cty.StringVal(v)
		}
		builder = builder.WithAttribute("topic_params", cty.ObjectVal(ctyParams))
	} else {
		builder = builder.WithAttribute("topic_params", cty.NullVal(cty.DynamicPseudoType))
	}

	// FSM capsule.
	if hookCtx.Fsm != cty.NilVal {
		builder = builder.WithAttribute("fsm", hookCtx.Fsm)
	}

	return builder.BuildEvalContext(config.evalCtx)
}

// fsmStartable wraps an FSM instance for the Startable interface.
type fsmStartable struct {
	inst          *fsm.Instance
	reactiveExprs []*ReactiveExpr
}

func (s *fsmStartable) Start() error {
	// Start the event processing goroutine first.
	if err := s.inst.Start(context.Background()); err != nil {
		return err
	}
	// Then start reactive when expressions so they can enqueue events.
	for _, re := range s.reactiveExprs {
		if diags := re.Start(context.Background()); diags.HasErrors() {
			return fmt.Errorf("fsm %q: reactive when: %s", s.inst.Name(), diags.Error())
		}
	}
	return nil
}

// fsmStoppable wraps an FSM instance for the Stoppable interface.
type fsmStoppable struct {
	inst          *fsm.Instance
	reactiveExprs []*ReactiveExpr
}

func (s *fsmStoppable) Stop() error {
	// Stop reactive expressions first so they stop enqueuing events.
	for _, re := range s.reactiveExprs {
		re.Stop()
	}
	return s.inst.Stop()
}
