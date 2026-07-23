package config

import (
	"fmt"
	"regexp"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"go.uber.org/zap"
)

// vinitSchema is the closed top-level schema for .vinit files. Only block
// types listed here are accepted; unknown blocks produce a fatal
// diagnostic. New .vinit block types are added by appending to Blocks.
var vinitSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "plugin", LabelNames: []string{"label"}},
		{Type: "git", LabelNames: []string{"label"}},
	},
}

// pluginLabelRegex enforces the allowed plugin-label syntax. The label
// must start with a letter, digit, or underscore; remaining characters
// may also include hyphens. The pattern disallows '/', '\', '.', '..',
// and any other path-separator-adjacent characters so a label cannot be
// abused to escape the configured plugin directory.
var pluginLabelRegex = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]*$`)

// PluginDefinition is the structural decode of a `plugin "<label>" { ... }`
// block in a .vinit file. Disabled is consumed by Vinculum; the remaining
// body is handed to the plugin's VinculumPluginInit for its own decoding.
type PluginDefinition struct {
	Label    string   `hcl:",label"`
	Disabled bool     `hcl:"disabled,optional"`
	Body     hcl.Body `hcl:",remain"`
}

// vinitEvalContext builds the minimal eval context used for .vinit
// expressions: `env.<NAME>` plus the cty standard library. No const, no
// user functions, no plugin-contributed values, no bus/server/client/ctx —
// none of those things exist yet when .vinit is evaluated.
//
// The stdlib functions are obtained by looking up the in-tree "stdlib"
// FunctionPlugin (registered from functions/stdlib.go). If that package
// is not blank-imported (e.g. in a config-only test binary), the result
// is an empty function map.
func vinitEvalContext() *hcl.EvalContext {
	return &hcl.EvalContext{
		Variables: map[string]cty.Value{
			"env": hclutil.EnvObject(),
		},
		Functions: vinitStdlibFunctions(),
	}
}

// vinitStdlibFunctions returns the cty standard library functions
// registered as the "stdlib" FunctionPlugin. The getter ignores its
// config argument; nil is safe.
func vinitStdlibFunctions() map[string]function.Function {
	for _, p := range functionPlugins {
		if p.name == "stdlib" {
			return p.getter(nil)
		}
	}
	return map[string]function.Function{}
}

// processVinit runs the .vinit bootstrap pass. It enumerates .vinit files
// from the same sources passed to WithSources, parses them, and processes
// their blocks in a fixed order: `plugin` blocks first, then any future
// block types in the order they are added to vinitSchema.
//
// On any fatal diagnostic the function returns immediately; subsequent
// blocks are not processed.
func processVinit(sources []any, pluginPath string, logger *zap.Logger) hcl.Diagnostics {
	return processVinitBlocks(sources, pluginPath, logger, true)
}

// ProcessVinitPlugins runs only the plugin-loading portion of the .vinit pass,
// deliberately skipping `git` blocks (which clone and materialize repos — a
// side effect no read-only tool should trigger). Tooling such as `fmt` calls it
// so plugin-registered contributions (e.g. functy types via RegisterFunctyType)
// are available when parsing/formatting, without the cost of a full Build.
func ProcessVinitPlugins(sources []any, pluginPath string, logger *zap.Logger) hcl.Diagnostics {
	return processVinitBlocks(sources, pluginPath, logger, false)
}

// processVinitBlocks is the shared implementation. processGit gates the git
// materialization pass so read-only tooling can load plugins without it.
func processVinitBlocks(sources []any, pluginPath string, logger *zap.Logger, processGit bool) hcl.Diagnostics {
	bodies, diags := ParseVinitFiles(sources...)
	if diags.HasErrors() {
		return diags
	}
	if len(bodies) == 0 {
		return nil
	}

	evalCtx := vinitEvalContext()

	var pluginBlocks []*hcl.Block
	var gitBlocks []*hcl.Block
	for _, body := range bodies {
		content, contentDiags := body.Content(vinitSchema)
		diags = diags.Extend(contentDiags)
		for _, block := range content.Blocks {
			switch block.Type {
			case "plugin":
				pluginBlocks = append(pluginBlocks, block)
			case "git":
				gitBlocks = append(gitBlocks, block)
			}
		}
	}
	if diags.HasErrors() {
		return diags
	}

	// Process plugin blocks first (in source order) so any system-wide
	// registration they perform is available before later bootstrap steps.
	seen := make(map[string]hcl.Range)
	for _, block := range pluginBlocks {
		diags = diags.Extend(processPluginBlock(block, evalCtx, pluginPath, logger, seen))
		if diags.HasErrors() {
			return diags
		}
	}

	// Then git blocks (in source order), with their own label namespace —
	// unless the caller opted out (read-only tooling that must not materialize).
	if processGit {
		seenGit := make(map[string]hcl.Range)
		for _, block := range gitBlocks {
			diags = diags.Extend(processGitBlock(block, evalCtx, logger, seenGit))
			if diags.HasErrors() {
				return diags
			}
		}
	}

	return diags
}

// processPluginBlock validates a single `plugin "<label>" { ... }` block,
// evaluates its `disabled` attribute, and dispatches to loadPlugin.
func processPluginBlock(
	block *hcl.Block,
	evalCtx *hcl.EvalContext,
	pluginPath string,
	logger *zap.Logger,
	seen map[string]hcl.Range,
) hcl.Diagnostics {
	var def PluginDefinition
	diags := gohcl.DecodeBody(block.Body, evalCtx, &def)
	if diags.HasErrors() {
		return diags
	}
	def.Label = block.Labels[0]

	labelRange := block.LabelRanges[0]

	if !pluginLabelRegex.MatchString(def.Label) {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid plugin label",
			Detail: fmt.Sprintf(
				"Plugin label %q does not match the required pattern %s.",
				def.Label, pluginLabelRegex.String()),
			Subject: &labelRange,
		}}
	}

	if prev, dup := seen[def.Label]; dup {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Duplicate plugin label",
			Detail: fmt.Sprintf(
				"Plugin %q was already declared at %s.",
				def.Label, prev),
			Subject: &labelRange,
		}}
	}
	seen[def.Label] = labelRange

	if def.Disabled {
		return nil
	}

	if pluginPath == "" {
		defRange := block.DefRange
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "plugin block requires --plugin-path",
			Detail: fmt.Sprintf(
				"Plugin %q is declared but --plugin-path was not given on the command line.",
				def.Label),
			Subject: &defRange,
		}}
	}

	pluginLogger := logger
	if pluginLogger != nil {
		pluginLogger = pluginLogger.With(zap.String("plugin", def.Label))
	}

	ctx := &PluginContext{
		Block:       block,
		EvalContext: evalCtx,
		Logger:      pluginLogger,
	}

	return loadPlugin(pluginPath, def.Label, ctx)
}
