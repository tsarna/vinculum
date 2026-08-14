package schemadoc

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
)

func TestWalkTypedBlockListsVariants(t *testing.T) {
	doc := testDoc()
	out := renderNode(BlockNode(doc, "client", doc.Blocks["client"]), WalkOptions{})

	assert.Contains(t, out, "# `client`")
	assert.Contains(t, out, "A connection to an external service.")
	// A typed block has no body of its own; its content is the variant list.
	assert.Contains(t, out, "## Types")
	assert.Contains(t, out, "- `http`")
	assert.Contains(t, out, "[`mqtt`](client-mqtt.md) — An MQTT 5.0 publisher and subscriber.")
	assert.NotContains(t, out, "broker", "variant attributes belong on the variant's own page")

	// Variants are listed in a stable order regardless of map iteration.
	assert.Less(t, strings.Index(out, "`http`"), strings.Index(out, "`mqtt`"))
}

func TestWalkVariantRendersBodyAndSynopsis(t *testing.T) {
	doc := testDoc()
	out := renderNode(VariantNode(doc, "client", "mqtt", doc.Blocks["client"], doc.Blocks["client"].Variants["mqtt"]), WalkOptions{})

	assert.Contains(t, out, "# `client \"mqtt\"`")

	// The synopsis leads with the header a config author would type, required
	// attributes first.
	assert.Contains(t, out, "```hcl\nclient \"mqtt\" \"<name>\" {")
	assert.Regexp(t, `broker\s+= "https://…"\s+# required`, out)
	assert.Regexp(t, `client_id\s+= string\s+# deprecated`, out)
	// Optional is the default and goes unmarked: annotating twelve of thirteen
	// lines with it says nothing, and buries the one mark that does.
	assert.Regexp(t, `disabled\s+= bool\n`, out)
	assert.Contains(t, out, "tls { … }  # optional")
	assert.Less(t, strings.Index(out, "broker "), strings.Index(out, "disabled "),
		"required attributes come before optional ones")

	// Both halves of the curated description survive.
	assert.Contains(t, out, "An MQTT 5.0 publisher and subscriber.")
	assert.Contains(t, out, "Available in expressions as `client.<name>`.")

	// The attribute overview, and the detail for what did not fit on one line.
	assert.Contains(t, out, "| Attribute | Type | Required | Description |")
	assert.Contains(t, out, "| `broker` | string (url) | yes | Broker URL. |")
	assert.Contains(t, out, "**`on_connect`**")
	assert.Contains(t, out, "Runs synchronously; no messages flow until it returns.")
	assert.Contains(t, out, "One of: `0`, `1`, `2`.")
	assert.Contains(t, out, "**Deprecated.** Use `id` instead.")

	// The constraint's own generated message is used verbatim, so the prose
	// here and in the JSON schema cannot diverge.
	assert.Contains(t, out, "- Specify at most one of broker or brokers.")

	// The sub-block is expanded inline, with its own constraints.
	assert.Contains(t, out, "## `tls`")
	assert.Contains(t, out, "*Optional; at most one.*")
	assert.Contains(t, out, "- cert_file and key_file must be specified together.")
}

// A type's page ends by pointing at the hand-written guide, which is where the
// worked examples and the reasoning live — the two things the schema cannot
// carry.
func TestWalkLinksToTheHandWrittenPage(t *testing.T) {
	doc := testDoc()

	out := renderNode(VariantNode(doc, "client", "mqtt", doc.Blocks["client"], doc.Blocks["client"].Variants["mqtt"]), WalkOptions{})
	assert.Contains(t, out, "## See also")
	assert.Contains(t, out, "- [client-mqtt.md](client-mqtt.md)")

	// The footer is the page's own, so it sits at the page's heading level —
	// not trailing the last sub-block, where a sink that indents by depth
	// would file it as documentation of that sub-block.
	assert.Regexp(t, `\n## See also\n`, out)
	assert.NotContains(t, out, "### See also")

	// A typed block's variant list links each entry to its own page, which is
	// what a generated index in doc/ is made of.
	index := renderNode(BlockNode(doc, "client", doc.Blocks["client"]), WalkOptions{})
	assert.Contains(t, index, "- [`mqtt`](client-mqtt.md) — An MQTT 5.0 publisher and subscriber.")
	// A variant with no page is still listed, just unlinked.
	assert.Contains(t, index, "- `http` — An HTTP(S) request/response client.")
}

