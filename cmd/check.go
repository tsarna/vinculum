package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/tsarna/vinculum/config"
)

var checkCmd = &cobra.Command{
	Use:   "check [config-files-or-directories...]",
	Short: "Check that the configuration is valid",
	Long: `Check that the specified configuration files or directories are valid.

Loads and validates HCL configuration files without starting any services.
Exits with a non-zero status if the configuration has errors.

If any *.vinit bootstrap file under the configured paths declares a "plugin"
block, --plugin-path must be set so the plugins can be loaded for validation.
See doc/vinit.md and doc/plugins.md for details.

Examples:
  vinculum check config.vcl
  vinculum check ./configs/
  vinculum check config1.vcl config2.vcl ./more-configs/
  vinculum check --plugin-path /plugins ./configs/`,
	Args: cobra.MinimumNArgs(1),
	RunE: runCheck,
}

func init() {
	rootCmd.AddCommand(checkCmd)

	checkCmd.Flags().StringVarP(&logLevel, "log-level", "l", "info", "log level (debug, info, warn, error)")
	checkCmd.Flags().StringVarP(&filePath, "file-path", "f", "", "base directory for file functions (enables file, fileexists, fileset functions)")
	checkCmd.Flags().StringVarP(&writePath, "write-path", "w", "", "base directory for file write functions; must be under --file-path")
	checkCmd.Flags().StringVar(&pluginPath, "plugin-path", "", "directory containing Go plugin .so files; required if any .vinit plugin block is present")
}

func runCheck(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	logger, err := setupLogger()
	if err != nil {
		return fmt.Errorf("failed to setup logger: %w", err)
	}
	defer logger.Sync()

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

	cfg, diags := configBuilder.Build()

	if cfg != nil {
		for i := len(cfg.Stoppables) - 1; i >= 0; i-- {
			cfg.Stoppables[i].Stop() //nolint:errcheck
		}
		for _, b := range cfg.Buses {
			b.Stop() //nolint:errcheck
		}
	}

	reportWarnings(diags)

	if diags.HasErrors() {
		return diags
	}

	fmt.Println("Configuration is valid.")
	return nil
}
