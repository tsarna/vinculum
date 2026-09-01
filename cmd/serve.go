package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/repl"
	"github.com/tsarna/vinculum/version"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/term"
)

// serverCmd represents the server command
var serverCmd = &cobra.Command{
	Use:   "serve [config-files-or-directories...]",
	Short: "Start the vinculum server",
	Long: `Start the vinculum server with the specified configuration files or directories.

The server will load HCL configuration files from the specified paths and start
the event bus and other configured services.

If any *.vinit bootstrap file under the configured paths declares a "plugin"
block, --plugin-path must be set to a directory containing the corresponding
.so files. See doc/vinit.md and doc/plugins.md for details.

At least one config file or directory is required, except with --interactive,
which may be run with no config to explore ambient values and built-in
functions in an empty environment.

Examples:
  vinculum serve config.vcl
  vinculum serve ./configs/
  vinculum serve config1.vcl config2.vcl ./more-configs/
  vinculum serve -f /path/to/files config.vcl
  vinculum serve --plugin-path /plugins ./configs/
  vinculum serve -i`,
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 && !interactive {
			return fmt.Errorf("requires at least one config file or directory (or -i to start an interactive session with no config)")
		}
		return nil
	},
	RunE: runServer,
}

var (
	// Each command that offers --log-level owns its own variable: the flag
	// defaults differ (serve/check info, test warn) and pflag writes the
	// default into the bound variable at registration time, so a shared
	// variable would leave whichever init() ran last deciding the level for
	// all of them.
	serveLogLevel string
	filePath      string
	writePath     string
	allowKill     bool
	pluginPath    string
	interactive   bool
	healthListen  string
	healthVerbose bool
)

func init() {
	rootCmd.AddCommand(serverCmd)

	serverCmd.Flags().StringVarP(&serveLogLevel, "log-level", "l", "info", "log level (debug, info, warn, error)")
	serverCmd.Flags().StringVarP(&filePath, "file-path", "f", "", "base directory for file functions (enables file, fileexists, fileset functions)")
	serverCmd.Flags().StringVarP(&writePath, "write-path", "w", "", "base directory for file write functions; must be under --file-path")
	serverCmd.Flags().BoolVar(&allowKill, "allow-kill", false, "enable the kill function (feature \"allowkill\")")
	serverCmd.Flags().StringVar(&pluginPath, "plugin-path", "", "directory containing Go plugin .so files; required if any .vinit plugin block is present")
	serverCmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "after startup, present an interactive REPL instead of blocking on a signal (requires a terminal)")
	serverCmd.Flags().StringVar(&healthListen, "health-listen", "", "address to serve /readyz, /livez, and /healthz on; needs no config (e.g. \":8081\")")
	serverCmd.Flags().BoolVar(&healthVerbose, "health-verbose", false, "let --health-listen honor ?verbose, which names every component and quotes its failure")
}

func runServer(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true

	if interactive && (!term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd()))) {
		return fmt.Errorf("interactive mode (-i) requires a terminal on stdin and stdout")
	}

	// Interactive mode uses a console logger writing through a swappable sink so
	// async runtime logs can redraw around the live prompt; non-interactive mode
	// uses the JSON production logger.
	var logger *zap.Logger
	var logging *repl.Logging
	if interactive {
		logging = repl.NewInteractiveLogging(resolveLogLevel(serveLogLevel))
		logger = logging.Logger
	} else {
		var err error
		logger, err = setupLogger(serveLogLevel)
		if err != nil {
			return fmt.Errorf("failed to setup logger: %w", err)
		}
	}
	defer logger.Sync()

	logger.Info("Starting vinculum server",
		zap.String("version", version.String()),
		zap.Strings("config-paths", args),
		zap.String("log-level", serveLogLevel),
		zap.String("file-path", filePath),
	)

	configBuilder := config.NewConfig().
		WithLogger(logger).
		WithSources(stringSliceToAnySlice(args)...).
		WithPluginPath(pluginPath)

	if filePath != "" {
		configBuilder = configBuilder.WithFeature("readfiles", filePath)
	}
	if writePath != "" {
		configBuilder = configBuilder.WithFeature("writefiles", writePath)
	}
	if allowKill {
		configBuilder = configBuilder.WithFeature("allowkill", "true")
	}

	cfg, diags := configBuilder.Build()

	if err := reportBuildDiags(cmd.ErrOrStderr(), configBuilder, diags); err != nil {
		return err
	}

	// The health listener binds before anything starts, so a probe arriving
	// during boot gets an honest "503 starting" rather than a refused
	// connection — the difference between a startupProbe that works and one
	// that reports the pod unreachable. It is closed after shutdown() returns,
	// so draining answers 503 while in-flight work finishes. That lifecycle is
	// the inverse of every Startable's, which is why it is not one.
	healthSrv, err := startHealthListener(cfg, logger)
	if err != nil {
		return err
	}
	if healthSrv != nil {
		defer healthSrv.Close()
	}

	// A terminal failure tears down whatever did start rather than exiting on
	// top of it: half a process is what the Startable contract exists to
	// prevent, and a component that bound a port or opened a connection still
	// has to give it back.
	if err := startAll(cfg, logger); err != nil {
		shutdown(cfg, logger)
		return err
	}

	// Boot is complete. Until this point readiness is false with reason
	// "starting", so a startupProbe pointed at /readyz behaves correctly
	// without a separate endpoint.
	cfg.Health.SetBooted()

	if interactive {
		// Present the REPL on the foreground goroutine instead of blocking on a
		// signal. SIGINT is handled by the line editor (cancel current line);
		// SIGTERM and :quit/EOF return from Run().
		session := repl.New(cfg, logging, args)
		if err := session.Run(); err != nil {
			logger.Error("REPL error", zap.Error(err))
		}
		shutdown(cfg, logger)
		return nil
	}

	// Wait for SIGINT or SIGTERM, then stop all stoppable components.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	shutdown(cfg, logger)
	return nil
}

