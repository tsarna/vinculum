package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/tsarna/vinculum/config"
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
describes exactly what this binary can parse. Plugins are not loaded: the
output describes a stock binary.

Intended for editor tooling — completion, hover, linting — and for generating
reference documentation.

Examples:
  vinculum schema
  vinculum schema -o schema.json
  vinculum schema --pretty=false
  vinculum schema --strict --require-docs -o /dev/null`,
	Args: cobra.NoArgs,
	RunE: runSchema,
}

func init() {
	rootCmd.AddCommand(schemaCmd)

	schemaCmd.Flags().StringVar(&schemaFormat, "format", "json", "output format (json)")
	schemaCmd.Flags().BoolVar(&schemaPretty, "pretty", true, "indent the JSON output")
	schemaCmd.Flags().StringVarP(&schemaOutput, "output", "o", "", "write to a file instead of stdout")
	schemaCmd.Flags().BoolVar(&schemaStrict, "strict", false, "fail if curated metadata does not match the parsed structure")
	schemaCmd.Flags().BoolVar(&schemaRequireDocs, "require-docs", false, "with --strict, also require every block and attribute to be documented")
}

func runSchema(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true

	if schemaFormat != "json" {
		return &ExitCodeError{Code: 2, Err: fmt.Errorf("unsupported --format %q (only \"json\" is supported)", schemaFormat)}
	}
	if schemaRequireDocs && !schemaStrict {
		return &ExitCodeError{Code: 2, Err: fmt.Errorf("--require-docs has no effect without --strict")}
	}

	doc, problems := config.GenerateSchema(config.SchemaGenOptions{
		Strict:      schemaStrict,
		RequireDocs: schemaRequireDocs,
	})

	// Curation problems are always worth surfacing; --strict decides whether
	// they stop the build.
	for _, problem := range problems {
		fmt.Fprintf(os.Stderr, "%s: %v\n", schemaProblemLabel(), problem)
	}
	if len(problems) > 0 && schemaStrict {
		return &ExitCodeError{Code: 1, Err: fmt.Errorf("%d schema problem(s)", len(problems))}
	}

	var data []byte
	var err error
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
