package cmd

import (
	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

// startAll runs the boot sequence: every Startable, then every PostStartable,
// each in registration order. Shared by `serve` and `test` so the two cannot
// disagree about what a failed start means.
//
// It returns non-nil only for a terminal failure — one that retrying cannot fix
// (see config.TerminalError). The caller runs teardown and propagates the
// error, which carries its own exit code and is already reported.
//
// Everything else is logged at Warn and boot continues. That level is
// deliberate: under the Startable contract a plain error means the component is
// degraded but still trying, so its state belongs to readiness rather than to
// the boot log, and an Error line here would overstate a condition that heals
// itself. The failure is not lost — /readyz names the component and quotes the
// reason for as long as it lasts.
func startAll(cfg *config.Config, logger *zap.Logger) error {
	for _, startable := range cfg.Startables {
		if err := startable.Start(); err != nil {
			if terminal := terminalStart(logger, err); terminal != nil {
				return terminal
			}
			logger.Warn("Component started degraded", zap.Error(err))
		}
	}

	for _, ps := range cfg.PostStartables {
		if err := ps.PostStart(); err != nil {
			if terminal := terminalStart(logger, err); terminal != nil {
				return terminal
			}
			logger.Warn("Component post-start degraded", zap.Error(err))
		}
	}

	return nil
}

// terminalStart reports a terminal failure and returns the error to abort boot
// with, or nil if err is retriable.
//
// Reported is set because the line below is the explanation: the component
// named it, and a trailing "Error: ..." from main would restate the headline
// without adding the context this line carries.
func terminalStart(logger *zap.Logger, err error) error {
	if !config.IsTerminal(err) {
		return nil
	}
	logger.Error("Component cannot start; shutting down", zap.Error(err))
	return &ExitCodeError{Code: 1, Err: err, Reported: true}
}