// shutdown runs the graceful teardown sequence shared by the signal path and
// the REPL-exit path: Drainables, then PreStoppables, then the message
// pipeline, then Stoppables. The three phases of components run in reverse
// registration order so dependents stop before their dependencies.
//
// Draining first closes the listeners and waits out the requests already in
// flight, so no handler can start running against a client or bus that a later
// phase has already stopped.
//
// Quiescing last, after the shutdown triggers have published whatever they
// publish and before any transport closes, is what makes an acknowledgement
// able to follow the work: the message is settled by whichever hop finishes it,
// and that hop needs the receiver's connection still open when it does.
func shutdown(cfg *config.Config, logger *zap.Logger) {
	logger.Info("Shutting down")

	// Readiness goes false before anything is torn down, so the endpoint
	// controller removes the pod while in-flight work finishes. This has to
	// precede drain(), not merely the Stoppables: draining is what closes the
	// listeners, and a probe arriving after that gets a connection refusal
	// instead of the honest 503 that distinguishes a graceful rollout from a
	// lossy one.
	cfg.Health.BeginDrain()

	drain(cfg, logger)
	for i := len(cfg.PreStoppables) - 1; i >= 0; i-- {
		if err := cfg.PreStoppables[i].PreStop(); err != nil {
			logger.Error("Failed to pre-stop component", zap.Error(err))
		}
	}
	quiesce(cfg, logger, config.DefaultShutdownTimeout)
	for i := len(cfg.Stoppables) - 1; i >= 0; i-- {
		if err := cfg.Stoppables[i].Stop(); err != nil {
			logger.Error("Failed to stop component", zap.Error(err))
		}
	}
}

// drain closes the listeners and waits for in-flight work, in reverse
// registration order. Because a server "http" block is processed after the
// server it mounts, that order closes the front door first and only then the
// connections held behind it.
//
// Each Drainable enforces its own configured grace period beneath this
// context, so the phase is bounded by the config rather than by a number
// chosen here.
func drain(cfg *config.Config, logger *zap.Logger) {
	ctx := context.Background()
	for i := len(cfg.Drainables) - 1; i >= 0; i-- {
		if err := cfg.Drainables[i].Drain(ctx); err != nil {
			logger.Warn("Component did not drain cleanly", zap.Error(err))
		}
	}
}

// quiesceInterval is how often the pipeline is resampled while it empties. It
// is also what a shutdown of an already-idle process costs, since quiesce
// takes two readings before believing the first.
const quiesceInterval = 25 * time.Millisecond

// quiesce waits for the message pipeline to empty, then closes the queues in
// it. Both halves matter: a message still on a bus channel or an async queue
// when the process exits is work that was accepted and never done, and on a
// path where nothing acknowledged it there is no broker to redeliver it.
//
// It waits rather than emptying hop by hop because there is no order that would
// work. Registration order is dependency order, and a `subscription` names its
// source while a client receiver names its destination — so the bus a
// subscription's queue reads from and the bus a receiver's queue writes to are
// both registered before the queue. Every holder still processing is draining
// itself; the only thing teardown was doing wrong is exiting first.
//
// budget bounds the phase rather than any one holder: what an operator is
// choosing is how long the process may take to exit. Whatever is still held
// when it expires is named in the log, which is the number they want after a
// rollout that produced duplicates.
func quiesce(cfg *config.Config, logger *zap.Logger, budget time.Duration) {
	if len(cfg.InFlight) == 0 {
		return
	}

	deadline := time.Now().Add(budget)

	// Two consecutive empty readings, because a depth of zero does not prove
	// quiescence: the last message may have been taken off the queue and still
	// be running, and for a subscriber with no queue of its own that work
	// happens on the bus's own dispatch goroutine. A second reading a moment
	// later is not a proof either, and does not have to be — what slips through
	// settles against a transport that has closed, which is a refused
	// acknowledgement and a redelivery rather than a lost message.
	for readings := 0; readings < 2; {
		if pending := pendingWork(cfg); pending == 0 {
			readings++
			if readings == 2 {
				break
			}
		} else {
			readings = 0
			if time.Now().After(deadline) {
				logger.Warn("Shutting down with messages still in flight",
					zap.Int("messages", pending),
					zap.Strings("holders", pendingHolders(cfg)),
				)
				break
			}
		}
		time.Sleep(quiesceInterval)
	}

	closeQueues(cfg, logger, time.Until(deadline))
}

