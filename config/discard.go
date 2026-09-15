package config

import (
	"context"

	"github.com/hashicorp/hcl/v2"
	"go.uber.org/zap"
)

// Discard releases a config that was built and never started: the teardown
// `vinculum check` runs, and the one Build runs on itself when it fails after
// blocks have begun to be processed.
//
// Building starts nothing and binds no port, but it is not free of resources: a
// bus starts its dispatch goroutine while it is processed, and a client may
// hold a pool. A process that exits straight after building never notices. One
// that builds many configs and keeps running — a checker answering requests —
// leaks one set per build without this.
//
// The phases are serve's, less the two that only mean anything once work has
// flowed: Drainables, then Stoppables, then buses, each in reverse
// registration order. PreStoppables are skipped because they carry the user's
// `trigger "shutdown"` actions, which must not run for a config that never
// started. Draining is bounded by DefaultShutdownTimeout, as it is for serve.
//
// Errors are logged rather than returned: there is nothing a caller discarding a
// config can do about one, and every phase should run whatever an earlier one
// reported.
func (c *Config) Discard() {
	logger := c.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	ctx, cancel := context.WithTimeout(context.Background(), DefaultShutdownTimeout)
	defer cancel()
	for i := len(c.Drainables) - 1; i >= 0; i-- {
		if err := c.Drainables[i].Drain(ctx); err != nil {
			logger.Warn("Component did not drain cleanly", zap.Error(err))
		}
	}

	for i := len(c.Stoppables) - 1; i >= 0; i-- {
		if err := c.Stoppables[i].Stop(); err != nil {
			logger.Warn("Component did not stop cleanly", zap.Error(err))
		}
	}

	for name, b := range c.Buses {
		if err := b.Stop(); err != nil {
			logger.Warn("Bus did not stop cleanly", zap.String("bus", name), zap.Error(err))
		}
	}
}

// discardFailed is Build's return for a failure after blocks have been
// processed: it releases what they built, then returns no Config.
func (c *Config) discardFailed(diags hcl.Diagnostics) (*Config, hcl.Diagnostics) {
	c.Discard()
	return nil, diags
}
