package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Every flag is also settable from the environment, so a containerised
// Vinculum can be configured without rewriting its command line. Docker
// replaces the entire CMD as soon as a user supplies arguments of their own,
// which would otherwise discard the flags the image needs to keep — file
// functions and the plugin directory among them.
//
// Two names reach each flag. The command-scoped name (VINCULUM_SCHEMA_FORMAT)
// wins over the bare one (VINCULUM_FORMAT), and an explicit flag wins over
// both. The bare form is what makes one variable configure --plugin-path for
// serve, check, fmt, man, and schema alike; the scoped form exists because
// --format does not mean the same thing on check as it does on schema, and a
// single flat namespace would make one shell's convenience another command's
// failure.
const (
	envPrefix = "VINCULUM_"

	// envMarker introduces the usage-string annotation, and is what makes
	// annotateEnvUsage idempotent.
	envMarker = "[env: "
)

func init() {
	// Cobra runs only the nearest PersistentPreRunE in the chain, so a
	// subcommand that grows one of its own would otherwise silently lose
	// environment binding. TestEnvBindingSurvivesCommandHooks states the
	// invariant either way.
	cobra.EnableTraverseRunHooks = true

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		return bindEnv(cmd)
	}
}

// envName converts a flag or command name to its environment-variable
// spelling.
func envName(s string) string {
	return strings.ToUpper(strings.ReplaceAll(s, "-", "_"))
}

// envNamesFor returns the variable names that can supply the named flag on
// cmd, most specific first. A flag on the root command has no scope of its
// own, so it yields one name rather than the same name twice.
func envNamesFor(cmd *cobra.Command, flagName string) []string {
	scope := ""
	for c := cmd; c != nil && c.Parent() != nil; c = c.Parent() {
		scope = envName(c.Name()) + "_" + scope
	}

	bare := envPrefix + envName(flagName)
	if scope == "" {
		return []string{bare}
	}
	return []string{envPrefix + scope + envName(flagName), bare}
}

// bindEnv applies VINCULUM_* variables to the flags of the command about to
// run. It is the root's PersistentPreRunE, which cobra invokes after flag
// parsing, so cmd.Flags() is the complete set — local plus every inherited
// persistent flag — and one implementation covers commands added later.
//
// A value pflag rejects is a startup error rather than a fall back to the
// default: a variable that was set was meant, and silently ignoring it would
// leave the process running under settings nobody chose. Every rejected
// variable is reported, not just the first, so one run fixes them all.
func bindEnv(cmd *cobra.Command) error {
	var problems []string

	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		// An explicit flag always wins, and --help is cobra's own.
		if f.Changed || f.Name == "help" {
			return
		}

		for _, name := range envNamesFor(cmd, f.Name) {
			// LookupEnv rather than Getenv: a variable that is set but empty
			// is applied as an empty value, which is how an operator turns off
			// a default baked into an image.
			value, ok := os.LookupEnv(name)
			if !ok {
				continue
			}

			if err := f.Value.Set(value); err != nil {
				// Leave Changed alone — the value did not take, and the run is
				// about to be abandoned anyway.
				problems = append(problems, fmt.Sprintf("%s=%q: %v", name, value, err))
				return
			}

			// Marking it changed makes the value indistinguishable from one
			// the user typed, which is the right answer for anything that
			// later asks whether the flag was set.
			f.Changed = true

			// The scoped name wins outright: precedence within the environment
			// has to be total, so the bare name is not also applied.
			return
		}
	})

	if len(problems) > 0 {
		// A rejected value is a mistake in the environment, not in how the
		// command was invoked, so the flag list would bury the one line that
		// says which variable to fix. Code 2 is what every other bad-value
		// path in cmd/ returns.
		cmd.SilenceUsage = true

		label := "invalid environment variable"
		if len(problems) > 1 {
			label = "invalid environment variables"
		}
		return &ExitCodeError{
			Code: 2,
			Err:  fmt.Errorf("%s:\n  %s", label, strings.Join(problems, "\n  ")),
		}
	}
	return nil
}

// annotateEnvUsage appends each flag's bare environment name to its usage
// string, so `--help` advertises the binding. Environment-driven behaviour is
// not risky because it is powerful but because it is invisible, and a flag
// nobody can discover from --help is exactly that.
//
// It runs from Execute rather than an init(), because a package's init()
// functions run in file-name order and most flags are registered by an init()
// that has not run yet when this file's does. The scoped form is documented as
// a rule in doc/cli-env.md instead of repeated here, since spelling it out
// would roughly double each annotation to express a form most users never
// need.
func annotateEnvUsage(cmd *cobra.Command) {
	annotate := func(flags *pflag.FlagSet) {
		flags.VisitAll(func(f *pflag.Flag) {
			if f.Name == "help" || strings.Contains(f.Usage, envMarker) {
				return
			}
			f.Usage = fmt.Sprintf("%s %s%s%s]", f.Usage, envMarker, envPrefix, envName(f.Name))
		})
	}

	// Each flag is declared on exactly one command, in exactly one of these
	// two sets, and cobra has not yet merged parents' persistent flags into
	// the children — so this visits every flag once.
	annotate(cmd.Flags())
	annotate(cmd.PersistentFlags())

	for _, sub := range cmd.Commands() {
		annotateEnvUsage(sub)
	}
}
