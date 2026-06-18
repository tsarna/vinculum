package repl

import (
	"io"
	"os"
	"sync/atomic"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// swapSink is a zapcore.WriteSyncer whose destination can be swapped atomically
// at runtime. Interactive mode uses it to redirect async runtime logs without
// rebuilding the logger: to os.Stderr during startup/shutdown, and to the line
// editor's redraw-aware writer (rl.Stderr()) while the prompt is live, so
// concurrent log writes erase the prompt, print, and redraw it cleanly.
type swapSink struct {
	w atomic.Pointer[io.Writer]
}

func newSwapSink(initial io.Writer) *swapSink {
	s := &swapSink{}
	s.set(initial)
	return s
}

// set repoints the sink at a new destination writer.
func (s *swapSink) set(w io.Writer) {
	s.w.Store(&w)
}

func (s *swapSink) Write(p []byte) (int, error) {
	wp := s.w.Load()
	if wp == nil {
		return len(p), nil
	}
	return (*wp).Write(p)
}

func (s *swapSink) Sync() error {
	wp := s.w.Load()
	if wp == nil {
		return nil
	}
	if syncer, ok := (*wp).(interface{ Sync() error }); ok {
		return syncer.Sync()
	}
	return nil
}

// Logging bundles the interactive-mode logger with the handles the REPL needs
// to manage it: the swappable destination sink and the atomic level that the
// :loglevel / :quiet / :logs meta-commands manipulate.
type Logging struct {
	// Logger is the base logger handed to the ConfigBuilder via WithLogger.
	Logger *zap.Logger
	sink   *swapSink
	level  zap.AtomicLevel
}

// NewInteractiveLogging builds the logger used in interactive mode: a console
// encoder (human-readable + color) writing through zapcore.Lock(sink) at a
// runtime-adjustable level. The sink starts pointed at os.Stderr (before the
// line editor exists). Both Config.Logger and Config.UserLogger inherit this
// sink and level because they derive from this single base logger.
func NewInteractiveLogging(level zapcore.Level) *Logging {
	sink := newSwapSink(os.Stderr)
	atom := zap.NewAtomicLevelAt(level)

	encoderCfg := zap.NewDevelopmentEncoderConfig()
	encoderCfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
	encoder := zapcore.NewConsoleEncoder(encoderCfg)

	core := zapcore.NewCore(encoder, zapcore.Lock(sink), atom)
	logger := zap.New(core, zap.AddStacktrace(zapcore.ErrorLevel))

	return &Logging{Logger: logger, sink: sink, level: atom}
}
