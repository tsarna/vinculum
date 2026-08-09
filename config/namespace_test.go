package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

// withCleanAmbientProviders isolates a test from the process-global ambient
// registry, so a fixture provider cannot leak into other tests.
func withCleanAmbientProviders(t *testing.T) {
	t.Helper()
	saved := ambientProviders
	ambientProviders = append([]ambientEntry(nil), saved...)
	t.Cleanup(func() { ambientProviders = saved })
}

// withCleanBlockNamespaces does the same for the curated block-namespace map.
func withCleanBlockNamespaces(t *testing.T) {
	t.Helper()
	saved := blockNamespaceSchemas
	blockNamespaceSchemas = map[string]NamespaceSchema{}
	for k, v := range saved {
		blockNamespaceSchemas[k] = v
	}
	t.Cleanup(func() { blockNamespaceSchemas = saved })
}

// registerFixtureNamespace registers a provider returning val, described by ns.
func registerFixtureNamespace(t *testing.T, name string, val cty.Value, ns NamespaceSchema) {
	t.Helper()
	withCleanAmbientProviders(t)
	ambientProviders = append(ambientProviders, ambientEntry{
		name:   name,
		p:      func(*Config) cty.Value { return val },
		schema: &ns,
	})
}

// fixtureProblems returns the problems mentioning the fixture namespace.
//
// GenerateSchema always reports a couple of context shapes as unnamed inside
// package config: the shapes config itself registers are named by attributes in
// the leaf packages, which are not linked into this test binary. That closure is
// the business of the cmd package's whole-binary test, not of this one.
func fixtureProblems(t *testing.T, opts SchemaGenOptions) []string {
	t.Helper()
	_, problems := GenerateSchema(opts)
	var out []string
	for _, p := range problemStrings(problems) {
		if strings.Contains(p, "fixture") {
			out = append(out, p)
		}
	}
	return out
}

// generateFixtureSchema generates under the strictest options and returns the
// document with only the fixture's problems, for a case that expects none.
func generateFixtureSchema(t *testing.T) (*SchemaDocument, []string) {
	t.Helper()
	opts := SchemaGenOptions{Strict: true, RequireDocs: true}
	doc, _ := GenerateSchema(opts)
	return doc, fixtureProblems(t, opts)
}

// TestNamespaceReflectsTheProvidersValue covers the mechanized half: names and
// types come from the value the provider returns, not from the prose beside it.
func TestNamespaceReflectsTheProvidersValue(t *testing.T) {
	registerFixtureNamespace(t, "fixture", cty.ObjectVal(map[string]cty.Value{
		"name":  cty.StringVal("vinculum"),
		"count": cty.NumberIntVal(3),
		"on":    cty.BoolVal(true),
		"tags":  cty.ListVal([]cty.Value{cty.StringVal("a")}),
	}), NamespaceSchema{
		Summary: "Fixture.",
		Members: map[string]MemberMeta{
			"name":  {Summary: "A name."},
			"count": {Summary: "A count."},
			"on":    {Summary: "A flag."},
			"tags":  {Summary: "Some tags."},
		},
	})

	doc, problems := generateFixtureSchema(t)
	assert.Empty(t, problems)

	ns := doc.Namespaces["fixture"]
	require.NotNil(t, ns)
	assert.Equal(t, NamespaceProvider, ns.Kind)

	// Members are sorted by name: a cty object has no declaration order to keep,
	// and a stable order is what lets the generated docs be diffed.
	var names, types []string
	for _, m := range ns.Members {
		names = append(names, m.Name)
		types = append(types, m.Type)
	}
	assert.Equal(t, []string{"count", "name", "on", "tags"}, names)
	assert.Equal(t, []string{"number", "string", "bool", "list"}, types)

	// Nothing is a constant namespace by default, so no values are emitted.
	for _, m := range ns.Members {
		assert.Empty(t, m.Value, m.Name)
	}
}

