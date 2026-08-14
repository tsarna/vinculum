package schemadoc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
)

// paths renders candidates as "kind:a/b/c" strings, which is what the
// assertions compare — a candidate is identified by where it sits, and
// nothing else about it is interesting to resolution.
func paths(nodes []Node) []string {
	var out []string
	for _, n := range nodes {
		s := string(n.Kind) + ":"
		for i, p := range n.Path {
			if i > 0 {
				s += "/"
			}
			s += p
		}
		out = append(out, s)
	}
	return out
}

func TestResolvePaths(t *testing.T) {
	doc := testDoc()

	for _, tc := range []struct {
		name string
		path []string
		want []string
	}{
		{"top-level typed block", []string{"client"}, []string{"block:client"}},
		{"top-level plain block", []string{"subscription"}, []string{"block:subscription"}},
		{"a variant by its full path", []string{"client", "mqtt"}, []string{"block:client/mqtt"}},
		// The short form: a variant name resolves without naming its block.
		{"a variant by its bare name", []string{"mqtt"}, []string{"block:client/mqtt"}},
		{"a sub-block", []string{"client", "mqtt", "tls"}, []string{"block:client/mqtt/tls"}},
		{"a sub-block via the short form", []string{"mqtt", "tls"}, []string{"block:client/mqtt/tls"}},
		{"an attribute of a variant", []string{"client", "mqtt", "broker"}, []string{"block:client/mqtt/broker"}},
		{"a misspelled attribute", []string{"client", "mqtt", "brokers"}, nil},
		{"an attribute of a sub-block", []string{"client", "mqtt", "tls", "cert_file"}, []string{"block:client/mqtt/tls/cert_file"}},
		{"an attribute of a plain block", []string{"subscription", "action"}, []string{"block:subscription/action"}},
		{"a nested block two deep", []string{"server", "http", "handle"}, []string{"block:server/http/handle"}},
		{"a ctx shape", []string{"message"}, []string{"context:message"}},
		{"a ctx shape takes no members", []string{"message", "topic"}, nil},
		{"an unknown name", []string{"nope"}, nil},
		{"an unknown variant", []string{"client", "nope"}, nil},
		{"a path through an attribute", []string{"client", "mqtt", "broker", "more"}, nil},
		{"an empty path", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, paths(Resolve(doc, "", tc.path)))
		})
	}
}

func TestResolveAmbiguityWithinBlocks(t *testing.T) {
	doc := testDoc()

	// The collision that exists in the real schema: http is a client type and
	// a server type both.
	got := Resolve(doc, "", []string{"http"})
	assert.Equal(t, []string{"block:client/http", "block:server/http"}, paths(got))

	// Naming the block resolves it, which is what the menu will tell you.
	assert.Equal(t, []string{"block:client/http"}, paths(Resolve(doc, "", []string{"client", "http"})))

	// A continued short path stays ambiguous only where the continuation
	// exists in more than one candidate.
	assert.Equal(t, []string{"block:server/http/handle"}, paths(Resolve(doc, "", []string{"http", "handle"})))
}

func TestResolveKindRestriction(t *testing.T) {
	doc := testDoc()

	assert.Equal(t, []string{"context:message"}, paths(Resolve(doc, KindContext, []string{"message"})))
	assert.Empty(t, Resolve(doc, KindBlock, []string{"message"}), "a shape is not a block")
	assert.Empty(t, Resolve(doc, KindContext, []string{"client"}), "a block is not a shape")
	// Nothing registers function topics yet, so the kind resolves nothing
	// rather than erroring — the vocabulary exists ahead of the content.
	assert.Empty(t, Resolve(doc, KindFunction, []string{"client"}))
}

// A body could carry a sub-block and an attribute under one name. The parser
// permits it, so resolution must not silently prefer either: documenting the
// wrong one with no sign that it had is worse than saying it is ambiguous.
func TestResolveReportsAttributeAndBlockCollision(t *testing.T) {
	doc := testDoc()
	mqtt := doc.Blocks["client"].Variants["mqtt"]
	mqtt.Attributes = append(mqtt.Attributes, &config.SchemaAttr{
		Name: "tls", Type: "bool", Summary: "Shorthand for a default tls block.",
	})

	got := Resolve(doc, "", []string{"client", "mqtt", "tls"})
	require.Len(t, got, 2)
	assert.Equal(t, []string{"block:client/mqtt/tls", "block:client/mqtt/tls"}, paths(got))
	assert.Equal(t, shapeNested, got[0].shape)
	assert.Equal(t, shapeAttr, got[1].shape)
}

