package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/version"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// serverCmd represents the server command
var serverCmd = &cobra.Command{
	Use:   "serve [config-files-or-directories...]",
	Short: "Start the vinculum server",
	Long: `Start the vinculum server with the specified configuration files or directories.

The server will load HCL configuration files from the specified paths and start
the event bus and other configured services.

Examples:
  vinculum server config.vcl
  vinculum server ./configs/
  vinculum server config1.vcl config2.vcl ./more-configs/
  vinculum server -f /path/to/files config.vcl`,
	Args: cobra.MinimumNArgs(1),
	RunE: runServer,
}

var (
	logLevel  string
	filePath  string
	writePath string
	allowKill bool
)

func init() {
	rootCmd.AddCommand(serverCmd)

	serverCmd.Flags().StringVarP(&logLevel, "log-level", "l", "info", "log level (debug, info, warn, error)")
	serverCmd.Flags().StringVarP(&filePath, "file-path", "f", "", "base directory for file functions (enables file, fileexists, fileset functions)")
	serverCmd.Flags().StringVarP(&writePath, "write-path", "w", "", "base directory for file write functions; must be under --file-path")
	serverCmd.Flags().BoolVar(&allowKill, "allow-kill", false, "enable the kill function (feature \"allowkill\")")
}

func runServer(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	// Setup logger
	logger, err := setupLogger()
	if err != nil {
		return fmt.Errorf("failed to setup logger: %w", err)
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
		WithSources(stringSliceToAnySlice(args)...)

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

	// Wait for SIGINT or SIGTERM, then stop all stoppable components.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

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
	return nil
}

func setupLogger() (*zap.Logger, error) {
	level := logLevel
	debugFlag := GetDebug()
	verboseFlag := GetVerbose()

	// Override log level based on flags
	if debugFlag {
		level = "debug"
	} else if verboseFlag && level == "info" {
		level = "debug"
	}

	var zapLevel zap.AtomicLevel
	switch strings.ToLower(level) {
	case "debug":
		zapLevel = zap.NewAtomicLevelAt(zap.DebugLevel)
	case "info":
		zapLevel = zap.NewAtomicLevelAt(zap.InfoLevel)
	case "warn", "warning":
		zapLevel = zap.NewAtomicLevelAt(zap.WarnLevel)
	case "error":
		zapLevel = zap.NewAtomicLevelAt(zap.ErrorLevel)
	default:
		zapLevel = zap.NewAtomicLevelAt(zap.InfoLevel)
	}

	config := zap.NewProductionConfig()
	config.Level = zapLevel
	config.Development = debugFlag

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
