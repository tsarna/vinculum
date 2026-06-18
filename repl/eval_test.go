package repl

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tsarna/vinculum/config"

	_ "github.com/tsarna/vinculum/ambient"   // register env.* and other ambient providers
	_ "github.com/tsarna/vinculum/functions" // register stdlib + generic functions
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// newTestSession builds a real Config from inline VCL and returns a Session
// whose output streams are captured buffers (no terminal involved).
func newTestSession(t *testing.T, src string) (*Session, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	cfg, diags := config.NewConfig().
		WithSources([]byte(src)).
		WithLogger(zap.NewNop()).
		Build()
	if diags.HasErrors() {
		t.Fatalf("build config: %s", diags)
	}
	s := New(cfg, NewInteractiveLogging(zapcore.InfoLevel), nil)
	var out, errOut bytes.Buffer
	s.out = &out
	s.errOut = &errOut
	return s, &out, &errOut
}

// TestEvalNoConfig mirrors "serve -i" with no config files: a Config built from
// zero sources still exposes ambient values and built-in functions, so the REPL
// is useful for exploration in an empty environment.
func TestEvalNoConfig(t *testing.T) {
	cfg, diags := config.NewConfig().WithLogger(zap.NewNop()).Build()
	if diags.HasErrors() {
		t.Fatalf("build empty config: %s", diags)
	}
	s := New(cfg, NewInteractiveLogging(zapcore.InfoLevel), nil)
	var out, errOut bytes.Buffer
	s.out = &out
	s.errOut = &errOut

	s.evalExpr(`upper("hi")`)
	if got := out.String(); got != `"HI"`+"\n" {
		t.Fatalf(`upper("hi") => %q, want "HI"`, got)
	}
	if errOut.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", errOut.String())
	}
}

func TestEvalExpression(t *testing.T) {
	s, out, errOut := newTestSession(t, `bus "main" {}`)

	s.evalExpr("1 + 1")
	if got := out.String(); got != "2\n" {
		t.Fatalf("1 + 1 => %q, want %q", got, "2\n")
	}
	if errOut.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", errOut.String())
	}
	if s.resultCounter != 1 {
		t.Fatalf("resultCounter = %d, want 1", s.resultCounter)
	}

	// "_" and "_1" carry the prior result.
	out.Reset()
	s.evalExpr("_ + 1")
	if got := out.String(); got != "3\n" {
		t.Fatalf("_ + 1 => %q, want %q", got, "3\n")
	}
	out.Reset()
	s.evalExpr("_1")
	if got := out.String(); got != "2\n" {
		t.Fatalf("_1 => %q, want %q", got, "2\n")
	}
	if s.resultCounter != 3 {
		t.Fatalf("resultCounter = %d, want 3", s.resultCounter)
	}
}

func TestEvalFunctionsAndContext(t *testing.T) {
	s, out, _ := newTestSession(t, `bus "main" {}`)

	// A registered function resolves up the eval-context chain.
	s.evalExpr(`length(["a", "b", "c"])`)
	if got := out.String(); got != "3\n" {
		t.Fatalf("length => %q, want %q", got, "3\n")
	}

	// ctx is available (ctx.trace_id is an empty string under the NOOP tracer).
	out.Reset()
	s.evalExpr("ctx.trace_id")
	if got := out.String(); got != `""`+"\n" {
		t.Fatalf("ctx.trace_id => %q, want empty string", got)
	}
}

func TestEvalNullSuppressed(t *testing.T) {
	s, out, errOut := newTestSession(t, `bus "main" {}`)

	s.evalExpr("null")
	if out.Len() != 0 {
		t.Fatalf("top-level null printed %q, want nothing", out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", errOut.String())
	}
	if s.resultCounter != 0 {
		t.Fatalf("resultCounter = %d, want 0 (null not numbered)", s.resultCounter)
	}
	if _, ok := s.bindings["_"]; ok {
		t.Fatalf("null result rebound _")
	}
}

func TestEvalErrorNotNumbered(t *testing.T) {
	s, out, errOut := newTestSession(t, `bus "main" {}`)

	// Seed a successful result first.
	s.evalExpr("42")
	out.Reset()

	s.evalExpr("1 +") // parse error
	if out.Len() != 0 {
		t.Fatalf("error produced stdout: %q", out.String())
	}
	if errOut.Len() == 0 {
		t.Fatal("expected diagnostics on stderr")
	}
	if s.resultCounter != 1 {
		t.Fatalf("resultCounter = %d, want 1 (error must not advance it)", s.resultCounter)
	}
	if got := s.bindings["_"].AsBigFloat().Text('f', -1); got != "42" {
		t.Fatalf("_ changed after error: %q, want 42", got)
	}
}

func TestEvalBusCapsule(t *testing.T) {
	s, out, _ := newTestSession(t, `bus "main" {}`)

	s.evalExpr("bus.main")
	if got := out.String(); !strings.HasPrefix(got, "eventbus(") {
		t.Fatalf("bus.main => %q, want eventbus(...) summary", got)
	}
}
