package config

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/hashicorp/hcl/v2"
	richcty "github.com/tsarna/rich-cty-types"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

// checkBody declares the attributes a check block accepts.
//
// `input` stays an unevaluated expression: a check is the one construct in the
// language that is evaluated by the prober rather than by an event, and
// evaluating it here would answer a question nobody had asked yet.
type checkBody struct {
	Input    hcl.Expression `hcl:"input"`
	Reason   string         `hcl:"reason,optional"`
	Probe    string         `hcl:"probe,optional"`
	Timeout  hcl.Expression `hcl:"timeout,optional"`
	Disabled bool           `hcl:"disabled,optional"`

	// ProbeRange underlines the attribute itself when the value is rejected.
	// A string carries no range of its own, and pointing at the whole block
	// would leave the reader looking for which line was meant.
	ProbeRange hcl.Range `hcl:"probe,attr_range"`
}

// CheckBlockHandler processes `check` blocks.
type CheckBlockHandler struct {
	BlockHandlerBase
}

func NewCheckBlockHandler() *CheckBlockHandler { return &CheckBlockHandler{} }

// Schema describes the check block for `vinculum schema`.
func (h *CheckBlockHandler) Schema() TypeSchema { return checkSchema }

var checkSchema = TypeSchema{
	Sample:  &checkBody{},
	Summary: "Declares a health check.",
	Doc: `Adds a condition of your own to one of the process's health probes, available in
expressions as ` + "`check.<name>`" + `.

A ` + "`check`" + ` is meaning attached to a boolean: this one, when false, means do not
send this process traffic; that one, when false, means the process is wedged. That
is why it is a block of its own rather than an attribute on
[` + "`condition`" + `](condition.md), which is behavior over a boolean — debounce it,
delay it, latch it — with no opinion about what the boolean is for. A check that
needs hysteresis names a condition; a condition that wants to be a probe is wrapped
by a check. Neither knows more than its own job.

` + "```hcl" + `
condition "timer" "broker_backlog_ok" {
    input            = get(var.backlog) < 1000
    deactivate_after = "30s"          # a momentary spike is not an outage
}

check "backlog" {
    input  = get(condition.broker_backlog_ok)
    reason = "message backlog above threshold for 30s"
}
` + "```" + `

See [health](health.md).`,
	DocPage: "health.md",
	Attrs: map[string]AttrMeta{
		"input": {
			Summary: "What this check tests.",
			Doc: `Evaluated on each probe rather than on an event, so it is the one place a
configuration gets to compute something at the moment a prober asks.

How the result is read:

| Result | Meaning |
|---|---|
| ` + "`true`" + ` | passing |
| ` + "`false`" + ` | failing, with ` + "`reason`" + ` as the reason |
| a string | failing, with that string as the reason |
| ` + "`null`" + ` | failing, reason ` + "`check returned null`" + ` |
| ` + "`{ready = bool, reason = string}`" + ` | as stated |
| an evaluation error | failing, with the error as the reason |

A returned string is always a complaint: a healthy check has nothing to say, the
same principle that makes an empty ` + "`health::failing()`" + ` mean healthy.`,
			Hint:    HintExpression,
			Context: "check",
		},
		"reason": {
			Summary: "Why a failing check matters, in words.",
			Doc: "Reported as the reason whenever `input` says the check fails without supplying " +
				"one of its own. It reads as a fragment completing \"`check.<name>` is not ready: …\".",
			Default: "check failed",
		},
		"probe": {
			Summary: "Which probe this check belongs to.",
			Doc: "`ready` means a failure should stop traffic being routed here — the pod leaves " +
				"the load balancer and comes back when the check passes again. `live` means a " +
				"failure should **restart the process**, so use it only for a wedge nothing else " +
				"can clear, never for a dependency being down. A check participates in exactly " +
				"one probe; a signal that belongs to both is two checks over one condition, " +
				"which is rare and should be conspicuous.",
			Enum:    []string{ProbeReady, ProbeLive},
			Default: ProbeReady,
		},
		"timeout": {
			Summary: "How long to allow `input` before giving up on it.",
			Doc: "A check that exceeds this is reported as failing with `timed out after …`, so one " +
				"slow dependency cannot hold a probe open past the deadline its caller set.",
			Hint:    HintDuration,
			Default: "2s",
		},
		"disabled": DisabledAttr,
	},
}

