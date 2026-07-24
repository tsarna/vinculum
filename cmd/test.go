package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/spf13/cobra"
	"github.com/tsarna/functy"
	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

var (
	testRun        string
	testVerbose    bool
	testJSON       bool
	testTimeout    time.Duration
	testNoServe    bool
	testFailNoTest bool
)

var testCmd = &cobra.Command{
	Use:   "test [config-files-or-directories...]",
	Short: "Run the test blocks in a configuration's .cty files",
	Long: `Run the functy test "..." { ... } blocks embedded in a configuration's .cty
files against the running system.

By default the full server is started exactly as "vinculum serve" would — buses,
servers, subscriptions, triggers — the tests are run, and the server is shut down.
This lets tests reference bus.*/client.*/server.*, send() messages, and assert on
the resulting state. The "sys.testing" ambient is true during a test run, so a
configuration can gate real external connections off with "disabled = sys.testing".

A test passes if its body runs to completion, is skipped if it calls skip(...),
and fails if any other error (a failed assert, a throw, an evaluation error)
unwinds out of it. Two test-only polling assertions bridge async effects:
eventually(cond, timeout) and never(cond, timeout). Exits non-zero if any test
fails.

Use --no-serve to skip starting the runtime (build + run tests only), for pure
function unit tests that do not need a live bus.

Examples:
  vinculum test config.vcl tests.cty
  vinculum test ./configs/
  vinculum test --run 'router' -v ./configs/
  vinculum test --no-serve funcs.cty tests.cty`,
	Args: cobra.MinimumNArgs(1),
	RunE: runTest,
}

func init() {
	rootCmd.AddCommand(testCmd)

	testCmd.Flags().StringVar(&testRun, "run", "", "run only tests whose description matches this regular expression")
	testCmd.Flags().BoolVarP(&testVerbose, "verbose", "v", false, "list every test (ok/SKIP/FAIL), not just failures")
	testCmd.Flags().BoolVar(&testJSON, "json", false, "emit a machine-readable JSON report to stderr instead of human-readable output")
	testCmd.Flags().DurationVar(&testTimeout, "timeout", 60*time.Second, "overall wall-clock budget for the run (boot + all tests)")
	testCmd.Flags().BoolVar(&testNoServe, "no-serve", false, "do not start the runtime; build the config and run tests only")
	testCmd.Flags().BoolVar(&testFailNoTest, "fail-if-no-tests", false, "exit non-zero if the configuration contains no test blocks")

	// Default quieter than serve: a test run wants clean pass/fail output, not
	// routine startup/shutdown info logs interleaved with it. Overridable with -l.
	testCmd.Flags().StringVarP(&logLevel, "log-level", "l", "warn", "log level (debug, info, warn, error)")
	testCmd.Flags().StringVarP(&filePath, "file-path", "f", "", "base directory for file functions (enables file, fileexists, fileset functions)")
	testCmd.Flags().StringVarP(&writePath, "write-path", "w", "", "base directory for file write functions; must be under --file-path")
	testCmd.Flags().BoolVar(&allowKill, "allow-kill", false, "enable the kill function (feature \"allowkill\")")
	testCmd.Flags().StringVar(&pluginPath, "plugin-path", "", "directory containing Go plugin .so files; required if any .vinit plugin block is present")
}

func runTest(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true

	logger, err := setupLogger()
	if err != nil {
		return fmt.Errorf("failed to setup logger: %w", err)
	}
	defer logger.Sync() //nolint:errcheck

	var filter func(string) bool
	if testRun != "" {
		re, rerr := regexp.Compile(testRun)
		if rerr != nil {
			return fmt.Errorf("invalid --run pattern: %w", rerr)
		}
		filter = re.MatchString
	}

	configBuilder := config.NewConfig().
		WithLogger(logger).
		WithSources(stringSliceToAnySlice(args)...).
		WithPluginPath(pluginPath).
		WithTesting(true)

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
	reportWarnings(diags)
	if diags.HasErrors() {
		return diags
	}

	// --fail-if-no-tests catches a config that forgot to declare any tests.
	if cfg.FunctyTestCount() == 0 {
		if !testJSON {
			fmt.Fprintln(cmd.OutOrStdout(), "no tests")
		} else {
			writeTestJSON(cmd.OutOrStdout(), nil, 0)
		}
		if testFailNoTest {
			return errors.New("no tests")
		}
		return nil
	}

	// Full mode boots the runtime like serve; --no-serve skips it (and teardown).
	if !testNoServe {
		for _, startable := range cfg.Startables {
			if serr := startable.Start(); serr != nil {
				logger.Error("Failed to start component", zap.Error(serr))
			}
		}
		for _, ps := range cfg.PostStartables {
			if perr := ps.PostStart(); perr != nil {
				logger.Error("Failed to post-start component", zap.Error(perr))
			}
		}
		defer shutdown(cfg, logger)
	}

	// The injected ctx is cancelable through this timeout, so context-aware
	// functions a test calls (send, http, ...) are bounded; a watchdog also
	// aborts the run if the tests themselves overrun the budget.
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	type runResult struct {
		outcomes []functy.TestOutcome
		err      error
	}
	done := make(chan runResult, 1)
	go func() {
		o, e := cfg.RunTests(ctx, filter)
		done <- runResult{outcomes: o, err: e}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			return fmt.Errorf("running tests: %w", r.err)
		}
		return reportTestOutcomes(cmd, cfg, r.outcomes)
	case <-ctx.Done():
		return fmt.Errorf("test run timed out after %s", testTimeout)
	}
}