// closeQueues closes every holder that has something to close, in reverse
// registration order.
//
// Closing is exact where sampling a depth is not: an async queue's Close waits
// for its goroutine to finish the message it is running. It also leaves the
// queue refusing what arrives next, so a late delivery from a receiver that has
// not been stopped yet is nacked and redelivered rather than accepted by a
// queue that will never run it.
//
// Which is also why it needs a bound of its own: the goroutine it waits for is
// running a user action, and an action that never returns must not be able to
// hang the process. budget is what is left of the phase deadline, floored at one
// sampling interval — closing a queue that has already emptied is a goroutine
// handoff rather than a wait, so the floor costs nothing and keeps the ordinary
// case tidy even when the wait above used the whole deadline up.
func closeQueues(cfg *config.Config, logger *zap.Logger, budget time.Duration) {
	if budget < quiesceInterval {
		budget = quiesceInterval
	}

	// The one being closed is published as it is reached, so the give-up line
	// can name it. A queue that will not finish has an action that will not
	// return, and which one that is cannot be worked out from a depth: by then
	// the message is off the queue.
	var closing atomic.Pointer[string]

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := len(cfg.InFlight) - 1; i >= 0; i-- {
			holder := cfg.InFlight[i]
			if holder.Close == nil {
				continue
			}
			closing.Store(&holder.Name)
			if err := holder.Close(); err != nil {
				logger.Warn("Queue did not close cleanly",
					zap.String("holder", holder.Name), zap.Error(err))
			}
		}
	}()

	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		fields := []zap.Field{zap.Duration("budget", budget)}
		if name := closing.Load(); name != nil {
			fields = append(fields, zap.String("holder", *name))
		}
		logger.Warn("Gave up waiting for a queue to finish its work", fields...)
	}
}

// pendingWork totals the messages the pipeline is still holding. A holder that
// reports no depth is skipped rather than dereferenced: registering one is a
// mistake, and teardown is the worst place in the process to answer a mistake
// with a panic, since everything after it would go unstopped.
func pendingWork(cfg *config.Config) int {
	total := 0
	for _, holder := range cfg.InFlight {
		if holder.QueueDepth != nil {
			total += holder.QueueDepth()
		}
	}
	return total
}

// pendingHolders names the holders that are still carrying something, for the
// log line that says a shutdown gave up waiting.
func pendingHolders(cfg *config.Config) []string {
	var names []string
	for _, holder := range cfg.InFlight {
		if holder.QueueDepth == nil {
			continue
		}
		if depth := holder.QueueDepth(); depth > 0 {
			names = append(names, fmt.Sprintf("%s=%d", holder.Name, depth))
		}
	}
	return names
}

// resolveLogLevel applies the -d/-v overrides to the given --log-level value
// and returns the effective zap level. Shared by the production and interactive
// logger builders; the caller passes its own command's flag variable.
func resolveLogLevel(level string) zapcore.Level {
	if GetDebug() {
		level = "debug"
	} else if GetVerbose() && level == "info" {
		level = "debug"
	}

	switch strings.ToLower(level) {
	case "debug":
		return zap.DebugLevel
	case "warn", "warning":
		return zap.WarnLevel
	case "error":
		return zap.ErrorLevel
	default:
		return zap.InfoLevel
	}
}

func setupLogger(level string) (*zap.Logger, error) {
	config := zap.NewProductionConfig()
	config.Level = zap.NewAtomicLevelAt(resolveLogLevel(level))
	config.Development = GetDebug()

	// Pin stacktrace to error level regardless of Development mode.
	// zap's default promotes stacktraces to warn when Development=true, which
	// produces noisy output for routine warnings and for user-caused errors
	// that surface via Logger.Warn.
	return config.Build(zap.AddStacktrace(zapcore.ErrorLevel))
}

// Helper to convert []string to []any
func stringSliceToAnySlice(strs []string) []any {
	anys := make([]any, len(strs))
	for i, s := range strs {
		anys[i] = s
	}
	return anys
}