// TestNamespaceCurationClosure covers the two-sided contract: the value is
// authoritative, and the prose must match it in both directions.
func TestNamespaceCurationClosure(t *testing.T) {
	t.Run("member with nothing said about it", func(t *testing.T) {
		registerFixtureNamespace(t, "fixture", cty.ObjectVal(map[string]cty.Value{
			"described":   cty.StringVal("x"),
			"undescribed": cty.StringVal("y"),
		}), NamespaceSchema{
			Summary: "Fixture.",
			Members: map[string]MemberMeta{"described": {Summary: "Described."}},
		})

		assert.Contains(t, fixtureProblems(t, SchemaGenOptions{RequireDocs: true}),
			"fixture.undescribed: missing summary")
	})

	t.Run("prose for a member that does not exist", func(t *testing.T) {
		registerFixtureNamespace(t, "fixture", cty.ObjectVal(map[string]cty.Value{
			"real": cty.StringVal("x"),
		}), NamespaceSchema{
			Summary: "Fixture.",
			Members: map[string]MemberMeta{
				"real":  {Summary: "Real."},
				"stale": {Summary: "Removed some time ago."},
			},
		})

		assert.Contains(t, fixtureProblems(t, SchemaGenOptions{}),
			`fixture: documented member "stale" does not exist`)
	})

	t.Run("namespace with no summary", func(t *testing.T) {
		registerFixtureNamespace(t, "fixture", cty.EmptyObjectVal, NamespaceSchema{})
		assert.Contains(t, fixtureProblems(t, SchemaGenOptions{RequireDocs: true}),
			"namespace fixture: missing summary")
	})

	t.Run("provider with no schema at all", func(t *testing.T) {
		withCleanAmbientProviders(t)
		ambientProviders = append(ambientProviders, ambientEntry{
			name: "fixture",
			p:    func(*Config) cty.Value { return cty.EmptyObjectVal },
		})

		doc, problems := GenerateSchema(SchemaGenOptions{RequireDocs: true})
		assert.Contains(t, problemStrings(problems),
			"namespace fixture: no namespace schema registered")
		assert.True(t, doc.Namespaces["fixture"].Undocumented)
	})
}

// TestNamespaceObjectMembers covers a member that is itself an object: it must
// say what it contains, or say that its contents are not the language's to know.
func TestNamespaceObjectMembers(t *testing.T) {
	nested := cty.ObjectVal(map[string]cty.Value{
		"nested": cty.ObjectVal(map[string]cty.Value{"leaf": cty.StringVal("x")}),
	})

	t.Run("described a level down", func(t *testing.T) {
		registerFixtureNamespace(t, "fixture", nested, NamespaceSchema{
			Summary: "Fixture.",
			Members: map[string]MemberMeta{
				"nested": {
					Summary: "A nested object.",
					Members: map[string]MemberMeta{"leaf": {Summary: "A leaf."}},
				},
			},
		})

		doc, problems := generateFixtureSchema(t)
		assert.Empty(t, problems)
		members := doc.Namespaces["fixture"].Members
		require.Len(t, members, 1)
		require.Len(t, members[0].Members, 1)
		assert.Equal(t, "leaf", members[0].Members[0].Name)
	})

	t.Run("saying nothing is a problem", func(t *testing.T) {
		registerFixtureNamespace(t, "fixture", nested, NamespaceSchema{
			Summary: "Fixture.",
			Members: map[string]MemberMeta{"nested": {Summary: "A nested object."}},
		})

		assert.Contains(t, fixtureProblems(t, SchemaGenOptions{}),
			"fixture.nested: is an object; describe its members or mark them free")
	})

	t.Run("members on a scalar", func(t *testing.T) {
		registerFixtureNamespace(t, "fixture", cty.ObjectVal(map[string]cty.Value{
			"scalar": cty.StringVal("x"),
		}), NamespaceSchema{
			Summary: "Fixture.",
			Members: map[string]MemberMeta{
				"scalar": {Summary: "A scalar.", FreeMembers: true},
			},
		})

		assert.Contains(t, fixtureProblems(t, SchemaGenOptions{}),
			"fixture.scalar: has no members of its own to describe (string)")
	})
}

// TestNamespaceFreeMembers covers the roots whose member names come from
// outside the language. Only what is described is emitted, which is also what
// keeps the document from varying with the machine that produced it.
func TestNamespaceFreeMembers(t *testing.T) {
	registerFixtureNamespace(t, "fixture", cty.ObjectVal(map[string]cty.Value{
		"HOSTS_SPECIFIC_TO_THIS_MACHINE": cty.StringVal("x"),
		"always_here":                    cty.StringVal("y"),
	}), NamespaceSchema{
		Summary:     "Fixture.",
		FreeMembers: true,
		Members:     map[string]MemberMeta{"always_here": {Summary: "Always here."}},
	})

	doc, problems := generateFixtureSchema(t)
	assert.Empty(t, problems, "an undescribed free member is not a gap")

	ns := doc.Namespaces["fixture"]
	assert.True(t, ns.FreeMembers)
	require.Len(t, ns.Members, 1)
	assert.Equal(t, "always_here", ns.Members[0].Name)
}

