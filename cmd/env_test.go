package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// newEnvTestTree builds a two-level command tree of its own rather than
// reaching for rootCmd, so a test can assert on scoped names, on whether RunE
// ran, and on flag values without disturbing the real commands other tests in
// this package share.
func newEnvTestTree() (root, sub *cobra.Command, ran *bool) {
	didRun := false

	root = &cobra.Command{Use: "vinculum", SilenceErrors: true}
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		return bindEnv(cmd)
	}
	root.PersistentFlags().Bool("verbose", false, "verbose output")

	sub = &cobra.Command{
		Use:  "widget",
		RunE: func(*cobra.Command, []string) error { didRun = true; return nil },
	}
	sub.Flags().String("format", "text", "output format")
	sub.Flags().Bool("pretty", true, "indent the output")
	sub.Flags().Duration("timeout", time.Minute, "wall-clock budget")
	sub.Flags().Int("retries", 3, "attempts before giving up")
	sub.Flags().StringArray("config", []string{"/declared/default"}, "config path")

	root.AddCommand(sub)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	return root, sub, &didRun
}

func runEnvTestTree(t *testing.T, args ...string) (*cobra.Command, bool, error) {
	t.Helper()

	root, sub, ran := newEnvTestTree()
	root.SetArgs(args)
	err := root.Execute()
	return sub, *ran, err
}

// TestBindEnvValueTypes covers each flag type the CLI uses today. The point is
// not that pflag can parse them — it is that the binding hands values to
// pflag's own Set rather than converting them itself, so a type added later
// works with no further code.
func TestBindEnvValueTypes(t *testing.T) {
	t.Setenv("VINCULUM_FORMAT", "json")
	t.Setenv("VINCULUM_PRETTY", "false")
	t.Setenv("VINCULUM_TIMEOUT", "30s")
	t.Setenv("VINCULUM_RETRIES", "7")
	t.Setenv("VINCULUM_CONFIG", "/etc/vinculum")

	sub, ran, err := runEnvTestTree(t, "widget")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !ran {
		t.Fatal("the command did not run")
	}

	for _, want := range []struct{ flag, value string }{
		{"format", "json"},
		{"pretty", "false"},
		{"timeout", "30s"},
		{"retries", "7"},
		{"config", "[/etc/vinculum]"},
	} {
		if got := sub.Flags().Lookup(want.flag).Value.String(); got != want.value {
			t.Errorf("--%s = %q, want %q", want.flag, got, want.value)
		}
	}
}

// TestBindEnvStringArrayReplacesDefault pins the pflag behaviour the "array
// flags take one element" rule depends on: the first Set on a StringArray
// replaces the declared default rather than appending to it. If that ever
// changed, an env-supplied path would silently accumulate on top of whatever
// the flag was declared with, and a variable meant to redirect a search would
// widen it instead.
//
// There is deliberately no separator convention. A StringArray's defining
// property is that its values are not split, so VINCULUM_CONFIG=/a:/b is one
// path containing a colon. A user who needs several passes them as flags.
func TestBindEnvStringArrayReplacesDefault(t *testing.T) {
	t.Setenv("VINCULUM_CONFIG", "/only/this")

	sub, _, err := runEnvTestTree(t, "widget")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got := sub.Flags().Lookup("config").Value.String(); got != "[/only/this]" {
		t.Errorf("--config = %q, want [/only/this]; an env value must replace the default, not append to it", got)
	}
}

// TestBindEnvPrecedence states the whole precedence rule in one place:
// explicit flag beats environment beats default, and within the environment
// the command-scoped name beats the bare one.
func TestBindEnvPrecedence(t *testing.T) {
	t.Run("env beats default", func(t *testing.T) {
		t.Setenv("VINCULUM_FORMAT", "json")

		sub, _, err := runEnvTestTree(t, "widget")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if got := sub.Flags().Lookup("format").Value.String(); got != "json" {
			t.Errorf("--format = %q, want json", got)
		}
	})

	t.Run("explicit flag beats env", func(t *testing.T) {
		t.Setenv("VINCULUM_FORMAT", "json")
		t.Setenv("VINCULUM_WIDGET_FORMAT", "json")

		sub, _, err := runEnvTestTree(t, "widget", "--format", "yaml")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if got := sub.Flags().Lookup("format").Value.String(); got != "yaml" {
			t.Errorf("--format = %q, want yaml; a flag the user typed is never overridden", got)
		}
	})

	t.Run("scoped beats bare", func(t *testing.T) {
		t.Setenv("VINCULUM_FORMAT", "bare")
		t.Setenv("VINCULUM_WIDGET_FORMAT", "scoped")

		sub, _, err := runEnvTestTree(t, "widget")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if got := sub.Flags().Lookup("format").Value.String(); got != "scoped" {
			t.Errorf("--format = %q, want scoped", got)
		}
	})

	t.Run("bare applies when scoped is absent", func(t *testing.T) {
		t.Setenv("VINCULUM_FORMAT", "bare")

		sub, _, err := runEnvTestTree(t, "widget")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if got := sub.Flags().Lookup("format").Value.String(); got != "bare" {
			t.Errorf("--format = %q, want bare", got)
		}
	})

	t.Run("an inherited persistent flag binds too", func(t *testing.T) {
		t.Setenv("VINCULUM_VERBOSE", "true")

		sub, _, err := runEnvTestTree(t, "widget")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if got := sub.Flags().Lookup("verbose").Value.String(); got != "true" {
			t.Errorf("--verbose = %q, want true; cmd.Flags() is local plus inherited", got)
		}
	})
}