func TestWalkInlinesContextShapes(t *testing.T) {
	doc := testDoc()
	out := renderNode(VariantNode(doc, "client", "mqtt", doc.Blocks["client"], doc.Blocks["client"].Variants["mqtt"]), WalkOptions{})

	assert.Contains(t, out, "`ctx` in `on_connect`")
	assert.Contains(t, out, "| `ctx.trace_id` | string | Current trace ID. *(every `ctx` carries this)* |")

	// An open shape says so, and the site's own additions are marked as the
	// site's rather than the shape's.
	assert.Contains(t, out, "`ctx` in `on_decode_error`")
	assert.Contains(t, out, "| `ctx.mqtt_topic` | string | The MQTT topic the message arrived on. *(added here)* |")
	assert.Contains(t, out, "*This shape is open: a particular site may carry fields beyond these.*")
	assert.Contains(t, out, "| `ctx.raw` | bytes | The undecodable payload. *(not always present)* |")
}

func TestWalkPlainBlock(t *testing.T) {
	doc := testDoc()
	out := renderNode(BlockNode(doc, "subscription", doc.Blocks["subscription"]), WalkOptions{})

	assert.Contains(t, out, "# `subscription`")
	assert.Contains(t, out, "```hcl\nsubscription \"<name>\" {")
	assert.Contains(t, out, "Subscribes to messages from a bus or client.")
	assert.Contains(t, out, "| `target` | expression (bus-ref) | yes |")
	assert.Contains(t, out, "- Specify at most one of action or subscriber.")
	assert.Contains(t, out, "`ctx` in `action`")
	assert.Contains(t, out, "| `ctx.topic` | string | Topic of the message. |")
}

// A .vinit block is written in a different file, in a different and much
// smaller expression language, and both facts are the reader's first problem —
// so the note comes off the file kind and appears on every page reached through
// such a block, not only its own.
func TestWalkVinitBlockSaysWhereItGoes(t *testing.T) {
	doc := testDoc()

	const note = "This block belongs in a `.vinit` bootstrap file"

	block := renderNode(BlockNode(doc, "git", doc.Blocks["git"]), WalkOptions{})
	assert.Contains(t, block, note)
	assert.Contains(t, block, "there is no `ctx`")
	assert.Contains(t, block, "```hcl\ngit \"<label>\" {")

	nested := doc.Blocks["git"].Body.Blocks["auth"]
	assert.Contains(t, renderNode(NestedNode(doc, []string{"git", "auth"}, nested), WalkOptions{}), note)
	assert.Contains(t,
		renderNode(AttrNode(doc, []string{"git", "auth", "token"}, nested.Attributes[0]), WalkOptions{}), note)

	// And not on a .vcl block, where every one of those names does exist.
	assert.NotContains(t, renderNode(BlockNode(doc, "subscription", doc.Blocks["subscription"]), WalkOptions{}), note)
}

// A breadcrumb spells its parent path the way the config writes it, which
// differs by block shape: a typed block's second element is the type label and
// belongs in the quotes, while a plain block's is a sub-block and does not —
// `git "auth"` would name a type that does not exist.
func TestBreadcrumbSpellsAPlainBlocksSubBlock(t *testing.T) {
	doc := testDoc()

	auth := doc.Blocks["git"].Body.Blocks["auth"]
	out := renderNode(AttrNode(doc, []string{"git", "auth", "token"}, auth.Attributes[0]), WalkOptions{})
	assert.Contains(t, out, "In `git` › `auth`.")

	tls := doc.Blocks["client"].Variants["mqtt"].Blocks["tls"]
	out = renderNode(AttrNode(doc, []string{"client", "mqtt", "tls", "cert_file"}, tls.Attributes[0]), WalkOptions{})
	assert.Contains(t, out, "In `client \"mqtt\"` › `tls`.")
}

// The index answers "what may I write?", which the two languages answer
// differently — so they are listed apart, though both remain resolvable topics.
func TestIndexListsBootstrapBlocksApart(t *testing.T) {
	out := RenderMarkdown(Index(testDoc(), WalkOptions{}), MarkdownOptions{})

	assert.Contains(t, out, "## Bootstrap blocks (`.vinit`)")
	assert.Contains(t, out, "- `git` — Clones a repository at startup.")

	blocks := strings.Index(out, "## Blocks")
	bootstrap := strings.Index(out, "## Bootstrap blocks")
	git := strings.Index(out, "- `git`")
	assert.Less(t, blocks, bootstrap, "the .vcl blocks come first")
	assert.Less(t, bootstrap, git, "and git is under the bootstrap heading, not among them")
}