// TestNamespaceConstantValues covers the namespaces whose values are part of
// the language rather than of the environment.
func TestNamespaceConstantValues(t *testing.T) {
	t.Run("values are emitted", func(t *testing.T) {
		registerFixtureNamespace(t, "fixture", cty.ObjectVal(map[string]cty.Value{
			"code": cty.NumberIntVal(404),
			"name": cty.StringVal("NotFound"),
			"flag": cty.False,
			"map":  cty.MapVal(map[string]cty.Value{"404": cty.StringVal("NotFound")}),
		}), NamespaceSchema{
			Summary:              "Fixture.",
			Constant:             true,
			UniformMemberSummary: "One of the fixture's codes.",
		})

		doc, problems := generateFixtureSchema(t)
		assert.Empty(t, problems, "a uniform summary documents every member")

		byName := map[string]*SchemaMember{}
		for _, m := range doc.Namespaces["fixture"].Members {
			byName[m.Name] = m
			assert.Equal(t, "One of the fixture's codes.", m.Summary)
		}
		assert.Equal(t, "404", byName["code"].Value)
		assert.Equal(t, "NotFound", byName["name"].Value)
		assert.Equal(t, "false", byName["flag"].Value)
		assert.Empty(t, byName["map"].Value, "a map has no literal form worth emitting")
	})

	t.Run("a uniform summary needs constant values", func(t *testing.T) {
		registerFixtureNamespace(t, "fixture", cty.EmptyObjectVal, NamespaceSchema{
			Summary:              "Fixture.",
			UniformMemberSummary: "Says nothing on its own.",
		})

		assert.Contains(t, fixtureProblems(t, SchemaGenOptions{}),
			"namespace fixture: a uniform member summary says nothing unless the values are constant and emitted")
	})
}

// TestNamespaceProviderMisbehaviour covers what a provider may return. A
// third-party plugin must not be able to take down `vinculum man`.
func TestNamespaceProviderMisbehaviour(t *testing.T) {
	t.Run("not an object", func(t *testing.T) {
		registerFixtureNamespace(t, "fixture", cty.StringVal("not an object"),
			NamespaceSchema{Summary: "Fixture."})

		assert.Contains(t, fixtureProblems(t, SchemaGenOptions{}),
			"namespace fixture: provider returned string, not an object")
	})

	t.Run("panics", func(t *testing.T) {
		withCleanAmbientProviders(t)
		ambientProviders = append(ambientProviders, ambientEntry{
			name:   "fixture",
			p:      func(*Config) cty.Value { panic("no config for you") },
			schema: &NamespaceSchema{Summary: "Fixture."},
		})

		assert.Contains(t, fixtureProblems(t, SchemaGenOptions{}),
			"namespace fixture: provider panicked: no config for you")
	})
}

// TestBlockNamespaces covers the other half of the namespace: the roots the
// config author fills in.
func TestBlockNamespaces(t *testing.T) {
	t.Run("every root names a real block", func(t *testing.T) {
		doc, _ := GenerateSchema(SchemaGenOptions{Strict: true, RequireDocs: true})
		for name, ns := range doc.Namespaces {
			if ns.Kind == NamespaceBlock {
				assert.Contains(t, doc.Blocks, ns.Block, "namespace %q", name)
			}
		}
	})

	t.Run("naming a block that does not exist", func(t *testing.T) {
		withCleanBlockNamespaces(t)
		blockNamespaceSchemas["fixture"] = NamespaceSchema{
			Block:   "no_such_block",
			Summary: "Fixture.",
		}

		assert.Contains(t, fixtureProblems(t, SchemaGenOptions{}),
			`namespace fixture: no top-level block type "no_such_block"`)
	})

	t.Run("a block namespace has no members to describe", func(t *testing.T) {
		withCleanBlockNamespaces(t)
		blockNamespaceSchemas["fixture"] = NamespaceSchema{
			Block:   "bus",
			Summary: "Fixture.",
			Members: map[string]MemberMeta{"whatever": {Summary: "Not the schema's to know."}},
		}

		assert.Contains(t, fixtureProblems(t, SchemaGenOptions{}),
			"namespace fixture: a block namespace has no members to describe; its names come from the config")
	})

	t.Run("two providers claiming one root", func(t *testing.T) {
		registerFixtureNamespace(t, "fixture", cty.EmptyObjectVal, NamespaceSchema{Summary: "Fixture."})
		registerFixtureNamespace(t, "fixture", cty.EmptyObjectVal, NamespaceSchema{Summary: "Fixture."})

		assert.Contains(t, fixtureProblems(t, SchemaGenOptions{}),
			`namespace "fixture" is registered by two ambient providers`)
	})

	t.Run("a root cannot be both", func(t *testing.T) {
		withCleanBlockNamespaces(t)
		blockNamespaceSchemas["fixture"] = NamespaceSchema{Block: "bus", Summary: "A block fixture."}
		registerFixtureNamespace(t, "fixture", cty.EmptyObjectVal, NamespaceSchema{Summary: "Fixture."})

		// Described the way the runtime resolves it: ambient providers populate
		// Constants first, and each block handler writes its root over whatever
		// was there, so the block is what an expression actually reaches.
		doc, _ := GenerateSchema(SchemaGenOptions{})
		assert.Equal(t, NamespaceBlock, doc.Namespaces["fixture"].Kind)
		assert.Equal(t, "A block fixture.", doc.Namespaces["fixture"].Summary)

		assert.Contains(t, fixtureProblems(t, SchemaGenOptions{}),
			`namespace "fixture" is both an ambient provider and a block namespace; the block wins at runtime`)
	})
}