func TestMenuUsesTheLongerPathWhenKindsMatch(t *testing.T) {
	doc := testDoc()
	menu := MenuFor([]string{"http"}, Resolve(doc, "", []string{"http"}), CommandSpeller)

	assert.Equal(t, `"http" is ambiguous, choose one of:`, menu.Intro)
	// Same kind, different paths: the path disambiguates, and --type would be
	// noise that resolves nothing.
	assert.Equal(t, []string{
		"vinculum man client http",
		"vinculum man server http",
	}, menu.Items)
}

func TestMenuNamesTheKindWhenKindsDiffer(t *testing.T) {
	doc := testDoc()
	// A block and a shape under one name — the shape of the jq collision
	// (block type and function) that --type exists for.
	doc.Contexts["subscription"] = &config.SchemaContext{Summary: "Contrived."}

	menu := MenuFor([]string{"subscription"}, Resolve(doc, "", []string{"subscription"}), CommandSpeller)
	assert.Equal(t, []string{
		"vinculum man --type block subscription",
		"vinculum man --type context subscription",
	}, menu.Items)
}

func TestMenuDropsDuplicateSpellings(t *testing.T) {
	doc := testDoc()
	n := Resolve(doc, "", []string{"client", "http"})
	menu := MenuFor([]string{"client", "http"}, append(n, n...), CommandSpeller)
	assert.Equal(t, []string{"vinculum man client http"}, menu.Items)
}

func TestSuggestNearMisses(t *testing.T) {
	doc := testDoc()

	assert.Equal(t, []string{"block:subscription"}, paths(Suggest(doc, "", []string{"subscriptions"})))
	assert.Equal(t, []string{"block:client/mqtt"}, paths(Suggest(doc, "", []string{"mqqt"})))
	// Capitalization is a near miss, not a distance large enough to lose.
	assert.Equal(t, []string{"block:client"}, paths(Suggest(doc, "", []string{"Client"})))
	// A suggestion is always something that would itself resolve.
	for _, n := range Suggest(doc, "", []string{"conection"}) {
		assert.NotEmpty(t, Resolve(doc, "", n.Path))
	}
	assert.Empty(t, Suggest(doc, "", []string{"totally-unrelated"}))
}

func TestLeadingNamesAreTheSetShortPathsSearch(t *testing.T) {
	doc := testDoc()
	names := LeadingNames(doc, "")

	assert.Contains(t, names, "client", "a block type")
	assert.Contains(t, names, "mqtt", "a variant name")
	assert.Contains(t, names, "decode-error", "a ctx shape")
	// Attributes are not globally addressable: `action` appears in dozens of
	// bodies in the real schema, and a menu of dozens is not a menu.
	assert.NotContains(t, names, "broker")
	assert.NotContains(t, names, "action")

	// Every name it offers resolves.
	for _, n := range names {
		assert.NotEmpty(t, Resolve(doc, "", []string{n}), n)
	}

	assert.NotContains(t, LeadingNames(doc, KindBlock), "decode-error")
	assert.Equal(t, []string{"connection", "decode-error", "message"}, LeadingNames(doc, KindContext))
}

func TestTopicsAreTheIndexPage(t *testing.T) {
	doc := testDoc()
	got := paths(Topics(doc, ""))

	// Both file kinds are topics — `man git` resolves like any other block.
	// Which of them the index groups apart is Index's business, not Topics'.
	assert.Equal(t, []string{
		"block:client", "block:git", "block:server", "block:subscription",
		"context:connection", "context:decode-error", "context:message",
		"namespace:env", "namespace:http_status", "namespace:sys",
	}, got)
	// Variants belong under their block, not in the index: 43 of them would
	// bury the 15 blocks a reader is looking for.
	assert.NotContains(t, got, "block:client/mqtt")
	// Nor does a block namespace, which would list `subscription` twice for a
	// page that repeats what the block's page says.
	assert.NotContains(t, got, "namespace:subscription")
}
