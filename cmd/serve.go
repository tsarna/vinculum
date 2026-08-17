package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

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
)

func init() {
	rootCmd.AddCommand(serverCmd)

	serverCmd.Flags().StringVarP(&serveLogLevel, "log-level", "l", "info", "log level (debug, info, warn, error)")
	serverCmd.Flags().StringVarP(&filePath, "file-path", "f", "", "base directory for file functions (enables file, fileexists, fileset functions)")
	serverCmd.Flags().StringVarP(&writePath, "write-path", "w", "", "base directory for file write functions; must be under --file-path")
	serverCmd.Flags().BoolVar(&allowKill, "allow-kill", false, "enable the kill function (feature \"allowkill\")")
	serverCmd.Flags().StringVar(&pluginPath, "plugin-path", "", "directory containing Go plugin .so files; required if any .vinit plugin block is present")
	serverCmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "after startup, present an interactive REPL instead of blocking on a signal (requires a terminal)")
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

	for _, startable := range cfg.Startables {
		err := startable.Start()
		if err != nil {
			logger.Error("Failed to start component", zap.Error(err))
		}
	}

	for _, ps := range cfg.PostStartables {
		if err := ps.PostStart(); err != nil {
			logger.Error("Failed to post-start component", zap.Error(err))
		}
	}

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
// the REPL-exit path: Drainables, then PreStoppables, then Stoppables, each in
// reverse registration order so dependents stop before their dependencies.
//
// Draining first closes the listeners and waits out the requests already in
// flight, so no handler can start running against a client or bus that a later
// phase has already stopped.
func shutdown(cfg *config.Config, logger *zap.Logger) {
	logger.Info("Shutting down")
	drain(cfg, logger)
	for i := len(cfg.PreStoppables) - 1; i >= 0; i-- {
		if err := cfg.PreStoppables[i].PreStop(); err != nil {
			logger.Error("Failed to pre-stop component", zap.Error(err))
		}
	}
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
