// Package repl implements Vinculum's interactive read-eval-print loop, reached
// via "vinculum serve -i". After normal server startup it presents a prompt at
// which the user types VCL expressions evaluated against the live, running
// configuration. Exiting the REPL shuts the server down.
package repl

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/chzyer/readline"
	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/version"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap/zapcore"
)

// primaryPrompt is the main prompt; it embeds the number the next successful,
// non-null result will be bound to (e.g. "9> " means a result becomes _9), so
// scrollback doubles as an index into the _N history.
func (s *Session) primaryPrompt() string {
	return fmt.Sprintf("%d> ", s.resultCounter+1)
}

// continuationPrompt is shown for multi-line input. It is dotted and padded to
// the same width as the current primary prompt so the input column stays aligned.
func (s *Session) continuationPrompt() string {
	p := s.primaryPrompt()
	return strings.Repeat(".", len(p)-1) + " "
}

// Session holds the state of a single interactive REPL run.
type Session struct {
	cfg     *config.Config
	logging *Logging
	rl      *readline.Instance

	// out receives results and the banner (stdout); errOut receives diagnostics
	// and meta-command errors (stderr). Set from the editor's redraw-aware
	// writers in Run; overridable in tests.
	out    io.Writer
	errOut io.Writer

	// configPaths are the config sources, shown in the startup banner.
	configPaths []string

	// baseCtx is tied to the server lifetime; cancelling it aborts an in-flight
	// eval (and is triggered on SIGTERM).
	baseCtx context.Context
	cancel  context.CancelFunc

	// detachOnce guards the repoint-then-close teardown so it runs exactly once,
	// whether triggered by normal loop exit or by the SIGTERM goroutine.
	detachOnce sync.Once

	// tracer opens a span per eval (NOOP when no client "otlp" is configured).
	tracer trace.Tracer

	// bindings holds session variables: named bindings plus the managed "_" and
	// "_N" results. Touched only by the foreground REPL goroutine.
	bindings map[string]cty.Value

	// files maps each synthetic "<repl:N>" input filename to its source, so
	// diagnostics can render caret-underlined snippets.
	files map[string]*hcl.File

	// inputCounter advances on every parsed input (the N in <repl:N>);
	// resultCounter advances only on successful, non-null evaluations (the N in _N).
	inputCounter  int
	resultCounter int

	// muted tracks whether async logs are currently muted (:quiet / :logs off);
	// savedLevel is the level to restore on :logs on.
	muted      bool
	savedLevel zapcore.Level
}

// New constructs a REPL session. The caller is responsible for having already
// performed normal server startup; New does not start or stop anything.
func New(cfg *config.Config, logging *Logging, configPaths []string) *Session {
	ctx, cancel := context.WithCancel(context.Background())

	// Open eval spans under the config's default OTLP tracer provider when one
	// is configured; otherwise the global NOOP provider (zero cost, empty IDs).
	var tp trace.TracerProvider
	if oc, _ := cfg.GetDefaultOtlpClient(); oc != nil {
		tp = oc.GetTracerProvider()
	}
	if tp == nil {
		tp = otel.GetTracerProvider()
	}

	return &Session{
		cfg:         cfg,
		logging:     logging,
		configPaths: configPaths,
		baseCtx:     ctx,
		cancel:      cancel,
		tracer:      tp.Tracer("vinculum/repl"),
		bindings:    make(map[string]cty.Value),
		files:       make(map[string]*hcl.File),
	}
}

// Run drives the read-eval-print loop on the calling (foreground) goroutine.
// It returns when the user exits (EOF / :quit) or SIGTERM is received. The
// caller should run the shared shutdown sequence afterward.
func (s *Session) Run() error {
	rl, err := readline.NewEx(&readline.Config{
		Prompt:          s.primaryPrompt(),
		InterruptPrompt: "^C",
		EOFPrompt:       "",
		Stderr:          os.Stderr,
		HistoryFile:     historyFilePath(),
		AutoComplete:    &completer{s: s},
	})
	if err != nil {
		return fmt.Errorf("failed to start line editor: %w", err)
	}
	s.rl = rl
	s.out = rl.Stdout()
	s.errOut = os.Stderr

	// Hand async logs off to the editor's redraw-aware writer while the prompt
	// is live. detach() restores os.Stderr and closes the editor on the way out.
	s.logging.sink.set(rl.Stderr())
	defer s.detach()

	// SIGTERM breaks the loop (graceful shutdown). SIGINT is NOT handled here:
	// readline puts the terminal in raw mode, so Ctrl-C arrives as ErrInterrupt
	// on the current line rather than as a process signal.
	sigterm := make(chan os.Signal, 1)
	signal.Notify(sigterm, syscall.SIGTERM)
	defer signal.Stop(sigterm)
	go func() {
		select {
		case <-sigterm:
			s.cancel()
			s.detach() // repoint logs, then close to unblock Readline()
		case <-s.baseCtx.Done():
		}
	}()

	s.printBanner()
	s.loop()
	return nil
}

// detach repoints async logs back to os.Stderr and then closes the editor, in
// that order, so a concurrent log write can never reach a closed editor writer.
// Safe to call multiple times (e.g. SIGTERM goroutine plus the deferred exit).
func (s *Session) detach() {
	s.detachOnce.Do(func() {
		s.logging.sink.set(os.Stderr)
		s.rl.Close()
	})
}

func (s *Session) printBanner() {
	fmt.Fprintf(s.out, "Vinculum %s — interactive REPL\n", version.String())
	if len(s.configPaths) > 0 {
		fmt.Fprintf(s.out, "Config: %s\n", strings.Join(s.configPaths, ", "))
	}
	fmt.Fprintln(s.out, "Type an expression to evaluate it, or :help for commands.")
}

func (s *Session) loop() {
	for {
		// Refresh the prompt each iteration so its result number tracks the
		// counter, which advances on every successful, non-null evaluation.
		s.rl.SetPrompt(s.primaryPrompt())

		line, err := s.rl.Readline()
		switch err {
		case readline.ErrInterrupt:
			// Ctrl-C: discard the current line and re-prompt.
			continue
		case io.EOF:
			// Ctrl-D on an empty line: exit.
			return
		case nil:
			// fall through
		default:
			// Editor closed (e.g. SIGTERM) or other read error: exit.
			return
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// A leading ':' marks a meta-command (':' starts no HCL expression).
		// Meta-commands are always single-line.
		if strings.HasPrefix(trimmed, ":") {
			if s.handleMeta(trimmed) {
				return
			}
			continue
		}

		// Expressions and assignments may span multiple lines: accumulate
		// continuation lines until the input parses (or yields a real error).
		buffer, ok := s.accumulate(line)
		if !ok {
			continue // discarded at the continuation prompt
		}
		s.dispatchInput(buffer)
	}
}
