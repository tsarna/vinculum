package cmd

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
	"github.com/tsarna/vinculum/version"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version and build information",
	Args:  cobra.NoArgs,
	RunE:  runVersion,
}

func init() {
	rootCmd.AddCommand(versionCmd)
}

func runVersion(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	fmt.Printf("vinculum %s\n", version.Version)
	if version.Commit != "" {
		suffix := ""
		if version.Modified {
			suffix = " (dirty)"
		}
		fmt.Printf("  commit:     %s%s\n", version.Commit, suffix)
	}
	if version.BuildTime != "" {
		fmt.Printf("  built:      %s\n", version.BuildTime)
	}
	fmt.Printf("  go:         %s\n", runtime.Version())
	fmt.Printf("  platform:   %s/%s\n", runtime.GOOS, runtime.GOARCH)
	return nil
}
