package repl

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/hashicorp/hcl/v2"
	engine "github.com/tsarna/functy/repl"
	"github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap/zapcore"
)

// host adapts a live vinculum Config to the generic functy REPL engine's Host
// interface. Each input is evaluated against config.EvalCtx() with the per-eval
// "ctx" object layered on (and a trace span opened around it); the reserved-name
// policy rejects "ctx" and every built-in top-level namespace. host also owns the
// log-control state driven by the :loglevel / :quiet / :logs meta-commands.
type host struct {
	cfg     *config.Config
	logging *Logging
	tracer  trace.Tracer

	// muted tracks whether async logs are currently muted (:quiet / :logs off);
	// savedLevel is the level to restore on :logs on.
	muted      bool
	savedLevel zapcore.Level
}

// newHost builds the adapter, resolving the trace provider from the config's
// default OTLP client (falling back to the global NOOP provider).
func newHost(cfg *config.Config, logging *Logging) *host {
	var tp trace.TracerProvider
	if oc, _ := cfg.GetDefaultOtlpClient(); oc != nil {
		tp = oc.GetTracerProvider()
	}
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	return &host{cfg: cfg, logging: logging, tracer: tp.Tracer("vinculum/repl")}
}

// EvalContext opens a "repl.eval" span and builds the per-eval context chain
// (config.EvalCtx() → ctx child). The returned finish ends the span. Mirrors the
// handler pattern in config/subs.go and triggers/cron.
func (h *host) EvalContext(ctx context.Context, src string) (*hcl.EvalContext, func(), error) {
	goCtx, span := h.tracer.Start(ctx, "repl.eval",
		trace.WithAttributes(attribute.String("repl.expr", src)))
	child, err := hclutil.NewEvalContext(goCtx).BuildEvalContext(h.cfg.EvalCtx())
	if err != nil {
		span.End()
		return nil, nil, err
	}
	return child, func() { span.End() }, nil
}

// CompletionContext builds the same per-eval child without a span (it is called
// per keystroke), so tab-completion sees "ctx" and every config namespace and
// function.
func (h *host) CompletionContext(ctx context.Context) (*hcl.EvalContext, error) {
	return hclutil.NewEvalContext(ctx).BuildEvalContext(h.cfg.EvalCtx())
}

// Reserved rejects ctx and any built-in top-level namespace (bus, server,
// client, env, http_status, …) as a session-binding name.
func (h *host) Reserved(name string) bool {
	if name == "ctx" {
		return true
	}
	_, ok := h.cfg.EvalCtx().Variables[name]
	return ok
}

// metaCommands are the vinculum-specific log-control meta-commands layered onto
// the engine's built-in command set.
func (h *host) metaCommands() []engine.MetaCommand {
	return []engine.MetaCommand{
		{Names: []string{":loglevel"}, Summary: "set async log level (debug|info|warn|error)", Run: h.cmdLoglevel},
		{Names: []string{":quiet"}, Summary: "mute async logs", Run: h.cmdQuiet},
		{Names: []string{":logs"}, Summary: "unmute / mute async logs (:logs on|off)", Run: h.cmdLogs},
	}
}

func (h *host) cmdLoglevel(args []string, errOut io.Writer) bool {
	if len(args) != 1 {
		fmt.Fprintln(errOut, "usage: :loglevel debug|info|warn|error")
		return false
	}
	lvl, ok := parseLogLevel(args[0])
	if !ok {
		fmt.Fprintf(errOut, "unknown log level: %s\n", args[0])
		return false
	}
	h.muted = false
	h.logging.level.SetLevel(lvl)
	return false
}

func (h *host) cmdQuiet([]string, io.Writer) bool {
	h.muteLogs()
	return false
}

func (h *host) cmdLogs(args []string, errOut io.Writer) bool {
	if len(args) != 1 {
		fmt.Fprintln(errOut, "usage: :logs on|off")
		return false
	}
	switch args[0] {
	case "on":
		h.unmuteLogs()
	case "off":
		h.muteLogs()
	default:
		fmt.Fprintln(errOut, "usage: :logs on|off")
	}
	return false
}

// muteLogs raises the level above everything emitted (idiomatic zap muting),
// remembering the prior level so unmuteLogs can restore it.
func (h *host) muteLogs() {
	if !h.muted {
		h.savedLevel = h.logging.level.Level()
		h.muted = true
	}
	h.logging.level.SetLevel(zapcore.FatalLevel + 1)
}

func (h *host) unmuteLogs() {
	if h.muted {
		h.logging.level.SetLevel(h.savedLevel)
		h.muted = false
	}
}

func parseLogLevel(s string) (zapcore.Level, bool) {
	switch strings.ToLower(s) {
	case "debug":
		return zapcore.DebugLevel, true
	case "info":
		return zapcore.InfoLevel, true
	case "warn", "warning":
		return zapcore.WarnLevel, true
	case "error":
		return zapcore.ErrorLevel, true
	default:
		return 0, false
	}
}
