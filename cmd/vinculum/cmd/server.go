package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tsarna/vinculum/pkg/vinculum/config"
	"github.com/tsarna/vinculum/pkg/vinculum/subutils"
	"go.uber.org/zap"
)

// serverCmd represents the server command
var serverCmd = &cobra.Command{
	Use:   "server [config-files-or-directories...]",
	Short: "Start the vinculum server",
	Long: `Start the vinculum server with the specified configuration files or directories.

The server will load HCL configuration files from the specified paths and start
the event bus and other configured services.

Examples:
  vinculum server config.vcl
  vinculum server ./configs/
  vinculum server config1.vcl config2.vcl ./more-configs/`,
	Args: cobra.MinimumNArgs(1),
	RunE: runServer,
}

var (
	logLevel string
)

func init() {
	rootCmd.AddCommand(serverCmd)

	serverCmd.Flags().StringVarP(&logLevel, "log-level", "l", "info", "log level (debug, info, warn, error)")
}

func runServer(cmd *cobra.Command, args []string) error {
	// Setup logger
	logger, err := setupLogger()
	if err != nil {
		return fmt.Errorf("failed to setup logger: %w", err)
	}
	defer logger.Sync()

	logger.Info("Starting vinculum server",
		zap.Strings("config-paths", args),
		zap.String("log-level", logLevel),
	)

	logger.Info("Server startup complete (placeholder implementation)")

	cfg, diags := config.NewConfig().
		WithLogger(logger).
		WithSources(stringSliceToAnySlice(args)...).
		Build()

	if diags.HasErrors() {
		logger.Error("Failed to build config", zap.Any("diags", diags))
		return diags
	}

	// TODO remove, just for debugging
	logSub := subutils.NewLoggingSubscriber(nil, cfg.Logger, zap.InfoLevel)
	cfg.Buses["main"].Subscribe(context.Background(), logSub, "#")

	for _, cron := range cfg.Crons {
		cron.Start()
	}

	// For now, just wait indefinitely
	select {}
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

	return config.Build()
}

// Helper to convert []string to []any
func stringSliceToAnySlice(strs []string) []any {
	anys := make([]any, len(strs))
	for i, s := range strs {
		anys[i] = s
	}
	return anys
}