// reportTestOutcomes prints the human or JSON report and returns a non-nil error
// when any test failed, so the command exits non-zero. deselected counts tests
// excluded by --run.
func reportTestOutcomes(cmd *cobra.Command, cfg *config.Config, outcomes []functy.TestOutcome) error {
	deselected := cfg.FunctyTestCount() - len(outcomes)

	if testJSON {
		// JSON goes to stdout so it is cleanly redirectable (`... --json > report.json`);
		// zap logs stay on stderr, keeping the two streams separate.
		if writeTestJSON(cmd.OutOrStdout(), outcomes, deselected) > 0 {
			return errTestsFailed
		}
		return nil
	}

	out := cmd.OutOrStdout()
	files := cfg.FunctyFiles()
	passed, failed, skipped := 0, 0, 0
	for _, o := range outcomes {
		switch {
		case o.Skipped:
			skipped++
			if testVerbose {
				fmt.Fprintf(out, "SKIP %s%s (%s)\n", o.Name, skipReasonNote(o.SkipReason), fmtTestDur(o.Duration))
			}
		case o.Failed():
			failed++
			fmt.Fprintf(out, "FAIL %s (%s)\n", o.Name, fmtTestDur(o.Duration))
			printDiags(out, files, o.Diagnostics())
		default:
			passed++
			if testVerbose {
				fmt.Fprintf(out, "ok   %s (%s)\n", o.Name, fmtTestDur(o.Duration))
			}
		}
	}

	fmt.Fprintf(out, "\n%d passed, %d failed, %d skipped%s\n",
		passed, failed, skipped, deselectedNote(deselected))
	if failed > 0 {
		return errTestsFailed
	}
	return nil
}

// errTestsFailed drives a non-zero exit when one or more tests failed. The
// per-test FAIL lines and the summary footer are already printed; main() adds a
// final "Error: tests failed".
var errTestsFailed = errors.New("tests failed")

func fmtTestDur(d time.Duration) string {
	if d < time.Microsecond {
		return "<1µs"
	}
	return d.Round(time.Microsecond).String()
}

func skipReasonNote(r string) string {
	if r == "" {
		return ""
	}
	return ": " + r
}

func deselectedNote(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf(" (%d deselected by --run)", n)
}

// jsonTestReport is the top-level --json document: one entry per test that ran
// plus aggregate counts, mirroring `functy test --json`.
type jsonTestReport struct {
	Tests   []jsonTestEntry `json:"tests"`
	Summary jsonTestSummary `json:"summary"`
}

type jsonTestSummary struct {
	Passed     int `json:"passed"`
	Failed     int `json:"failed"`
	Skipped    int `json:"skipped"`
	Deselected int `json:"deselected"`
}

type jsonTestEntry struct {
	Name       string           `json:"name"`
	Status     string           `json:"status"` // "passed", "failed", or "skipped"
	DurationMs float64          `json:"duration_ms"`
	Location   *jsonTestRange   `json:"location,omitempty"`
	SkipReason string           `json:"skip_reason,omitempty"`
	Failures   []jsonTestFailed `json:"failures,omitempty"` // one per failure (soft expect() failures, then the hard failure)
}

type jsonTestFailed struct {
	Message  string         `json:"message"`
	Detail   string         `json:"detail,omitempty"`
	Location *jsonTestRange `json:"location,omitempty"`
}

type jsonTestRange struct {
	File      string `json:"file"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	EndLine   int    `json:"end_line"`
	EndColumn int    `json:"end_column"`
}

func testRangeToJSON(r hcl.Range) *jsonTestRange {
	return &jsonTestRange{
		File:      r.Filename,
		Line:      r.Start.Line,
		Column:    r.Start.Column,
		EndLine:   r.End.Line,
		EndColumn: r.End.Column,
	}
}

// writeTestJSON emits the --json report and returns the number of failed tests.
// Passing/failing/skipped tests are all included regardless of -v.
func writeTestJSON(w io.Writer, outcomes []functy.TestOutcome, deselected int) (failed int) {
	rep := jsonTestReport{
		Tests:   make([]jsonTestEntry, 0, len(outcomes)),
		Summary: jsonTestSummary{Deselected: deselected},
	}
	for _, o := range outcomes {
		jt := jsonTestEntry{
			Name:       o.Name,
			DurationMs: float64(o.Duration.Nanoseconds()) / 1e6,
			Location:   testRangeToJSON(o.DefRange),
		}
		switch {
		case o.Skipped:
			jt.Status = "skipped"
			jt.SkipReason = o.SkipReason
			rep.Summary.Skipped++
		case o.Failed():
			jt.Status = "failed"
			jt.Failures = testFailuresToJSON(o)
			rep.Summary.Failed++
			failed++
		default:
			jt.Status = "passed"
			rep.Summary.Passed++
		}
		rep.Tests = append(rep.Tests, jt)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(rep)
	return failed
}

// testFailuresToJSON renders a failed outcome's error diagnostics as one
// jsonTestFailed each — every recorded expect() soft failure, then the hard
// failure — in report order, mirroring `functy test --json`.
func testFailuresToJSON(o functy.TestOutcome) []jsonTestFailed {
	var out []jsonTestFailed
	for _, d := range o.Diagnostics() {
		if d.Severity != hcl.DiagError {
			continue
		}
		f := jsonTestFailed{Message: d.Summary, Detail: d.Detail}
		if d.Subject != nil {
			f.Location = testRangeToJSON(*d.Subject)
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		// A failed outcome normally yields at least one error diagnostic; fall back to
		// the raw error rather than emitting a failed test with no failure at all.
		msg := "test failed"
		if o.Err != nil {
			msg = o.Err.Error()
		}
		out = append(out, jsonTestFailed{Message: msg, Location: testRangeToJSON(o.DefRange)})
	}
	return out
}
