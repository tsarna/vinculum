package config

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A metrics or tracing backend that is auto-wired must not depend on where its
// block appears in the file. These cover the ordering mechanism itself: which
// blocks count as backends, and which handlers take a dependency on them.

func parseBlocks(t *testing.T, src string) hcl.Blocks {
	t.Helper()
	file, diags := hclsyntax.ParseConfig([]byte(src), "test.vcl", hcl.InitialPos)
	require.False(t, diags.HasErrors(), "parse: %s", diags.Error())
	content, _, diags := file.Body.PartialContent(configSchema)
	require.False(t, diags.HasErrors(), "content: %s", diags.Error())
	return content.Blocks
}

func blockOfType(t *testing.T, blocks hcl.Blocks, blockType string) *hcl.Block {
	t.Helper()
	for _, block := range blocks {
		if block.Type == blockType {
			return block
		}
	}
	t.Fatalf("no %q block in fixture", blockType)
	return nil
}

func TestBackendBlockIDs(t *testing.T) {
	blocks := parseBlocks(t, `
bus "b" {}
server "metrics" "prom" { listen = "127.0.0.1:0" }
server "http" "web" { listen = "127.0.0.1:0" }
client "otlp" "t" { endpoint = "http://127.0.0.1:4318" }
client "http" "api" { url = "http://example.com" }
metric "counter" "m" {}
`)

	// Only the two block types that can be a backend, and both of them: a
	// server "http" is not one however much it is instrumented.
	assert.Equal(t, []string{"server.prom", "client.t"}, backendBlockIDs(blocks))
}

func TestIsBackendBlockIgnoresMalformedLabels(t *testing.T) {
	// GetBlockDependencyId indexes Labels[1]; a block that reached the sort with
	// one label would panic there, so the predicate has to answer for the shape
	// rather than for the type alone.
	assert.False(t, IsBackendBlock(&hcl.Block{Type: "server", Labels: []string{"metrics"}}))
	assert.False(t, IsBackendBlock(&hcl.Block{Type: "client", Labels: []string{"otlp"}}))
}

// The distribution step: every handler that asks for the IDs gets them, not
// just the one that had the mechanism to itself.
func TestSetImplicitBackendDepsReachesEveryDependentHandler(t *testing.T) {
	blocks := parseBlocks(t, `
condition "timer" "c" {}
metric "counter" "m" {}
client "otlp" "t" { endpoint = "http://127.0.0.1:4318" }
`)
	handlers := GetBlockHandlers()
	setImplicitBackendDeps(handlers, blocks)

	for _, blockType := range []string{"condition", "metric"} {
		_, ok := handlers[blockType].(BackendDependent)
		require.True(t, ok, "%s handler should implement BackendDependent", blockType)

		deps, diags := handlers[blockType].GetBlockDependencies(blockOfType(t, blocks, blockType))
		require.False(t, diags.HasErrors(), "%s: %s", blockType, diags.Error())
		assert.Contains(t, deps, "client.t", "%s block should be ordered after the otlp client", blockType)
	}
}

// A condition has no attribute to name a backend with, so the dependency is
// unconditional — including when the condition names other blocks of its own.
func TestConditionAlwaysDependsOnTheBackends(t *testing.T) {
	blocks := parseBlocks(t, `
bus "main" {}
condition "timer" "c" {
    on_activate = send(ctx, bus.main, "t", "p")
}
server "metrics" "prom" { listen = "127.0.0.1:0" }
`)
	handlers := GetBlockHandlers()
	setImplicitBackendDeps(handlers, blocks)

	deps, diags := handlers["condition"].GetBlockDependencies(blockOfType(t, blocks, "condition"))
	require.False(t, diags.HasErrors(), "%s", diags.Error())
	assert.Contains(t, deps, "server.prom")
	assert.Contains(t, deps, "bus.main", "the block's own references must survive")
}

// A block that names its backend already depends on it by reference, and must
// not be ordered after backends it has nothing to do with.
func TestMetricWithExplicitServerTakesNoImplicitDep(t *testing.T) {
	blocks := parseBlocks(t, `
server "metrics" "prom" { listen = "127.0.0.1:0" }
server "metrics" "other" { listen = "127.0.0.1:0" }
metric "counter" "explicit" { server = server.prom }
`)
	handlers := GetBlockHandlers()
	setImplicitBackendDeps(handlers, blocks)

	deps, diags := handlers["metric"].GetBlockDependencies(blockOfType(t, blocks, "metric"))
	require.False(t, diags.HasErrors(), "%s", diags.Error())
	assert.Equal(t, []string{"server.prom"}, deps)
}

func TestMetricWithoutServerTakesTheImplicitDeps(t *testing.T) {
	blocks := parseBlocks(t, `
metric "counter" "m" {}
server "metrics" "prom" { listen = "127.0.0.1:0" }
`)
	handlers := GetBlockHandlers()
	setImplicitBackendDeps(handlers, blocks)

	deps, diags := handlers["metric"].GetBlockDependencies(blockOfType(t, blocks, "metric"))
	require.False(t, diags.HasErrors(), "%s", diags.Error())
	assert.Equal(t, []string{"server.prom"}, deps)
}

// With no backend anywhere, nothing is added: the handlers behave exactly as
// they did before the mechanism existed.
func TestNoBackendMeansNoImplicitDeps(t *testing.T) {
	blocks := parseBlocks(t, `
condition "timer" "c" {}
metric "counter" "m" {}
`)
	handlers := GetBlockHandlers()
	setImplicitBackendDeps(handlers, blocks)

	for _, blockType := range []string{"condition", "metric"} {
		deps, diags := handlers[blockType].GetBlockDependencies(blockOfType(t, blocks, blockType))
		require.False(t, diags.HasErrors(), "%s: %s", blockType, diags.Error())
		assert.Empty(t, deps, "%s", blockType)
	}
}
