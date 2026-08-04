package main

import (
	"fmt"
	"os"

	"github.com/tsarna/vinculum/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		if !cmd.Reported(err) {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		os.Exit(cmd.ExitCode(err))
	}
}
