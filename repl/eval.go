package repl

import (
	"fmt"
	"os"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/term"
)

// evalExpr parses src as a single VCL expression, evaluates it, and applies the
// echo + result-binding rules (non-null → printed and numbered into _ / _N;
// top-level null → nothing; error → diagnostics, not numbered).
func (s *Session) evalExpr(src string) {
	val, ok := s.parseAndEval(src)
	if !ok {
		return
	}
	s.echoAndNumber(val)
}

// parseAndEval parses src as a single VCL expression and evaluates it against
// the live three-level eval context. It renders any diagnostics (parse, eval,
// or warnings) and returns ok=false if there were errors. Shared by bare
// expressions and by assignment / :set RHS evaluation.
func (s *Session) parseAndEval(src string) (cty.Value, bool) {
	s.inputCounter++
	filename := fmt.Sprintf("<repl:%d>", s.inputCounter)

	expr, diags := hclsyntax.ParseExpression([]byte(src), filename, hcl.Pos{Line: 1, Column: 1})
	// Record the source so diagnostics can render caret-underlined snippets.
	s.files[filename] = &hcl.File{Bytes: []byte(src)}
	if diags.HasErrors() {
		s.printDiags(diags)
		return cty.NilVal, false
	}

	val, evalDiags := s.evaluate(src, expr)
	if evalDiags.HasErrors() {
		s.printDiags(evalDiags)
		return cty.NilVal, false
	}
	// Surface non-error diagnostics (warnings) without blocking the result.
	if len(evalDiags) > 0 {
		s.printDiags(evalDiags)
	}
	return val, true
}

// evaluate builds the per-eval context chain (config.evalCtx → ctxChild →
// replChild) under a fresh trace span and evaluates expr. Mirrors the handler
// pattern in config/subs.go and triggers/cron.
func (s *Session) evaluate(src string, expr hcl.Expression) (cty.Value, hcl.Diagnostics) {
	goCtx, span := s.tracer.Start(s.baseCtx, "repl.eval",
		trace.WithAttributes(attribute.String("repl.expr", src)))
	defer span.End()

	ctxChild, err := hclutil.NewEvalContext(goCtx).BuildEvalContext(s.cfg.EvalCtx())
	if err != nil {
		return cty.NilVal, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Failed to build evaluation context",
			Detail:   err.Error(),
		}}
	}

	replChild := ctxChild.NewChild()
	replChild.Variables = s.bindings // includes "_" and the numbered "_N"

	return expr.Value(replChild)
}

// echoAndNumber applies the result-numbering + echo rules to a successful
// value: a non-null value is printed HCL/VCL-style and numbered into "_" and
// "_N"; a top-level null produces no output and does not advance the counter or
// rebind "_". (Named assignment bindings are recorded by the caller first; this
// only handles the shared echo/numbering.)
func (s *Session) echoAndNumber(val cty.Value) {
	if val.IsNull() {
		return
	}
	s.resultCounter++
	name := fmt.Sprintf("_%d", s.resultCounter)
	s.bindings[name] = val
	s.bindings["_"] = val
	fmt.Fprintln(s.out, formatValue(val))
}

// printDiags renders diagnostics with caret-underlined source snippets against
// the session's accumulated files, to stderr (kept separate from the stdout
// result stream so power users can redirect with 2>logfile).
func (s *Session) printDiags(diags hcl.Diagnostics) {
	width := 80
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		width = w
	}
	wr := hcl.NewDiagnosticTextWriter(s.errOut, s.files, uint(width), true)
	_ = wr.WriteDiagnostics(diags)
}
