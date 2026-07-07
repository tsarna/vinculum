package repl

import (
	"context"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	_ "github.com/tsarna/vinculum/ambient"   // register env.* and other ambient providers
	_ "github.com/tsarna/vinculum/functions" // register stdlib + generic functions
)

// newTestHost builds a real Config from inline VCL and returns the REPL host
// adapter over it, at InfoLevel logging.
func newTestHost(t *testing.T, src string) *host {
	t.Helper()
	cfg, diags := config.NewConfig().
		WithSources([]byte(src)).
		WithLogger(zap.NewNop()).
		Build()
	if diags.HasErrors() {
		t.Fatalf("build config: %s", diags)
	}
	return newHost(cfg, NewInteractiveLogging(zapcore.InfoLevel))
}

// evalString parses and evaluates a single HCL expression against the host's
// eval context (the same chain the engine uses), for asserting vinculum-specific
// values.
func evalString(t *testing.T, h *host, src string) cty.Value {
	t.Helper()
	parent, finish, err := h.EvalContext(context.Background(), src)
	if err != nil {
		t.Fatalf("EvalContext(%q): %v", src, err)
	}
	if finish != nil {
		defer finish()
	}
	expr, pdiags := hclsyntax.ParseExpression([]byte(src), "<test>", hcl.InitialPos)
	if pdiags.HasErrors() {
		t.Fatalf("parse %q: %s", src, pdiags)
	}
	v, diags := expr.Value(parent.NewChild())
	if diags.HasErrors() {
		t.Fatalf("eval %q: %s", src, diags)
	}
	return v
}

// TestHostEvalContext checks that the per-eval context exposes vinculum's live
// namespaces: a configured bus resolves to its capsule value, and ctx is present
// (ctx.trace_id is empty under the NOOP tracer).
func TestHostEvalContext(t *testing.T) {
	h := newTestHost(t, `bus "main" {}`)

	bus := evalString(t, h, "bus.main")
	// The bus is a capsule (or rich object carrying one), not a plain value.
	if ty := bus.Type(); !ty.IsCapsuleType() && !(ty.IsObjectType() && ty.HasAttribute("_capsule")) {
		t.Fatalf("bus.main type = %s, want a capsule/rich object", ty.FriendlyName())
	}

	traceID := evalString(t, h, "ctx.trace_id")
	if traceID.Type() != cty.String || !traceID.IsKnown() {
		t.Fatalf("ctx.trace_id = %#v, want a known string", traceID)
	}
}

// TestHostReserved checks the reserved-name policy: ctx, built-in namespaces, and
// ambient values are rejected; a free name is allowed.
func TestHostReserved(t *testing.T) {
	h := newTestHost(t, `bus "main" {}`)

	for _, name := range []string{"ctx", "bus", "env"} {
		if !h.Reserved(name) {
			t.Errorf("Reserved(%q) = false, want true", name)
		}
	}
	if h.Reserved("myvar") {
		t.Error(`Reserved("myvar") = true, want false`)
	}
}

// TestHostCompletionContext checks the completion context is cheap to build and
// includes both ctx and the bus namespace.
func TestHostCompletionContext(t *testing.T) {
	h := newTestHost(t, `bus "main" {}`)

	ctx, err := h.CompletionContext(context.Background())
	if err != nil {
		t.Fatalf("CompletionContext: %v", err)
	}
	// ctx is layered in the child; bus lives in the base parent. Both must be
	// reachable through the chain (which is how the engine gathers candidates).
	if !inChain(ctx, "ctx") {
		t.Error("completion context missing ctx")
	}
	if !inChain(ctx, "bus") {
		t.Error("completion context missing bus namespace")
	}
}

// inChain reports whether name is a variable anywhere in the eval-context chain.
func inChain(ctx *hcl.EvalContext, name string) bool {
	for c := ctx; c != nil; c = c.Parent() {
		if _, ok := c.Variables[name]; ok {
			return true
		}
	}
	return false
}

// TestLogLevelControls exercises the :loglevel / :quiet / :logs meta-commands via
// the host handlers, checking the atomic level transitions (mute raises above
// Fatal; unmute restores the pre-mute level).
func TestLogLevelControls(t *testing.T) {
	h := newTestHost(t, `bus "main" {}`) // starts at InfoLevel

	h.cmdQuiet(nil, nil)
	if h.logging.level.Level() <= zapcore.FatalLevel {
		t.Fatalf(":quiet did not mute (level=%v)", h.logging.level.Level())
	}

	h.cmdLogs([]string{"on"}, nil)
	if h.logging.level.Level() != zapcore.InfoLevel {
		t.Fatalf(":logs on did not restore Info (level=%v)", h.logging.level.Level())
	}

	h.cmdLoglevel([]string{"debug"}, nil)
	if h.logging.level.Level() != zapcore.DebugLevel {
		t.Fatalf(":loglevel debug failed (level=%v)", h.logging.level.Level())
	}

	h.cmdLogs([]string{"off"}, nil)
	if h.logging.level.Level() <= zapcore.FatalLevel {
		t.Fatalf(":logs off did not mute (level=%v)", h.logging.level.Level())
	}
	h.cmdLogs([]string{"on"}, nil)
	if h.logging.level.Level() != zapcore.DebugLevel {
		t.Fatalf(":logs on did not restore the pre-mute Debug level (level=%v)", h.logging.level.Level())
	}
}
