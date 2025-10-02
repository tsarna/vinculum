package cmd

import (
	"github.com/spf13/cobra"
)

var (
	verbose bool
	debug   bool
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "vinculum",
	Short: "Vinculum event server",
	Long: `Vinculum is a configuration-driven event bus server that provides
flexible event routing and processing capabilities.

It uses HCL (HashiCorp Configuration Language) for configuration
and supports various event bus patterns and integrations.`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	// Global flags
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")
	rootCmd.PersistentFlags().BoolVarP(&debug, "debug", "d", false, "debug output")
}

// GetVerbose returns the verbose flag value
func GetVerbose() bool {
	return verbose
}

// GetDebug returns the debug flag value
func GetDebug() bool {
	return debug
}
