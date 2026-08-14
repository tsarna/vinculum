package config

import (
	"fmt"
	"regexp"
	"slices"

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
	Label    string   `hcl:"name,label"`
	Disabled bool     `hcl:"disabled,optional"`
	Body     hcl.Body `hcl:",remain"`
}

func init() {
	RegisterBlockSchema("plugin", pluginSchema)
}

// VinitDisabledAttr documents `disabled` on a .vinit block. DisabledAttr is
// written in .vcl terms — a disabled block publishing no name for expressions
// to read — and none of that is true here: nothing in a .vinit file publishes a
// name, and what a disabled block skips is a side effect at startup.
var VinitDisabledAttr = AttrMeta{
	Summary: "Skip this block entirely.",
	Doc: "Nothing the block would do at startup happens. It is evaluated against the `.vinit` " +
		"context, where an environment variable that is not set is not an attribute of `env` " +
		"at all — so gate on an optional one through `try`, as " +
		"`disabled = try(env.SKIP_BOOTSTRAP, \"\") != \"\"`, rather than reading it directly " +
		"and failing when it is absent.",
	Hint: HintBool,
}

// pluginSchema describes the `plugin` block. Only `disabled` is Vinculum's; the
// rest of the body belongs to the plugin, which is why there is nothing else to
// describe here.
var pluginSchema = TypeSchema{
	Sample:  &PluginDefinition{},
	Summary: "Loads a Go shared-object plugin before any configuration is parsed.",
	DocPage: "plugins.md#the-plugin-block",
	Doc: `The label names the ` + "`.so`" + ` file to load, relative to ` + "`--plugin-path`" + `: label
` + "`weather`" + ` loads ` + "`<plugin-path>/weather.so`" + `. That is why it is restricted to letters,
digits, underscores, and hyphens — a label cannot name a path — and why it must be
unique across all ` + "`.vinit`" + ` files. Without ` + "`--plugin-path`" + `, a ` + "`plugin`" + ` block is a fatal
error rather than a silently skipped one.

Plugin blocks are processed before ` + "`git`" + ` blocks, so a plugin's registrations are in
place for everything that follows.

**Every attribute other than ` + "`disabled`" + ` belongs to the plugin**, which decodes the
rest of the body itself against a schema Vinculum does not know. An unrecognized
name here is reported by the plugin, not by Vinculum.

	plugin "weather" {
	    api_key = env.WEATHER_API_KEY
	}`,
	Attrs: map[string]AttrMeta{
		"disabled": VinitDisabledAttr,
	},
}

// vinitEvalContext builds the minimal eval context used for .vinit
// expressions: `env.<NAME>` plus the standard library. No const, no
// user functions, no plugin-contributed values, no bus/server/client/ctx —
// none of those things exist yet when .vinit is evaluated.
func vinitEvalContext() *hcl.EvalContext {
	return &hcl.EvalContext{
		Variables: map[string]cty.Value{
			"env": hclutil.EnvObject(),
		},
		Functions: vinitStdlibFunctions(),
	}
}

// vinitStdlibGroups names the function plugins whose functions a .vinit
// expression may call: the cty standard library, and functy's host-agnostic
// builtins.
//
// The second group is what puts `try()` and `can()` here, and they belong here
// more than anywhere else — `env` is the only namespace a .vinit has, and an
// environment variable that is not set is not an attribute of it, so `try(env.X,
// "")` is how a bootstrap block reads an optional one. The rest of the group
// (typeof, cond, switch, error, assert) is pure and host-agnostic, which is what
// makes it safe to evaluate before anything else exists.
var vinitStdlibGroups = []string{"stdlib", "functy_stdlib"}

// vinitStdlibFunctions returns the functions of the vinitStdlibGroups plugins.
// Their getters ignore the *Config argument, so nil is safe. A group whose
// package is not blank-imported (as in a config-only test binary) contributes
// nothing, rather than failing.
func vinitStdlibFunctions() map[string]function.Function {
	funcs := make(map[string]function.Function)
	for _, p := range functionPlugins {
		if !slices.Contains(vinitStdlibGroups, p.name) {
			continue
		}
		for name, fn := range p.getter(nil) {
			funcs[name] = fn
		}
	}
	return funcs
}

// processVinit runs the .vinit bootstrap pass. It enumerates .vinit files
// from the same sources passed to WithSources, parses them, and processes
// their blocks in a fixed order: `plugin` blocks first, then any future
// block types in the order they are added to vinitSchema.
//
// On any fatal diagnostic the function returns immediately; subsequent
// blocks are not processed.
// It returns the filename→file map of the .vinit files it parsed, so a caller
// can render these diagnostics with the offending source line quoted.
func processVinit(sources []any, pluginPath string, logger *zap.Logger) (map[string]*hcl.File, hcl.Diagnostics) {
	return processVinitBlocks(sources, pluginPath, logger, true)
}

// ProcessVinitPlugins runs only the plugin-loading portion of the .vinit pass,
// deliberately skipping `git` blocks (which clone and materialize repos — a
// side effect no read-only tool should trigger). Tooling such as `fmt` calls it
// so plugin-registered contributions (e.g. functy types via RegisterFunctyType)
// are available when parsing/formatting, without the cost of a full Build.
func ProcessVinitPlugins(sources []any, pluginPath string, logger *zap.Logger) hcl.Diagnostics {
	_, diags := processVinitBlocks(sources, pluginPath, logger, false)
	return diags
}

// processVinitBlocks is the shared implementation. processGit gates the git
// materialization pass so read-only tooling can load plugins without it.
func processVinitBlocks(sources []any, pluginPath string, logger *zap.Logger, processGit bool) (map[string]*hcl.File, hcl.Diagnostics) {
	bodies, files, diags := parseVinitFiles(sources...)
	if diags.HasErrors() {
		return files, diags
	}
	if len(bodies) == 0 {
		return files, nil
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
		return files, diags
	}

	// Process plugin blocks first (in source order) so any system-wide
	// registration they perform is available before later bootstrap steps.
	seen := make(map[string]hcl.Range)
	for _, block := range pluginBlocks {
		diags = diags.Extend(processPluginBlock(block, evalCtx, pluginPath, logger, seen))
		if diags.HasErrors() {
			return files, diags
		}
	}

	// Then git blocks (in source order), with their own label namespace —
	// unless the caller opted out (read-only tooling that must not materialize).
	if processGit {
		seenGit := make(map[string]hcl.Range)
		for _, block := range gitBlocks {
			diags = diags.Extend(processGitBlock(block, evalCtx, logger, seenGit))
			if diags.HasErrors() {
				return files, diags
			}
		}
	}

	return files, diags
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