func init() {
	// The ctx a check's `input` sees. It is the standard no-message-in-flight
	// shape — nothing is being delivered, a prober is asking a question — but
	// it is *derived from the ctx of whoever asked* rather than fabricated, so
	// the caller's span parents whatever `input` does, and the caller's
	// baggage, auth, and deadline all reach it.
	RegisterContextSchema("check", ContextSchema{
		Summary: "Evaluated on each health probe that consults this check.",
		Doc: "Derived from the context of whatever asked for the probe — an HTTP request to " +
			"`/readyz`, a `health::` call, a metrics scrape — so a slow probe is diagnosable " +
			"as \"the database check took 1.8s of it\" rather than as an unexplained pause. " +
			"There is no message in flight, so there are no message fields.",
	})
}

func (h *CheckBlockHandler) GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics) {
	return "check." + block.Labels[0], nil
}

// GetBlockDependencies excludes `input`, which is evaluated at probe time
// rather than at build time. A check over a client is not waiting for that
// client to be processed first — and treating it as a dependency would make
// `input = sql::ping(client.db)` order the two, which is meaningless when the
// expression does not run until something probes.
func (h *CheckBlockHandler) GetBlockDependencies(block *hcl.Block) ([]string, hcl.Diagnostics) {
	return ExtractBlockDependencies(block, "input"), nil
}

func (h *CheckBlockHandler) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	name := block.Labels[0]

	var body checkBody
	diags := DecodeBody(block.Body, config.evalCtx, &body)
	if diags.HasErrors() {
		return diags
	}

	if body.Disabled {
		return nil
	}

	// gohcl never marks an hcl.Expression field required — absence is
	// representable as a null expression — so a check with nothing to test
	// would otherwise be accepted and then pass every probe. The schema says
	// `input` is required; this is what makes that true.
	if !IsExpressionProvided(body.Input) {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Missing required argument",
			Detail:   "The argument \"input\" is required, but no definition was found. A check with nothing to test would pass every probe.",
			Subject:  block.DefRange.Ptr(),
		}}
	}

	if _, exists := config.CtyCheckMap[name]; exists {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Duplicate check",
			Detail: fmt.Sprintf("Check %q is already defined. Rename one, or set disabled on the "+
				"one this configuration should not use.", name),
			Subject: block.DefRange.Ptr(),
		}}
	}

	probe, probeDiags := parseProbe(body.Probe, body.ProbeRange, block.DefRange)
	diags = diags.Extend(probeDiags)

	timeout, timeoutDiags := config.ParseDurationOrDefault(body.Timeout, 0)
	diags = diags.Extend(timeoutDiags)

	if diags.HasErrors() {
		return diags
	}

	check := &Check{
		name:   name,
		probe:  probe,
		reason: body.Reason,
		input:  body.Input,
		config: config,
	}

	config.CtyCheckMap[name] = NewCheckCapsule(check)
	config.evalCtx.Variables["check"] = CtyObjectOrEmpty(config.CtyCheckMap)
	config.Health.RegisterProbe(probe, "check", "", name, check, timeout)

	return nil
}

// parseProbe validates the `probe` attribute, defaulting to readiness.
func parseProbe(s string, attrRange, defRange hcl.Range) (string, hcl.Diagnostics) {
	if s == "" {
		return ProbeReady, nil
	}
	if ValidProbe(s) {
		return s, nil
	}
	subject := &defRange
	if attrRange.Filename != "" {
		subject = &attrRange
	}
	return ProbeReady, hcl.Diagnostics{{
		Severity: hcl.DiagError,
		Summary:  "Invalid probe",
		Detail:   fmt.Sprintf("probe must be %q or %q; got %q.", ProbeReady, ProbeLive, s),
		Subject:  subject,
	}}
}

// ---------------------------------------------------------------------------
// The check itself
// ---------------------------------------------------------------------------

// Check is one `check` block: a named, on-demand, reason-carrying probe over an
// expression.
//
// It is Gettable and Watchable, so `get(check.database)` reads its last result
// and `trigger "watch" { watch = check.database }` fires on that one check's
// transitions without involving the aggregate.
type Check struct {
	richcty.WatchableMixin

	name   string
	probe  string
	reason string
	input  hcl.Expression
	config *Config

	mu    sync.Mutex
	known bool
	ready bool
}

// Name returns the check's block name.
func (c *Check) Name() string { return c.name }

// Probe returns which probe this check contributes to.
func (c *Check) Probe() string { return c.probe }

// Ready implements Readyable: it evaluates `input` and reports what it said.
//
// The evaluation context is built from ctx, so whatever the expression does is
// parented by the span of whoever is probing and bounded by their deadline.
func (c *Check) Ready(ctx context.Context) error {
	err := c.evaluate(ctx)
	c.observe(ctx, err == nil)
	return err
}

