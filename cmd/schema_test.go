package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
)

// generateTestSchema builds the schema document the command would emit. The
// cmd package blank-imports every subsystem, so the registries are fully
// populated here in a way they are not inside package config itself.
func generateTestSchema(t *testing.T, opts config.SchemaGenOptions) *config.SchemaDocument {
	t.Helper()
	doc, problems := config.GenerateSchema(opts)
	require.NotNil(t, doc)
	if !opts.RequireDocs {
		// Orphaned curation is always a bug, whether or not --strict is on.
		assert.Empty(t, problemStrings(problems), "curated metadata does not match the parsed structure")
	}
	return doc
}

func TestSchemaTopLevelBlocks(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})

	assert.Equal(t, config.SchemaFormatVersion, doc.SchemaVersion)
	assert.NotEmpty(t, doc.VinculumVersion)

	// Every block the parser accepts, and nothing else.
	expected := []string{
		"assert", "bus", "client", "condition", "const", "editor", "fsm",
		"function", "jq", "metric", "server", "subscription", "trigger",
		"var", "wire_format",
	}
	assert.ElementsMatch(t, expected, keysOf(doc.Blocks))

	// The drift this feature exists to prevent: cron and signals are trigger
	// types, not top-level blocks, however the hand-written editor tooling
	// used to describe them.
	assert.NotContains(t, doc.Blocks, "cron")
	assert.NotContains(t, doc.Blocks, "signals")

	// Typed blocks carry a variant dimension; plain blocks carry a body.
	for _, name := range []string{"client", "server", "trigger", "condition", "metric", "wire_format", "editor"} {
		block := doc.Blocks[name]
		require.NotNil(t, block, name)
		assert.Equal(t, "type", block.VariantLabel, "%s should be typed", name)
		assert.Equal(t, []string{"type", "name"}, block.Labels, "%s labels", name)
		assert.Nil(t, block.Body, "%s should have no body of its own", name)
	}
	for _, name := range []string{"subscription", "bus", "var", "fsm", "assert", "const", "function", "jq"} {
		block := doc.Blocks[name]
		require.NotNil(t, block, name)
		assert.Empty(t, block.VariantLabel, "%s should be plain", name)
		assert.NotNil(t, block.Body, "%s should have a body", name)
	}
	assert.Empty(t, doc.Blocks["const"].Labels, "const takes no labels")
}

// TestSchemaCoversRegisteredTypes cross-checks the emitted variants against the
// registry names recorded at init(), so a newly registered type cannot be
// missing from the schema.
func TestSchemaCoversRegisteredTypes(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})

	typedBlocks := []string{"client", "server", "trigger", "condition", "wire_format", "editor"}
	registered := map[string][]string{}
	for _, plugin := range config.RegisteredPlugins() {
		kind, name, found := strings.Cut(plugin, ".")
		if !found {
			continue
		}
		for _, blockType := range typedBlocks {
			if kind == blockType {
				registered[blockType] = append(registered[blockType], name)
			}
		}
	}

	for _, blockType := range typedBlocks {
		require.NotEmpty(t, registered[blockType], "no %s types registered", blockType)
		block := doc.Blocks[blockType]
		require.NotNil(t, block)
		for _, name := range registered[blockType] {
			assert.Contains(t, block.Variants, name, "%s %q missing from schema", blockType, name)
		}
	}

	// Spot-check a few known types so the cross-check can't pass vacuously.
	assert.Contains(t, doc.Blocks["client"].Variants, "mqtt")
	assert.Contains(t, doc.Blocks["server"].Variants, "mcp")
	assert.Contains(t, doc.Blocks["trigger"].Variants, "cron")
	assert.Contains(t, doc.Blocks["condition"].Variants, "flipflop")
}

// TestSchemaConditionalTypes covers types whose availability depends on config
// state: they are emitted as part of the superset, flagged conditional.
func TestSchemaConditionalTypes(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})

	fileTrigger := doc.Blocks["trigger"].Variants["file"]
	require.NotNil(t, fileTrigger, "conditional trigger \"file\" missing")
	assert.True(t, fileTrigger.Conditional)
	assert.NotEmpty(t, fileTrigger.Attributes, "structure comes from the registered sample")
	assert.NotNil(t, findAttr(fileTrigger, "path"))

	// Unconditional types are not flagged.
	assert.False(t, doc.Blocks["trigger"].Variants["cron"].Conditional)
}