func TestWalkAttribute(t *testing.T) {
	doc := testDoc()
	body := doc.Blocks["client"].Variants["mqtt"]
	var attr = body.Attributes[2] // on_connect
	require.Equal(t, "on_connect", attr.Name)

	out := renderNode(AttrNode(doc, []string{"client", "mqtt", "on_connect"}, attr), WalkOptions{})

	assert.Contains(t, out, "# `on_connect`")
	assert.Contains(t, out, "In `client \"mqtt\"`.")
	assert.Contains(t, out, "Runs synchronously; no messages flow until it returns.")
	assert.Contains(t, out, "`ctx` in `on_connect`")
}

func TestWalkContextShapeListsItsUsers(t *testing.T) {
	doc := testDoc()
	out := renderNode(ContextNode(doc, "decode-error", doc.Contexts["decode-error"]), WalkOptions{})

	assert.Contains(t, out, "# `ctx` — decode-error")
	assert.Contains(t, out, "Evaluated when a received payload cannot be decoded.")
	assert.Contains(t, out, "| `ctx.error` | string | The decode failure. |")
	// The shape's own page says where it comes from.
	assert.Contains(t, out, "## Evaluated by")
	assert.Contains(t, out, "`client \"mqtt\"` › `on_decode_error`")
}

func TestWalkBaseLevelOffsetsEveryHeading(t *testing.T) {
	doc := testDoc()
	out := renderNode(VariantNode(doc, "client", "mqtt", doc.Blocks["client"], doc.Blocks["client"].Variants["mqtt"]), WalkOptions{BaseLevel: 3})

	assert.Contains(t, out, "### `client \"mqtt\"`")
	assert.Contains(t, out, "#### Attributes")
	assert.Contains(t, out, "#### `tls`")
	assert.NotContains(t, out, "\n# ", "nothing should escape the base level")
}

func TestWalkNoHeadingSuppressesOnlyTheNodesOwn(t *testing.T) {
	doc := testDoc()
	out := renderNode(VariantNode(doc, "client", "mqtt", doc.Blocks["client"], doc.Blocks["client"].Variants["mqtt"]),
		WalkOptions{BaseLevel: 3, NoHeading: true})

	assert.NotContains(t, out, "### `client \"mqtt\"`")
	assert.Contains(t, out, "#### Attributes", "sub-headings still appear")
}

func TestWalkMaxDepthListsRatherThanExpands(t *testing.T) {
	doc := testDoc()
	n := VariantNode(doc, "client", "mqtt", doc.Blocks["client"], doc.Blocks["client"].Variants["mqtt"])

	out := renderNode(n, WalkOptions{MaxDepth: 1})
	assert.Contains(t, out, "## Blocks")
	assert.Contains(t, out, "- `tls` (optional) — TLS settings for the connection.")
	assert.NotContains(t, out, "cert_file", "past the depth limit a sub-block is a pointer, not a page")

	// Unlimited by default.
	assert.Contains(t, renderNode(n, WalkOptions{}), "cert_file")
}

func TestSynopsisValuePlaceholders(t *testing.T) {
	for _, tc := range []struct {
		name string
		attr config.SchemaAttr
		want string
	}{
		{"coarse type when nothing refines it", config.SchemaAttr{Type: "bool"}, "bool"},
		{"hint beats the coarse type", config.SchemaAttr{Type: "string", Hint: config.HintDuration}, `"5s"`},
		{"enum beats the coarse type", config.SchemaAttr{Type: "string", Enum: []string{"a", "b"}}, `"a" | "b"`},
		// A hint describes the element, so a list re-wraps it rather than
		// claiming the attribute takes a bare URL.
		{"list of hinted elements", config.SchemaAttr{Type: "list", Hint: config.HintURL}, `["https://…"]`},
		{"list with nothing to refine it", config.SchemaAttr{Type: "list"}, "list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, synopsisValue(&tc.attr))
		})
	}
}

func TestSynopsisTruncatesLongBodies(t *testing.T) {
	doc := testDoc()
	body := doc.Blocks["client"].Variants["mqtt"]
	for i := 0; i < synopsisMaxAttrs; i++ {
		body.Attributes = append(body.Attributes, &config.SchemaAttr{
			Name: "extra_" + string(rune('a'+i)), Type: "string", Summary: "Filler.",
		})
	}

	out := renderNode(VariantNode(doc, "client", "mqtt", doc.Blocks["client"], body), WalkOptions{})
	assert.Contains(t, out, "# … 6 more attributes")
	// Truncation is a synopsis concern only: the full list is still tabulated.
	assert.Contains(t, out, "`extra_a`")
}