func (c *Check) evaluate(ctx context.Context) error {
	evalCtx, err := hclutil.NewEvalContext(ctx).BuildEvalContext(c.config.EvalCtx())
	if err != nil {
		return fmt.Errorf("building evaluation context: %w", err)
	}

	val, diags := c.input.Value(evalCtx)
	if diags.HasErrors() {
		// The expression is the user's, so its failure is the check's answer
		// rather than an internal error: a check whose input cannot be
		// evaluated is exactly a check that is not passing.
		return errors.New(diags.Error())
	}

	return c.interpret(val)
}

// interpret turns the value `input` produced into a pass or a reasoned failure.
func (c *Check) interpret(val cty.Value) error {
	switch {
	case val.IsNull():
		return errors.New("check returned null")

	case !val.IsKnown():
		return errors.New("check returned an unknown value")

	case val.Type() == cty.Bool:
		if val.True() {
			return nil
		}
		return errors.New(c.defaultReason())

	case val.Type() == cty.String:
		// A returned string is always a complaint: a healthy check has nothing
		// to say, the same principle that makes an empty health::failing()
		// mean healthy.
		if s := val.AsString(); s != "" {
			return errors.New(s)
		}
		return errors.New(c.defaultReason())

	case val.Type().IsObjectType() && val.Type().HasAttribute("ready"):
		return c.interpretObject(val)

	default:
		return fmt.Errorf("check produced %s, which is not a pass or a reason",
			val.Type().FriendlyName())
	}
}

// interpretObject reads the explicit {ready = bool, reason = string} form.
func (c *Check) interpretObject(val cty.Value) error {
	ready, err := convert.Convert(val.GetAttr("ready"), cty.Bool)
	if err != nil || ready.IsNull() || !ready.IsKnown() {
		return errors.New("check returned an object whose ready attribute is not a boolean")
	}
	if ready.True() {
		return nil
	}
	if val.Type().HasAttribute("reason") {
		reason, err := convert.Convert(val.GetAttr("reason"), cty.String)
		if err == nil && !reason.IsNull() && reason.IsKnown() && reason.AsString() != "" {
			return errors.New(reason.AsString())
		}
	}
	return errors.New(c.defaultReason())
}

func (c *Check) defaultReason() string {
	if c.reason != "" {
		return c.reason
	}
	return "check failed"
}

// Get implements richcty.Gettable: the check's last evaluated result.
//
// A check that nothing has probed yet reads as its args[0] default, or true —
// the same "unknown is treated as ready" rule the aggregator applies to a
// component that reports nothing.
func (c *Check) Get(ctx context.Context, args []cty.Value) (cty.Value, error) {
	c.mu.Lock()
	known, ready := c.known, c.ready
	c.mu.Unlock()

	if !known {
		if len(args) > 0 && !args[0].IsNull() {
			return args[0], nil
		}
		return cty.True, nil
	}
	return cty.BoolVal(ready), nil
}

// observe records a result and notifies watchers if it changed. Like sys.ready
// and unlike var, a repeat of the value already held is not an event.
func (c *Check) observe(ctx context.Context, ready bool) {
	c.mu.Lock()
	old, known := c.ready, c.known
	c.ready, c.known = ready, true
	c.mu.Unlock()

	if !known || old == ready {
		return
	}
	c.NotifyAll(ctx, c, cty.BoolVal(old), cty.BoolVal(ready))
}

// CheckCapsuleType is the capsule behind check.<name>.
var CheckCapsuleType = cty.CapsuleWithOps("check", reflect.TypeOf(Check{}), &cty.CapsuleOps{
	GoString:     func(val any) string { return fmt.Sprintf("check(%p)", val) },
	TypeGoString: func(_ reflect.Type) string { return "Check" },
})

func NewCheckCapsule(c *Check) cty.Value {
	return cty.CapsuleVal(CheckCapsuleType, c)
}

// GetCheckFromCapsule extracts a Check from check.<name>.
func GetCheckFromCapsule(val cty.Value) (*Check, error) {
	if val.Type() != CheckCapsuleType {
		return nil, fmt.Errorf("expected check capsule, got %s", val.Type().FriendlyName())
	}
	check, ok := val.EncapsulatedValue().(*Check)
	if !ok {
		return nil, fmt.Errorf("encapsulated value is not a Check, got %T", val.EncapsulatedValue())
	}
	return check, nil
}
