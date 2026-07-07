// Package repl wires Vinculum's live configuration into the generic functy REPL
// engine (github.com/tsarna/functy/repl), reached via "vinculum serve -i". After
// normal server startup it presents a prompt at which the user types VCL
// expressions evaluated against the running configuration. The generic loop,
// result history, bindings, completion, and formatting live in the engine; this
// package supplies the Vinculum host adapter (see host.go: the per-eval `ctx`
// object, trace span, reserved-name policy, and log-control meta-commands) plus
// the interactive log sink (logsink.go). Exiting the REPL shuts the server down.
package repl

import (
	"io"
	"os"
	"strings"

	engine "github.com/tsarna/functy/repl"
	"github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/version"
)

// Session is the interactive REPL session; it is the generic engine session
// driven by the Vinculum host. Callers invoke Run to drive the loop.
type Session = engine.Session

// New constructs a REPL session over a live vinculum Config. The caller is
// responsible for having already performed normal server startup; New does not
// start or stop anything. Exiting Run should be followed by the shared shutdown.
func New(cfg *config.Config, logging *Logging, configPaths []string) *Session {
	h := newHost(cfg, logging)

	banner := "Vinculum " + version.String() + " — interactive REPL"
	if len(configPaths) > 0 {
		banner += "\nConfig: " + strings.Join(configPaths, ", ")
	}

	return engine.New(h, engine.Options{
		Banner:      banner,
		HistoryPath: engine.DefaultHistoryPath("vinculum"),
		Meta:        h.metaCommands(),
		// While the prompt is live, async runtime logs go through the editor's
		// redraw-aware writer so they erase and redraw the prompt cleanly;
		// teardown repoints them back to stderr before the editor closes.
		OnStart:  func(refresh io.Writer) { logging.sink.set(refresh) },
		OnDetach: func() { logging.sink.set(os.Stderr) },
	})
}
