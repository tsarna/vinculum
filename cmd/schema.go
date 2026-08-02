package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/hashicorp/hcl/v2"
	"github.com/spf13/cobra"
	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

var (
	schemaFormat      string
	schemaPretty      bool
	schemaOutput      string
	schemaStrict      bool
	schemaRequireDocs bool
)

var schemaCmd = &cobra.Command{
	Use:   "schema",
	Short: "Print a machine-readable description of the configuration language",
	Long: `Print a machine-readable description of the VCL configuration language.

Emits a single JSON document describing every block type, every type-specific
variant (client "http" vs client "mqtt"), every attribute and nested sub-block,
plus curated documentation, value hints, and semantic constraints.

The structure is reflected from the same decode structs the parser uses, so it
describes exactly what this binary can parse.

By default no plugins are loaded and the output describes a stock binary. Give
config paths together with --plugin-path to load the plugins their .vinit files
declare first, so plugin-contributed block types are described too; the types
they add are then listed under "plugins" in the document. Only the plugin
bootstrap runs — git blocks are not materialized and no .vcl file is parsed.

Intended for editor tooling — completion, hover, linting — and for generating
reference documentation.

Examples:
  vinculum schema
  vinculum schema -o schema.json
  vinculum schema --pretty=false
  vinculum schema --strict --require-docs -o /dev/null
  vinculum schema --plugin-path /plugins ./configs/`,
	Args: cobra.ArbitraryArgs,
	RunE: runSchema,
}

func init() {
	rootCmd.AddCommand(schemaCmd)

	schemaCmd.Flags().StringVar(&schemaFormat, "format", "json", "output format (json)")
	schemaCmd.Flags().BoolVar(&schemaPretty, "pretty", true, "indent the JSON output")
	schemaCmd.Flags().StringVarP(&schemaOutput, "output", "o", "", "write to a file instead of stdout")
	schemaCmd.Flags().BoolVar(&schemaStrict, "strict", false, "fail if curated metadata does not match the parsed structure")
	schemaCmd.Flags().BoolVar(&schemaRequireDocs, "require-docs", false, "with --strict, also require every block and attribute to be documented")
	schemaCmd.Flags().StringVar(&pluginPath, "plugin-path", "", "directory of Go plugin .so files; load the plugins declared by the given config paths so their block types are described too")
}

func runSchema(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true

	if schemaFormat != "json" {
		return &ExitCodeError{Code: 2, Err: fmt.Errorf("unsupported --format %q (only \"json\" is supported)", schemaFormat)}
	}
	if schemaRequireDocs && !schemaStrict {
		return &ExitCodeError{Code: 2, Err: fmt.Errorf("--require-docs has no effect without --strict")}
	}
	// Loading plugins needs both halves: where the .so files are, and which
	// ones to load. Neither alone does anything, so say so rather than
	// silently emitting a stock-binary document.
	if pluginPath != "" && len(args) == 0 {
		return &ExitCodeError{Code: 2, Err: fmt.Errorf("--plugin-path needs config paths to search for .vinit plugin blocks")}
	}
	if pluginPath == "" && len(args) > 0 {
		return &ExitCodeError{Code: 2, Err: fmt.Errorf("config paths are only used to find plugins to load; pass --plugin-path as well")}
	}

	contributed, err := loadSchemaPlugins(cmd, args)
	if err != nil {
		return err
	}

	doc, problems := config.GenerateSchema(config.SchemaGenOptions{
		Strict:      schemaStrict,
		RequireDocs: schemaRequireDocs,
	})
	doc.Plugins = contributed

	// Curation problems are always worth surfacing; --strict decides whether
	// they stop the build.
	for _, problem := range problems {
		fmt.Fprintf(os.Stderr, "%s: %v\n", schemaProblemLabel(), problem)
	}
	if len(problems) > 0 && schemaStrict {
		return &ExitCodeError{Code: 1, Err: fmt.Errorf("%d schema problem(s)", len(problems))}
	}

	var data []byte
	if schemaPretty {
		data, err = json.MarshalIndent(doc, "", "  ")
	} else {
		data, err = json.Marshal(doc)
	}
	if err != nil {
		return &ExitCodeError{Code: 2, Err: fmt.Errorf("failed to encode schema: %w", err)}
	}
	data = append(data, '\n')

	if schemaOutput == "" {
		_, err = cmd.OutOrStdout().Write(data)
	} else {
		err = os.WriteFile(schemaOutput, data, 0o644)
	}
	if err != nil {
		return &ExitCodeError{Code: 2, Err: fmt.Errorf("failed to write schema: %w", err)}
	}
	return nil
}

// loadSchemaPlugins runs the plugin-only .vinit bootstrap over the given
// config paths, and returns the registry entries the plugins contributed.
//
// The contributions are computed by diffing the registry across the load
// rather than by naming the plugins: what a consumer of the document needs to
// know is which block types it gained, not which .so files produced them. The
// result is nil when no paths were given, which keeps the field absent from a
// stock-binary document rather than emitting an empty list.
func loadSchemaPlugins(cmd *cobra.Command, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	before := map[string]bool{}
	for _, name := range config.RegisteredPlugins() {
		before[name] = true
	}

	// A plugin that fails to load is fatal here, unlike in `vinculum fmt`
	// where unaffected files still format: a schema missing the types it was
	// asked to describe is worse than no schema at all.
	if diags := config.ProcessVinitPlugins(stringSliceToAnySlice(paths), pluginPath, zap.NewNop()); diags.HasErrors() {
		printDiags(cmd.ErrOrStderr(), map[string]*hcl.File{}, diags)
		return nil, &ExitCodeError{Code: 2, Err: errors.New("failed to load plugins")}
	}

	var contributed []string
	for _, name := range config.RegisteredPlugins() {
		if !before[name] {
			contributed = append(contributed, name)
		}
	}
	return contributed, nil
}

func schemaProblemLabel() string {
	if schemaStrict {
		return "error"
	}
	return "warning"
}

// ExitCodeError carries a specific process exit code out of a command, for
// callers that distinguish failure modes (`vinculum schema` uses 1 for a
// validation failure and 2 for a usage or I/O error).
type ExitCodeError struct {
	Code int
	Err  error
}

func (e *ExitCodeError) Error() string { return e.Err.Error() }

func (e *ExitCodeError) Unwrap() error { return e.Err }

// ExitCode returns the process exit code for an error returned by Execute:
// the error's own code if it carries one, otherwise 1.
func ExitCode(err error) int {
	var ece *ExitCodeError
	if errors.As(err, &ece) {
		return ece.Code
	}
	return 1
}