// TestBindEnvSetButEmpty is the image-default-off path: a variable that is set
// but empty is applied as an empty value, which is the only way to turn off a
// default an image baked in. Treating it as unset would leave `docker run -e
// VINCULUM_FILE_PATH= ...` no way to say what it plainly means.
func TestBindEnvSetButEmpty(t *testing.T) {
	t.Setenv("VINCULUM_FORMAT", "")

	sub, _, err := runEnvTestTree(t, "widget")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	format := sub.Flags().Lookup("format")
	if got := format.Value.String(); got != "" {
		t.Errorf("--format = %q, want the empty string", got)
	}
	if !format.Changed {
		t.Error("--format should read as changed; an empty value that was set is still a value the run chose")
	}
}

// TestBindEnvRejectsBadValues covers the three things a rejected value must
// do: name the variable and its value, report every bad one rather than the
// first, and stop the command from running at all.
func TestBindEnvRejectsBadValues(t *testing.T) {
	t.Setenv("VINCULUM_TIMEOUT", "30 seconds")
	t.Setenv("VINCULUM_RETRIES", "several")

	_, ran, err := runEnvTestTree(t, "widget")
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	if ran {
		t.Error("the command ran; a process must not do any work under settings it could not parse")
	}

	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) {
		t.Fatalf("want an *ExitCodeError, got %T", err)
	}
	if exitErr.Code != 2 {
		t.Errorf("exit code = %d, want 2", exitErr.Code)
	}

	for _, want := range []string{
		`VINCULUM_TIMEOUT="30 seconds"`,
		`VINCULUM_RETRIES="several"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s:\n%s", want, err)
		}
	}
}

// TestBindEnvBadValueLeavesTheFlagUnchanged states the invariant that makes
// rejection safe: a value pflag would not parse must not leave the flag
// marked as changed, since anything asking "did the user set this?" would
// then act on a value that never took.
//
// The value itself is a different matter and is not asserted here, because
// pflag does not leave it alone: intValue.Set and its siblings assign the
// zero from a failed strconv before returning the error, so the flag reads
// back 0 rather than its declared default. Nothing consumes it — a rejected
// variable abandons the run before RunE, which
// TestBindEnvRejectsBadValues pins — but it is the first thing a reader of
// this code will wonder about.
func TestBindEnvBadValueLeavesTheFlagUnchanged(t *testing.T) {
	t.Setenv("VINCULUM_RETRIES", "several")

	sub, _, err := runEnvTestTree(t, "widget")
	if err == nil {
		t.Fatal("want an error, got nil")
	}

	if sub.Flags().Lookup("retries").Changed {
		t.Error("--retries reads as changed after a value pflag rejected")
	}
}

// TestBindEnvIgnoresHelp keeps cobra's own flag out of the scheme. Binding it
// would let a stray variable turn every command into a help dump.
func TestBindEnvIgnoresHelp(t *testing.T) {
	t.Setenv("VINCULUM_HELP", "true")

	_, ran, err := runEnvTestTree(t, "widget")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !ran {
		t.Error("the command did not run; VINCULUM_HELP must not be bound")
	}
}

func TestEnvNamesFor(t *testing.T) {
	root, sub, _ := newEnvTestTree()

	t.Run("a subcommand flag has a scoped and a bare name", func(t *testing.T) {
		got := envNamesFor(sub, "log-level")
		want := []string{"VINCULUM_WIDGET_LOG_LEVEL", "VINCULUM_LOG_LEVEL"}
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("envNamesFor = %v, want %v", got, want)
		}
	})

	t.Run("a root flag has one name, not the same name twice", func(t *testing.T) {
		got := envNamesFor(root, "verbose")
		if len(got) != 1 || got[0] != "VINCULUM_VERBOSE" {
			t.Errorf("envNamesFor = %v, want [VINCULUM_VERBOSE]", got)
		}
	})

	t.Run("a nested subcommand joins the whole path", func(t *testing.T) {
		leaf := &cobra.Command{Use: "gadget"}
		sub.AddCommand(leaf)

		got := envNamesFor(leaf, "format")
		want := []string{"VINCULUM_WIDGET_GADGET_FORMAT", "VINCULUM_FORMAT"}
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("envNamesFor = %v, want %v", got, want)
		}
	})
}

// TestEnvBindingSurvivesCommandHooks is the guard whose absence would be found
// in production rather than in review. Cobra runs only the *nearest*
// PersistentPreRunE in the chain, so a subcommand that grows a hook of its own
// would silently stop reading the environment — for that command alone, on
// whichever machine had the variable set, with nothing to indicate why.
//
// EnableTraverseRunHooks is what prevents it, so the test exercises the
// arrangement rather than reading the flag back: a subcommand with its own
// hook must still see an env-supplied value.
func TestEnvBindingSurvivesCommandHooks(t *testing.T) {
	t.Setenv("VINCULUM_FORMAT", "json")

	root, sub, ran := newEnvTestTree()
	ownHookRan := false
	sub.PersistentPreRunE = func(*cobra.Command, []string) error {
		ownHookRan = true
		return nil
	}

	root.SetArgs([]string{"widget"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !ownHookRan {
		t.Fatal("the subcommand's own hook did not run; the test is not arranged as intended")
	}
	if !*ran {
		t.Fatal("the command did not run")
	}
	if got := sub.Flags().Lookup("format").Value.String(); got != "json" {
		t.Errorf("--format = %q, want json; a subcommand's own PersistentPreRunE shadowed the "+
			"root's environment binding — cobra.EnableTraverseRunHooks must be set", got)
	}
}

// TestNoCommandShadowsTheEnvHook is the cheaper half of the same guard, and
// says what to do rather than only that something broke. It stands even if the
// traverse setting is later reconsidered.
func TestNoCommandShadowsTheEnvHook(t *testing.T) {
	if cobra.EnableTraverseRunHooks {
		return
	}

	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd != rootCmd && (cmd.PersistentPreRun != nil || cmd.PersistentPreRunE != nil) {
			t.Errorf("%s defines its own PersistentPreRun hook, which shadows the root's "+
				"environment binding; set cobra.EnableTraverseRunHooks or call bindEnv from it",
				cmd.CommandPath())
		}
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
}

// TestAnnotateEnvUsage checks the discoverability half. A feature that changes
// behaviour and cannot be found in --help is worse than no feature.
func TestAnnotateEnvUsage(t *testing.T) {
	root, sub, _ := newEnvTestTree()
	annotateEnvUsage(root)

	if got := sub.Flags().Lookup("format").Usage; !strings.Contains(got, "[env: VINCULUM_FORMAT]") {
		t.Errorf("--format usage = %q, want it to name VINCULUM_FORMAT", got)
	}
	if got := root.PersistentFlags().Lookup("verbose").Usage; !strings.Contains(got, "[env: VINCULUM_VERBOSE]") {
		t.Errorf("--verbose usage = %q, want it to name VINCULUM_VERBOSE", got)
	}

	t.Run("is idempotent", func(t *testing.T) {
		annotateEnvUsage(root)
		annotateEnvUsage(root)

		got := sub.Flags().Lookup("format").Usage
		if n := strings.Count(got, envMarker); n != 1 {
			t.Errorf("--format usage names the variable %d times, want 1:\n%s", n, got)
		}
	})

	t.Run("skips help", func(t *testing.T) {
		root.InitDefaultHelpFlag()
		annotateEnvUsage(root)

		if got := root.Flags().Lookup("help").Usage; strings.Contains(got, envMarker) {
			t.Errorf("--help usage = %q, want no env annotation", got)
		}
	})
}

// TestRealCommandsAreAnnotated runs the annotation over the actual command
// tree, which is the thing users see. It also documents which flags carry the
// binding: every one of them, because a whitelist is exactly the mechanism
// that lets a flag added later be forgotten.
func TestRealCommandsAreAnnotated(t *testing.T) {
	annotateEnvUsage(rootCmd)

	var missing []string

	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			if f.Name == "help" {
				return
			}
			if !strings.Contains(f.Usage, envMarker+envPrefix+envName(f.Name)+"]") {
				missing = append(missing, cmd.CommandPath()+" --"+f.Name)
			}
		})
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)

	if len(missing) > 0 {
		t.Errorf("flags with no environment annotation in --help:\n  %s", strings.Join(missing, "\n  "))
	}
}