// TestSchemaCommonAttributes covers the attributes a block handler decodes
// before dispatching to the type-specific processor: they belong to every
// variant's authored surface even though no variant's struct declares them.
func TestSchemaCommonAttributes(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})

	for name, variant := range doc.Blocks["client"].Variants {
		assert.NotNil(t, findAttr(variant, "disabled"), "client %q is missing disabled", name)
	}
	for name, variant := range doc.Blocks["server"].Variants {
		assert.NotNil(t, findAttr(variant, "disabled"), "server %q is missing disabled", name)
	}
	for name, variant := range doc.Blocks["trigger"].Variants {
		assert.NotNil(t, findAttr(variant, "disabled"), "trigger %q is missing disabled", name)
		assert.NotNil(t, findAttr(variant, "tracing"), "trigger %q is missing tracing", name)
	}

	// A variant that declares a common attribute itself is not given a
	// duplicate: trigger "file" has its own `disabled`.
	assert.Len(t, attrsNamed(doc.Blocks["trigger"].Variants["file"], "disabled"), 1)

	// condition blocks have no such envelope — the whole body goes to the
	// subtype — so nothing is spliced in.
	assert.Nil(t, findAttr(doc.Blocks["condition"].Variants["timer"], "disabled"))
}

func TestSchemaCommandOutput(t *testing.T) {
	out, err := runSchemaCommand(t)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &doc))
	assert.Equal(t, config.SchemaFormatVersion, doc["schemaVersion"])
	assert.Contains(t, doc, "vinculumVersion")
	assert.Contains(t, doc["blocks"].(map[string]any), "subscription")

	// Pretty by default, compact on request.
	assert.Contains(t, out, "\n  ")
	compact, err := runSchemaCommand(t, "--pretty=false")
	require.NoError(t, err)
	assert.NotContains(t, compact, "\n  ")
	assert.True(t, strings.HasSuffix(compact, "\n"))
}

func TestSchemaCommandWritesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema.json")
	out, err := runSchemaCommand(t, "-o", path)
	require.NoError(t, err)
	assert.Empty(t, out, "output went to the file, not stdout")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	assert.Contains(t, doc, "blocks")
}

func TestSchemaCommandStrict(t *testing.T) {
	// The in-tree curation must always satisfy --strict.
	_, err := runSchemaCommand(t, "--strict")
	assert.NoError(t, err)
}

func TestSchemaCommandUsageErrors(t *testing.T) {
	_, err := runSchemaCommand(t, "--format", "yaml")
	require.Error(t, err)
	assert.Equal(t, 2, ExitCode(err))

	_, err = runSchemaCommand(t, "--require-docs")
	require.Error(t, err)
	assert.Equal(t, 2, ExitCode(err), "--require-docs without --strict is a usage error")
}

func TestExitCodeDefaultsToOne(t *testing.T) {
	assert.Equal(t, 1, ExitCode(assert.AnError))
	assert.Equal(t, 7, ExitCode(&ExitCodeError{Code: 7, Err: assert.AnError}))
}

// --- helpers ----------------------------------------------------------------

// runSchemaCommand runs `vinculum schema` in-process and returns its stdout.
func runSchemaCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()

	// Flags bind to package-level variables that cobra does not reset between
	// runs, so restore their declared defaults before each one.
	schemaFormat, schemaPretty, schemaOutput = "json", true, ""
	schemaStrict, schemaRequireDocs = false, false

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs(append([]string{"schema"}, args...))
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	return stdout.String(), err
}

func findAttr(body *config.SchemaBody, name string) *config.SchemaAttr {
	for _, attr := range body.Attributes {
		if attr.Name == name {
			return attr
		}
	}
	return nil
}

func attrsNamed(body *config.SchemaBody, name string) []*config.SchemaAttr {
	var found []*config.SchemaAttr
	for _, attr := range body.Attributes {
		if attr.Name == name {
			found = append(found, attr)
		}
	}
	return found
}

func keysOf[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func problemStrings(problems []error) []string {
	out := make([]string, len(problems))
	for i, p := range problems {
		out[i] = p.Error()
	}
	return out
}
