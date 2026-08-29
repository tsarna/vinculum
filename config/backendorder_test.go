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
	ids := backendBlockIDs(blocks)
	assert.Equal(t, []string{"server.prom", "client.t"}, ids.All)
	// A tracer comes only from an otlp client, which is what lets the metrics
	// server depend on one without the graph closing on itself.
	assert.Equal(t, []string{"client.t"}, ids.Tracing)
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

// A block that names every backend it could name already depends on them by
// reference, and must not be ordered after backends it has nothing to do with.
func TestMetricNamingBothBackendsTakesNoImplicitDep(t *testing.T) {
	blocks := parseBlocks(t, `
server "metrics" "prom" { listen = "127.0.0.1:0" }
server "metrics" "other" { listen = "127.0.0.1:0" }
client "otlp" "t" { endpoint = "http://127.0.0.1:4318" }
metric "counter" "explicit" {
    server  = server.prom
    tracing = client.t
}
`)
	handlers := GetBlockHandlers()
	setImplicitBackendDeps(handlers, blocks)

	deps, diags := handlers["metric"].GetBlockDependencies(blockOfType(t, blocks, "metric"))
	require.False(t, diags.HasErrors(), "%s", diags.Error())
	assert.ElementsMatch(t, []string{"server.prom", "client.t"}, deps,
		"only the two it names, and only because it names them")
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

// Naming one backend and not the other is the case that makes this a rule about
// every attribute rather than any: a metric with an explicit `server` still
// auto-wires the tracer its computed `value` polls in, and a bus with explicit
// `metrics` still auto-wires a tracer. Skipping there would leave exactly the
// bug this exists to fix.
func TestNamingOneBackendStillTakesTheImplicitDeps(t *testing.T) {
	blocks := parseBlocks(t, `
metric "counter" "m" { server = server.prom }
bus "b" { metrics = server.prom }
server "metrics" "prom" { listen = "127.0.0.1:0" }
client "otlp" "t" { endpoint = "http://127.0.0.1:4318" }
`)
	handlers := GetBlockHandlers()
	setImplicitBackendDeps(handlers, blocks)

	for _, blockType := range []string{"metric", "bus"} {
		deps, diags := handlers[blockType].GetBlockDependencies(blockOfType(t, blocks, blockType))
		require.False(t, diags.HasErrors(), "%s: %s", blockType, diags.Error())
		assert.Contains(t, deps, "client.t",
			"%s names no tracing backend and must wait for the one it will auto-wire", blockType)
	}
}

// The blanket rule: a server or client block takes the dependency whatever its
// type, because the handler dispatches through a registry and cannot know which
// types read a backend.
func TestServerAndClientBlocksTakeTheImplicitDepsWhateverTheirType(t *testing.T) {
	blocks := parseBlocks(t, `
server "http" "web" { listen = "127.0.0.1:0" }
client "mqtt" "broker" { broker = "tcp://127.0.0.1:1883" }
client "otlp" "t" { endpoint = "http://127.0.0.1:4318" }
`)
	handlers := GetBlockHandlers()
	setImplicitBackendDeps(handlers, blocks)

	for _, block := range blocks {
		if IsBackendBlock(block) {
			continue
		}
		deps, diags := handlers[block.Type].GetBlockDependencies(block)
		require.False(t, diags.HasErrors(), "%s: %s", block.Type, diags.Error())
		assert.Contains(t, deps, "client.t", "%s %q", block.Type, block.Labels)
	}
}

// The exclusion, and the reason it is the one piece of real care here: a
// backend given the blanket rule waits for a set that includes itself, which
// the sort can only report as a cycle.
func TestBackendBlocksAreExcludedFromTheBlanketRule(t *testing.T) {
	blocks := parseBlocks(t, `
server "metrics" "prom" { listen = "127.0.0.1:0" }
server "metrics" "other" { listen = "127.0.0.1:0" }
client "otlp" "t" { endpoint = "http://127.0.0.1:4318" }
`)
	handlers := GetBlockHandlers()
	setImplicitBackendDeps(handlers, blocks)

	for _, block := range blocks {
		deps, diags := handlers[block.Type].GetBlockDependencies(block)
		require.False(t, diags.HasErrors(), "%s: %s", block.Type, diags.Error())

		self := block.Type + "." + block.Labels[1]
		assert.NotContains(t, deps, self, "a backend must not wait for itself")
		assert.NotContains(t, deps, "server.prom", "a backend must not wait for a metrics backend")
		assert.NotContains(t, deps, "server.other")
	}
}

// The metrics server is a backend that consumes one: `tracing` names a
// client "otlp" and auto-wires the default when omitted, so it must still be
// ordered after those — the case the spec's own table missed.
func TestMetricsServerWaitsForTheOtlpClientItAutoWires(t *testing.T) {
	blocks := parseBlocks(t, `
server "metrics" "prom" { listen = "127.0.0.1:0" }
client "otlp" "t" { endpoint = "http://127.0.0.1:4318" }
`)
	handlers := GetBlockHandlers()
	setImplicitBackendDeps(handlers, blocks)

	deps, diags := handlers["server"].GetBlockDependencies(blockOfType(t, blocks, "server"))
	require.False(t, diags.HasErrors(), "%s", diags.Error())
	assert.Equal(t, []string{"client.t"}, deps)

	// And the root waits for nothing.
	deps, diags = handlers["client"].GetBlockDependencies(blockOfType(t, blocks, "client"))
	require.False(t, diags.HasErrors(), "%s", diags.Error())
	assert.Empty(t, deps)
}

// Every block type that resolves a backend from an attribute the author may
// omit. The list is the point: the bug this fixes was one block type having the
// mechanism and twenty not.
func TestEveryDependentBlockTypeTakesTheImplicitDeps(t *testing.T) {
	blocks := parseBlocks(t, `
bus "b" {}
condition "timer" "c" {}
fsm "f" {}
metric "counter" "m" {}
subscription "s" {
    target = bus.b
    topics = ["a"]
    action = "noop"
}
trigger "interval" "tr" {
    every  = "1s"
    action = "noop"
}
client "otlp" "t" { endpoint = "http://127.0.0.1:4318" }
`)
	handlers := GetBlockHandlers()
	setImplicitBackendDeps(handlers, blocks)

	for _, blockType := range []string{"bus", "condition", "fsm", "metric", "subscription", "trigger"} {
		deps, diags := handlers[blockType].GetBlockDependencies(blockOfType(t, blocks, blockType))
		require.False(t, diags.HasErrors(), "%s: %s", blockType, diags.Error())
		assert.Contains(t, deps, "client.t", "%s block auto-wires a backend and must wait for it", blockType)
	}
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
