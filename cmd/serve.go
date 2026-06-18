package cmd

import (
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
	logLevel    string
	filePath    string
	writePath   string
	allowKill   bool
	pluginPath  string
	interactive bool
)

func init() {
	rootCmd.AddCommand(serverCmd)

	serverCmd.Flags().StringVarP(&logLevel, "log-level", "l", "info", "log level (debug, info, warn, error)")
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
		logging = repl.NewInteractiveLogging(resolveLogLevel())
		logger = logging.Logger
	} else {
		var err error
		logger, err = setupLogger()
		if err != nil {
			return fmt.Errorf("failed to setup logger: %w", err)
		}
	}
	defer logger.Sync()

	logger.Info("Starting vinculum server",
		zap.String("version", version.String()),
		zap.Strings("config-paths", args),
		zap.String("log-level", logLevel),
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

	if diags.HasErrors() {
		return diags
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
// the REPL-exit path: PreStoppables then Stoppables, each in reverse
// registration order so dependents stop before their dependencies.
func shutdown(cfg *config.Config, logger *zap.Logger) {
	logger.Info("Shutting down")
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

// resolveLogLevel applies the -d/-v overrides to the --log-level flag and
// returns the effective zap level. Shared by the production and interactive
// logger builders.
func resolveLogLevel() zapcore.Level {
	level := logLevel
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

func setupLogger() (*zap.Logger, error) {
	config := zap.NewProductionConfig()
	config.Level = zap.NewAtomicLevelAt(resolveLogLevel())
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
