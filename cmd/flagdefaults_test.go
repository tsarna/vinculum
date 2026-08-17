package cmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestFlagDefaultsSurviveInit guards against two commands binding the same
// package-level variable to flags with different defaults.
//
// pflag writes a flag's default into the bound variable at registration time,
// so when two init() functions share a variable the one that runs last (file
// order within the package) silently overwrites the other command's default.
// That is invisible in --help, which prints the declared default, and only
// shows up at runtime: `vinculum serve` once started at "warn" because
// test.go's --log-level bound the same variable with a quieter default,
// suppressing every info-level log the server emits.
//
// The invariant is cheap to state: once every init() has run, what a flag
// reads back must be what it declared.
//
// The reading is taken in this file's init(), which the go command orders
// after every non-test init() in the package, so other tests in the package
// are free to assign the flag variables without disturbing it.
func TestFlagDefaultsSurviveInit(t *testing.T) {
	for _, m := range flagsAfterInit {
		if m.got != m.want {
			t.Errorf("%s --%s: bound variable is %q but the declared default is %q; "+
				"another command's flag is almost certainly bound to the same variable",
				m.command, m.flag, m.got, m.want)
		}
	}
}

type flagReading struct {
	command, flag, got, want string
}

var flagsAfterInit []flagReading

func init() {
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			flagsAfterInit = append(flagsAfterInit, flagReading{
				command: cmd.CommandPath(), flag: f.Name,
				got: f.Value.String(), want: f.DefValue,
			})
		})
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
}
