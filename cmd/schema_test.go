package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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

	// Every block either parser accepts, and nothing else. The two languages
	// share the map, so the file kind is part of the inventory rather than a
	// separate assertion: a block that moved between them would be a silent
	// change to what each file may contain.
	expectedVCL := []string{
		"assert", "auth", "bus", "check", "client", "condition", "const", "editor",
		"fsm", "function", "jq", "metric", "server", "subscription", "trigger",
		"var", "wire_format",
	}
	expectedVinit := []string{"git", "plugin"}
	assert.ElementsMatch(t, append(append([]string{}, expectedVCL...), expectedVinit...), keysOf(doc.Blocks))
	for _, name := range expectedVCL {
		assert.Equal(t, config.FileVCL, doc.Blocks[name].File, "%s is a .vcl block", name)
	}
	for _, name := range expectedVinit {
		assert.Equal(t, config.FileVinit, doc.Blocks[name].File, "%s is a .vinit block", name)
	}

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
	for _, name := range []string{"subscription", "bus", "var", "fsm", "assert", "const", "function", "jq", "git", "plugin"} {
		block := doc.Blocks[name]
		require.NotNil(t, block, name)
		assert.Empty(t, block.VariantLabel, "%s should be plain", name)
		assert.NotNil(t, block.Body, "%s should have a body", name)
	}
	assert.Empty(t, doc.Blocks["const"].Labels, "const takes no labels")
}

// The .vinit blocks come from a second source loop, over the same closed schema
// the .vinit parser is handed. This pins what their description has to carry
// for a reader to act on it: the file kind, the defaults the code applies, and
// the conflicts the parser refuses.
func TestSchemaVinitBlocks(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})

	git := doc.Blocks["git"]
	require.NotNil(t, git)
	require.NotNil(t, git.Body)
	assert.Equal(t, config.FileVinit, git.File)
	assert.Equal(t, "git.md", git.DocPage)

	assert.True(t, findAttr(git.Body, "repo").Required, "repo is required")
	assert.Equal(t, "1", findAttr(git.Body, "depth").Default, "the shallow-clone default the code applies")
	assert.Equal(t, []string{"branch", "tag", "commit"},
		git.Body.Constraints[0].Attributes, "the revision rule validateGitBlock enforces")

	auth := git.Body.Blocks["auth"]
	require.NotNil(t, auth)
	assert.False(t, auth.Repeatable, "one auth block")
	assert.False(t, auth.Required, "auth is optional — an anonymous clone is legal")
	var exclusive [][]string
	for _, c := range auth.Constraints {
		assert.Equal(t, config.ConstraintMutuallyExclusive, c.Kind)
		exclusive = append(exclusive, c.Attributes)
	}
	assert.ElementsMatch(t, [][]string{
		{"token", "username"},
		{"token", "password"},
		{"private_key", "private_key_file"},
		{"known_hosts", "insecure_ignore_host_key"},
	}, exclusive, "the conflicts validateGitAuth enforces")

	fetch := git.Body.Blocks["fetch"]
	require.NotNil(t, fetch)
	assert.True(t, fetch.Repeatable)
	assert.True(t, fetch.Required, "one or more: a clone with nowhere to put anything is an error")
	assert.Equal(t, ".", findAttr(&fetch.SchemaBody, "from").Default)
	assert.True(t, findAttr(&fetch.SchemaBody, "into").Required)

	// The plugin body belongs to the plugin: `disabled` is the only attribute
	// Vinculum decodes, and the only one it can describe.
	plugin := doc.Blocks["plugin"]
	require.NotNil(t, plugin)
	require.NotNil(t, plugin.Body)
	assert.Equal(t, config.FileVinit, plugin.File)
	assert.Equal(t, []string{"disabled"}, attrNames(plugin.Body))
}

// A .vinit expression sees `env.*` and the cty standard library and nothing
// else, so no attribute of a .vinit block may name a `ctx` shape. An AttrMeta
// with a Context would otherwise list `git` among the blocks that evaluate
// against that shape, in a file where `ctx` does not exist.
func TestSchemaVinitBlocksNameNoContext(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})

	for name, block := range doc.Blocks {
		if block.File != config.FileVinit {
			continue
		}
		require.NotNil(t, block.Body, name)
		var walk func(path string, body *config.SchemaBody)
		walk = func(path string, body *config.SchemaBody) {
			for _, attr := range body.Attributes {
				assert.Empty(t, attr.Context, "%s.%s names a ctx shape, but .vinit has no ctx", path, attr.Name)
			}
			for sub, nested := range body.Blocks {
				walk(path+"."+sub, &nested.SchemaBody)
			}
		}
		walk(name, block.Body)
	}
}

// The receivers whose parser demands a subscription say so in the schema. A
// slice field reflects as 0..n, so this is the one cardinality that is only
// true because someone curated it — and the one that silently reverts.
func TestSchemaRepeatableBlocksWithAFloor(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})

	for _, tc := range []struct{ path, parent, block string }{
		{"client kafka", "receiver", "subscription"},
		{"client mqtt", "receiver", "subscription"},
		{"client redis_pubsub", "subscriber", "channel_subscription"},
	} {
		t.Run(tc.path+" "+tc.parent, func(t *testing.T) {
			blockType, variant, _ := strings.Cut(tc.path, " ")
			body := doc.Blocks[blockType].Variants[variant]
			require.NotNil(t, body, tc.path)
			parent := body.Blocks[tc.parent]
			require.NotNil(t, parent, tc.parent)

			nested := parent.Blocks[tc.block]
			require.NotNil(t, nested, tc.block)
			assert.True(t, nested.Repeatable, "more than one is allowed")
			assert.True(t, nested.Required, "and one is required")
		})
	}

	// The senders alongside them take no floor: none is legal there.
	sender := doc.Blocks["client"].Variants["kafka"].Blocks["sender"]
	require.NotNil(t, sender)
	assert.True(t, sender.Repeatable)
	assert.False(t, sender.Required)
}

func attrNames(body *config.SchemaBody) []string {
	names := make([]string, 0, len(body.Attributes))
	for _, attr := range body.Attributes {
		names = append(names, attr.Name)
	}
	return names
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

// TestSchemaCoversAmbientProviders is the same cross-check for the other
// registry: every ambient provider recorded at init() must have a namespace in
// the document, so registering one and describing it are a single change.
func TestSchemaCoversAmbientProviders(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})

	var registered []string
	for _, plugin := range config.RegisteredPlugins() {
		if kind, name, found := strings.Cut(plugin, "."); found && kind == "ambient" {
			registered = append(registered, name)
		}
	}
	require.NotEmpty(t, registered, "no ambient providers registered")
	for _, name := range registered {
		ns := doc.Namespaces[name]
		require.NotNil(t, ns, "ambient provider %q missing from schema", name)
		assert.Equal(t, config.NamespaceProvider, ns.Kind, "%s should be a provider namespace", name)
	}
	assert.Subset(t, registered, []string{"env", "http_status", "sys"})
}

// TestSchemaNamespaces covers what the namespace section says about the two
// kinds of root and about the members whose shape is not simply "a scalar".
func TestSchemaNamespaces(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})

	// A block namespace names the block that publishes into it, and has no
	// members of its own: they are whatever the config declares.
	for name, ns := range doc.Namespaces {
		if ns.Kind != config.NamespaceBlock {
			continue
		}
		assert.NotEmpty(t, ns.Block, "namespace %q names no block", name)
		assert.Contains(t, doc.Blocks, ns.Block, "namespace %q names an unknown block", name)
		assert.True(t, ns.FreeMembers, "namespace %q should be free", name)
		assert.Empty(t, ns.Members, "namespace %q should have no members", name)
	}
	assert.Equal(t, "bus", doc.Namespaces["bus"].Block)

	// env's members are the environment of whatever process is running, so
	// nothing is enumerated — otherwise the document would describe the machine
	// that produced it.
	env := doc.Namespaces["env"]
	require.NotNil(t, env)
	assert.True(t, env.FreeMembers)
	assert.Empty(t, env.Members)

	// http_status's values are the same in every process, so they are emitted.
	status := doc.Namespaces["http_status"]
	require.NotNil(t, status)
	assert.True(t, status.Constant)
	assert.Equal(t, "404", findMember(t, status.Members, "NotFound").Value)
	assert.Equal(t, "map", findMember(t, status.Members, "bycode").Type)

	// sys's values describe the machine, so no value is emitted for them.
	sys := doc.Namespaces["sys"]
	require.NotNil(t, sys)
	assert.False(t, sys.Constant)
	assert.Empty(t, findMember(t, sys.Members, "hostname").Value)
	assert.Equal(t, "string", findMember(t, sys.Members, "hostname").Type)
	// A capsule is named the way a .cty annotation would name it.
	assert.Equal(t, "time", findMember(t, sys.Members, "starttime").Type)

	// An object member is described a level down.
	functy := findMember(t, sys.Members, "functy")
	assert.Equal(t, "object", functy.Type)
	assert.NotEmpty(t, findMember(t, functy.Members, "version").Summary)

	// sys.signals carries whichever signals the host defines, so only the fixed
	// member beside them is described — the document must not vary by OS.
	signals := findMember(t, sys.Members, "signals")
	assert.True(t, signals.FreeMembers)
	require.Len(t, signals.Members, 1)
	assert.Equal(t, "bynumber", signals.Members[0].Name)
}

func findMember(t *testing.T, members []*config.SchemaMember, name string) *config.SchemaMember {
	t.Helper()
	for _, m := range members {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("no member %q", name)
	return nil
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

	for name, variant := range doc.Blocks["condition"].Variants {
		assert.NotNil(t, findAttr(variant, "disabled"), "condition %q is missing disabled", name)
	}

	// A variant that declares a common attribute itself is not given a
	// duplicate: trigger "file" has its own `disabled`.
	assert.Len(t, attrsNamed(doc.Blocks["trigger"].Variants["file"], "disabled"), 1)
}

// TestSchemaIsCompleteAndConsistent is the anti-drift guard the whole feature
// rests on. It is the `vinculum schema --strict --require-docs` invariant,
// enforced by `go test ./...` rather than only by a CI step.
//
// Two kinds of problem are reported, and each has a different fix:
//
//   - "missing summary" — an `hcl` field was added without documenting it.
//     Add an AttrMeta for it to the block's TypeSchema.
//   - "does not exist" — curated metadata names something the decode struct no
//     longer has. Delete or rename the AttrMeta.
//
// Either way the schema and the parser have diverged, which is exactly what
// this document exists not to do.
func TestSchemaIsCompleteAndConsistent(t *testing.T) {
	_, problems := config.GenerateSchema(config.SchemaGenOptions{
		Strict:      true,
		RequireDocs: true,
	})
	assert.Empty(t, problemStrings(problems),
		"the schema no longer matches the parser; see the comment on this test")

	// Nothing may be flagged undocumented, at any depth.
	doc := generateTestSchema(t, config.SchemaGenOptions{})
	for name, block := range doc.Blocks {
		require.NotNil(t, block, name)
		assert.False(t, block.Undocumented, "block %q is undocumented", name)
		assert.NotEmpty(t, block.Summary, "block %q has no summary", name)
		if block.Body != nil {
			assertBodyDocumented(t, name, block.Body)
		}
		for variant, body := range block.Variants {
			assertBodyDocumented(t, name+" "+variant, body)
		}
	}
	for name, ns := range doc.Namespaces {
		require.NotNil(t, ns, name)
		assert.False(t, ns.Undocumented, "namespace %q is undocumented", name)
		assert.NotEmpty(t, ns.Summary, "namespace %q has no summary", name)
		assertMembersDocumented(t, name, ns.Members)
	}
}

func assertMembersDocumented(t *testing.T, path string, members []*config.SchemaMember) {
	t.Helper()
	for _, m := range members {
		assert.NotEmpty(t, m.Summary, "%s.%s has no summary", path, m.Name)
		assert.NotEmpty(t, m.Type, "%s.%s has no type", path, m.Name)
		assertMembersDocumented(t, path+"."+m.Name, m.Members)
	}
}

func assertBodyDocumented(t *testing.T, path string, body *config.SchemaBody) {
	t.Helper()
	assert.False(t, body.Undocumented, "%s is undocumented", path)
	assert.NotEmpty(t, body.Summary, "%s has no summary", path)
	for _, attr := range body.Attributes {
		assert.NotEmpty(t, attr.Summary, "%s.%s has no summary", path, attr.Name)
	}
	for name, nested := range body.Blocks {
		assertBodyDocumented(t, path+"."+name, &nested.SchemaBody)
	}
}

func TestSchemaSubscriptionCuration(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})
	sub := doc.Blocks["subscription"].Body
	require.NotNil(t, sub)

	topics := findAttr(sub, "topics")
	require.NotNil(t, topics)
	assert.True(t, topics.Required)
	assert.Equal(t, config.HintTopicPattern, topics.Hint)

	// An action is evaluated per message, not at config load, and sees a
	// message-shaped ctx.
	action := findAttr(sub, "action")
	require.NotNil(t, action)
	assert.Equal(t, config.HintActionExpression, action.Hint)
	assert.Equal(t, "message", action.Context)

	// A subscriber is anything implementing bus.Subscriber, while a target
	// resolves an event bus and nothing else.
	assert.Equal(t, config.HintSubscriberRef, findAttr(sub, "subscriber").Hint)
	assert.Equal(t, config.HintBusRef, findAttr(sub, "target").Hint)

	// Transform functions are a DSL of their own, not general expressions.
	transforms := findAttr(sub, "transforms")
	require.NotNil(t, transforms)
	assert.Equal(t, config.HintTransformPipeline, transforms.Hint)

	kinds := map[config.ConstraintKind][]string{}
	for _, c := range sub.Constraints {
		kinds[c.Kind] = c.Attributes
		assert.NotEmpty(t, c.Message, "constraint %s has no message", c.Kind)
	}
	assert.Equal(t, []string{"action", "subscriber"}, kinds[config.ConstraintMutuallyExclusive])
	assert.Equal(t, []string{"action", "subscriber"}, kinds[config.ConstraintAtLeastOneOf])
}

func TestSchemaMetricVariants(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})
	metric := doc.Blocks["metric"]

	// metric is typed but has no registry behind it: the variants come from
	// the block's own schema.
	assert.ElementsMatch(t, []string{"gauge", "counter", "histogram"}, keysOf(metric.Variants))
	for name, variant := range metric.Variants {
		help := findAttr(variant, "help")
		require.NotNil(t, help, "metric %q has no help attribute", name)
		assert.True(t, help.Required)
		assert.NotEmpty(t, variant.Summary, "metric %q has no summary", name)
		assert.False(t, variant.Undocumented)
	}
	assert.NotNil(t, findAttr(metric.Variants["histogram"], "buckets"))
}

// TestSchemaFsmSubBlocks covers blocks the parser reads by hand out of a
// `,remain` body, which reflection cannot see.
func TestSchemaFsmSubBlocks(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})
	fsm := doc.Blocks["fsm"].Body
	require.NotNil(t, fsm)

	assert.ElementsMatch(t, []string{"state", "event", "storage"}, keysOf(fsm.Blocks))

	state := fsm.Blocks["state"]
	require.NotNil(t, state)
	assert.Equal(t, []string{"name"}, state.Labels)
	assert.True(t, state.Repeatable)
	for _, hook := range []string{"on_init", "on_entry", "on_exit", "on_event"} {
		attr := findAttr(&state.SchemaBody, hook)
		require.NotNil(t, attr, "state.%s missing", hook)
		assert.Equal(t, config.HintActionExpression, attr.Hint)
	}

	// A reactive expression is neither config-time nor event-time: it
	// re-evaluates when what it references changes.
	when := findAttr(&fsm.Blocks["event"].SchemaBody, "when")
	require.NotNil(t, when)
	assert.Equal(t, config.HintReactiveExpression, when.Hint)
	assert.Empty(t, when.Context, "a reactive expression has no ctx")

	// Sub-blocks of a declared sub-block recurse.
	transition := fsm.Blocks["event"].Blocks["transition"]
	require.NotNil(t, transition)
	assert.Equal(t, []string{"from", "to"}, transition.Labels)
	assert.NotNil(t, findAttr(&transition.SchemaBody, "guard"))

	// storage keys are named by the config author.
	assert.True(t, fsm.Blocks["storage"].FreeAttributes)
	assert.Empty(t, fsm.Blocks["storage"].Attributes)
}

func TestSchemaFreeAttributeBlocks(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})

	constBlock := doc.Blocks["const"].Body
	require.NotNil(t, constBlock)
	assert.True(t, constBlock.FreeAttributes, "every const attribute is author-named")
	assert.Empty(t, constBlock.Attributes)

	// A var, by contrast, has a fixed set.
	varBlock := doc.Blocks["var"].Body
	require.NotNil(t, varBlock)
	assert.False(t, varBlock.FreeAttributes)
	assert.ElementsMatch(t, []string{"value", "type", "nullable"}, attrNamesOf(varBlock))
}

func TestSchemaServerVariants(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})
	server := doc.Blocks["server"]

	assert.NotEmpty(t, server.Summary, "the typed block itself needs a description")
	for name, variant := range server.Variants {
		assert.False(t, variant.Undocumented, "server %q is undocumented", name)
		assert.NotEmpty(t, variant.Summary, "server %q has no summary", name)
	}

	// The http server's nested blocks. Authentication is not among them: it is
	// a top-level `auth` block, referenced by an attribute.
	http := server.Variants["http"]
	assert.ElementsMatch(t,
		[]string{"tls", "baggage", "real_ip", "handle", "files"},
		keysOf(http.Blocks))
	handle := http.Blocks["handle"]
	require.NotNil(t, handle)
	assert.Equal(t, []string{"route"}, handle.Labels)
	assert.True(t, handle.Repeatable)
	assert.Len(t, handle.Constraints, 2, "action XOR handler, and at least one of them")

	// mcp recurses two levels: tool/prompt each contain param blocks.
	mcp := server.Variants["mcp"]
	for _, parent := range []string{"tool", "prompt"} {
		block := mcp.Blocks[parent]
		require.NotNil(t, block, parent)
		param := block.Blocks["param"]
		require.NotNil(t, param, "%s.param", parent)
		assert.Equal(t, []string{"name"}, param.Labels)
		assert.Equal(t, []string{"string", "number", "boolean"},
			findAttr(&param.SchemaBody, "type").Enum)
	}
	// Each handler kind gets its own ctx shape, even within one block.
	assert.Equal(t, "mcp-resource", findAttr(&mcp.Blocks["resource"].SchemaBody, "action").Context)
	assert.Equal(t, "mcp-tool", findAttr(&mcp.Blocks["tool"].SchemaBody, "action").Context)
	assert.Equal(t, "mcp-prompt", findAttr(&mcp.Blocks["prompt"].SchemaBody, "action").Context)
}

// TestSchemaSharedBlocks covers sub-block structs embedded by many parents:
// they are documented once and appear wherever they are used.
func TestSchemaSharedBlocks(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})
	servers := doc.Blocks["server"].Variants

	// tls is embedded by every server that owns a listener — same documentation
	// in each. A mounted-only server has no tls block, because it has no socket
	// to terminate TLS on.
	var tlsSummary string
	for _, name := range []string{"http", "metrics"} {
		tlsBlock := servers[name].Blocks["tls"]
		require.NotNil(t, tlsBlock, "server %q has no tls block", name)
		assert.NotEmpty(t, tlsBlock.Summary)
		assert.NotNil(t, findAttr(&tlsBlock.SchemaBody, "ca_cert"))
		if tlsSummary == "" {
			tlsSummary = tlsBlock.Summary
		}
		assert.Equal(t, tlsSummary, tlsBlock.Summary, "server %q describes tls differently", name)
	}

	// The auth attribute reaches nested blocks too, not just the top level of a
	// server, so a route can require something different from its server.
	for _, nested := range []string{"handle", "files"} {
		attr := findAttr(&servers["http"].Blocks[nested].SchemaBody, "auth")
		require.NotNil(t, attr, "server http %s has no auth attribute", nested)
		assert.NotEmpty(t, attr.Summary)
	}

	assert.NotEmpty(t, servers["http"].Blocks["baggage"].Summary)
}

func TestSchemaClientVariants(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})
	clients := doc.Blocks["client"]

	assert.NotEmpty(t, clients.Summary, "the typed block itself needs a description")

	// Every registered client type is documented, so a newly registered one
	// fails here until it is.
	_, problems := config.GenerateSchema(config.SchemaGenOptions{RequireDocs: true})
	for name, variant := range clients.Variants {
		assert.False(t, variant.Undocumented, "client %q is undocumented", name)
		assert.NotEmpty(t, variant.Summary, "client %q has no summary", name)
	}
	for _, problem := range problems {
		assert.False(t, strings.HasPrefix(problem.Error(), "client "),
			"a client attribute is undocumented: %v", problem)
	}
}

// TestSchemaTwoPassBodies covers a body decoded by more than one struct: a sql
// dialect captures the rest of the body with `,remain` and hands it to the
// shared connection/query struct, and the schema describes the union.
func TestSchemaTwoPassBodies(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})

	for _, dialect := range []string{"postgres", "mysql", "sqlite"} {
		variant := doc.Blocks["client"].Variants[dialect]
		require.NotNil(t, variant, "client %q missing", dialect)

		// From the shared struct...
		for _, attr := range []string{"max_open_conns", "conn_max_lifetime", "statement_timeout"} {
			assert.NotNil(t, findAttr(variant, attr), "%s is missing %q", dialect, attr)
		}
		query := variant.Blocks["query"]
		require.NotNil(t, query, "%s has no query block", dialect)
		assert.Equal(t, []string{"name"}, query.Labels)
		assert.Equal(t, []string{"one", "zero_or_one", "many", "exec"},
			findAttr(&query.SchemaBody, "cardinality").Enum)
	}

	// ...and from each dialect's own struct.
	assert.NotNil(t, findAttr(doc.Blocks["client"].Variants["postgres"], "sslmode"))
	assert.NotNil(t, findAttr(doc.Blocks["client"].Variants["sqlite"], "path"))
}

// TestSchemaDeliveryQuartet covers the subscriber/action/transforms/queue_size
// pattern shared by the subscription block and every client receiver: it is
// curated once and folded in wherever it appears.
func TestSchemaDeliveryQuartet(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})
	quartet := []string{"subscriber", "action", "transforms", "queue_size"}

	receivers := map[string]*config.SchemaNestedBlock{
		"mqtt":         doc.Blocks["client"].Variants["mqtt"].Blocks["receiver"],
		"kafka":        doc.Blocks["client"].Variants["kafka"].Blocks["receiver"],
		"rabbitmq":     doc.Blocks["client"].Variants["rabbitmq"].Blocks["receiver"],
		"redis_pubsub": doc.Blocks["client"].Variants["redis_pubsub"].Blocks["subscriber"],
		"redis_stream": doc.Blocks["client"].Variants["redis_stream"].Blocks["consumer"],
	}
	for name, receiver := range receivers {
		require.NotNil(t, receiver, "client %q has no receiver block", name)
		for _, attr := range quartet {
			meta := findAttr(&receiver.SchemaBody, attr)
			require.NotNil(t, meta, "client %q receiver is missing %q", name, attr)
			assert.NotEmpty(t, meta.Summary)
		}
		assert.Equal(t, config.HintSubscriberRef, findAttr(&receiver.SchemaBody, "subscriber").Hint)
		assert.Equal(t, config.HintTransformPipeline, findAttr(&receiver.SchemaBody, "transforms").Hint)
		assert.Len(t, receiver.Constraints, 2, "client %q receiver: action XOR subscriber", name)
	}

	// The subscription block uses the same wording for the same attributes.
	sub := doc.Blocks["subscription"].Body
	mqttReceiver := &receivers["mqtt"].SchemaBody
	for _, attr := range []string{"subscriber", "transforms", "queue_size"} {
		assert.Equal(t, findAttr(sub, attr).Summary, findAttr(mqttReceiver, attr).Summary,
			"%q is described differently in a subscription and a receiver", attr)
	}
}

func TestSchemaTriggerVariants(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})
	triggers := doc.Blocks["trigger"]

	assert.NotEmpty(t, triggers.Summary, "the typed block itself needs a description")
	for name, variant := range triggers.Variants {
		assert.False(t, variant.Undocumented, "trigger %q is undocumented", name)
		assert.NotEmpty(t, variant.Summary, "trigger %q has no summary", name)
	}

	// Every trigger fires an action, and every action is evaluated per firing
	// against a ctx shaped by that trigger type — never the same ctx twice.
	contexts := map[string]string{}
	for name, variant := range triggers.Variants {
		if name == "cron" || name == "signals" {
			continue // these carry their actions elsewhere; covered below
		}
		action := findAttr(variant, "action")
		require.NotNil(t, action, "trigger %q has no action", name)
		assert.Equal(t, config.HintActionExpression, action.Hint, "trigger %q action", name)
		require.NotEmpty(t, action.Context, "trigger %q action has no ctx", name)
		assert.NotContains(t, contexts, action.Context,
			"trigger %q reuses the ctx name of %q", name, contexts[action.Context])
		contexts[action.Context] = name
	}

	// A stop/skip predicate is a boolean gate, not a side-effecting action.
	for name, attr := range map[string]string{
		"at": "stop_when", "interval": "stop_when", "watchdog": "stop_when",
		"watch": "skip_when", "file": "skip_when",
	} {
		meta := findAttr(triggers.Variants[name], attr)
		require.NotNil(t, meta, "trigger %q has no %s", name, attr)
		assert.Equal(t, config.HintPredicateExpression, meta.Hint, "trigger %q %s", name, attr)
	}

	// cron is the one trigger holding many schedules: the action lives on each
	// `at` rule, whose two labels are the schedule and the rule name.
	cronAt := triggers.Variants["cron"].Blocks["at"]
	require.NotNil(t, cronAt)
	assert.Equal(t, []string{"schedule", "name"}, cronAt.Labels)
	assert.True(t, cronAt.Repeatable)
	assert.False(t, cronAt.Required, "a cron block with no rules parses; it just schedules nothing")
	assert.Equal(t, config.HintActionExpression, findAttr(&cronAt.SchemaBody, "action").Hint)
	assert.Nil(t, findAttr(triggers.Variants["cron"], "action"))

	// signals names its actions after the signals they handle.
	signals := triggers.Variants["signals"]
	for _, sig := range []string{"SIGHUP", "SIGINFO", "SIGUSR1", "SIGUSR2"} {
		attr := findAttr(signals, sig)
		require.NotNil(t, attr, "signals has no %s", sig)
		assert.Equal(t, config.HintActionExpression, attr.Hint)
	}

	// file is registered conditionally, so it is emitted as available-if rather
	// than omitted, and still fully documented.
	assert.True(t, triggers.Variants["file"].Conditional)
	assert.Equal(t, []string{"create", "write", "delete", "rename", "chmod"},
		findAttr(triggers.Variants["file"], "events").Enum)
}

func TestSchemaConditionSubtypes(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})
	conditions := doc.Blocks["condition"]

	assert.NotEmpty(t, conditions.Summary)
	assert.ElementsMatch(t, []string{"timer", "threshold", "counter", "flipflop"},
		keysOf(conditions.Variants))

	// The lifecycle hooks are curated once and are identical on every subtype.
	var hookSummary string
	for name, variant := range conditions.Variants {
		assert.NotEmpty(t, variant.Summary, "condition %q has no summary", name)
		for _, hook := range []string{"on_init", "on_activate", "on_deactivate"} {
			attr := findAttr(variant, hook)
			require.NotNil(t, attr, "condition %q has no %s", name, hook)
			assert.Equal(t, config.HintActionExpression, attr.Hint)
			assert.Equal(t, "condition-hook", attr.Context)
		}
		// inhibit is reactive everywhere: it re-evaluates when a watchable it
		// references changes, rather than at config load or per event.
		inhibit := findAttr(variant, "inhibit")
		require.NotNil(t, inhibit, "condition %q has no inhibit", name)
		assert.Equal(t, config.HintReactiveExpression, inhibit.Hint)
		assert.Empty(t, inhibit.Context, "a reactive expression has no ctx")

		summary := findAttr(variant, "on_activate").Summary
		if hookSummary == "" {
			hookSummary = summary
		}
		assert.Equal(t, hookSummary, summary, "condition %q describes on_activate differently", name)
	}

	// Threshold's two forms are exclusive and each is all-or-nothing.
	threshold := conditions.Variants["threshold"]
	assert.Equal(t, config.HintReactiveExpression, findAttr(threshold, "input").Hint)
	kinds := map[config.ConstraintKind][][]string{}
	for _, c := range threshold.Constraints {
		kinds[c.Kind] = append(kinds[c.Kind], c.Attributes)
	}
	assert.Contains(t, kinds[config.ConstraintRequiredTogether], []string{"on_above", "off_below"})
	assert.Contains(t, kinds[config.ConstraintRequiredTogether], []string{"on_below", "off_above"})
	assert.Contains(t, kinds[config.ConstraintMutuallyExclusive], []string{"on_above", "on_below"})

	// A counter declares input/debounce/retentive only so it can reject them
	// with a friendly message, so the schema says they are not supported
	// rather than pretending they work.
	counter := conditions.Variants["counter"]
	for _, attr := range []string{"input", "debounce", "retentive"} {
		meta := findAttr(counter, attr)
		require.NotNil(t, meta, "counter should still declare %q", attr)
		assert.Contains(t, meta.Summary, "Not supported", "counter %q", attr)
	}
	assert.True(t, findAttr(counter, "preset").Required)

	// A flipflop needs at least one wire, and a D input needs its gate.
	flipflop := conditions.Variants["flipflop"]
	for _, wire := range []string{"set_on", "reset_on", "toggle_on", "set_from", "gate_on"} {
		assert.Equal(t, config.HintReactiveExpression, findAttr(flipflop, wire).Hint, wire)
	}
	assert.Equal(t, []string{"rising", "falling", "both", "high", "low"},
		findAttr(flipflop, "gate_edge").Enum)
	assert.Equal(t, []string{"reset", "set"}, findAttr(flipflop, "dominant").Enum)
	ffKinds := map[config.ConstraintKind][][]string{}
	for _, c := range flipflop.Constraints {
		ffKinds[c.Kind] = append(ffKinds[c.Kind], c.Attributes)
	}
	assert.Equal(t, [][]string{{"set_on", "reset_on", "toggle_on", "set_from"}},
		ffKinds[config.ConstraintAtLeastOneOf])
	// Each edge attribute requires its wire — the parser rejects an orphaned
	// edge rather than ignoring it, so the schema can say so.
	assert.Equal(t, [][]string{
		{"set_from", "gate_on"},
		{"set_edge", "set_on"},
		{"reset_edge", "reset_on"},
		{"toggle_edge", "toggle_on"},
		{"gate_edge", "gate_on"},
	}, ffKinds[config.ConstraintRequires])
}

func TestSchemaEditorAndWireFormat(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})

	line := doc.Blocks["editor"].Variants["line"]
	require.NotNil(t, line)
	assert.False(t, line.Undocumented)
	assert.Equal(t, []string{"file", "string"}, findAttr(line, "mode").Enum)

	// params and variadic_param are decoded from the outer editor block, so
	// they are spliced into every editor type like any other envelope.
	assert.NotNil(t, findAttr(line, "params"))
	assert.NotNil(t, findAttr(line, "variadic_param"))

	match := line.Blocks["match"]
	require.NotNil(t, match)
	assert.True(t, match.Repeatable)
	assert.Equal(t, []string{"pattern"}, match.Labels,
		"an unnamed hcl label falls back to the field name")

	// Within one block, per-line expressions carry a ctx and config-time ones
	// do not — `when` is evaluated per matching line, `required` once at load.
	when := findAttr(&match.SchemaBody, "when")
	require.NotNil(t, when)
	assert.Equal(t, config.HintPredicateExpression, when.Hint)
	assert.Equal(t, "editor-match", when.Context)
	assert.Empty(t, findAttr(&match.SchemaBody, "required").Context)

	// before and after share a body, and are described identically.
	before, after := line.Blocks["before"], line.Blocks["after"]
	require.NotNil(t, before)
	require.NotNil(t, after)
	assert.False(t, before.Repeatable, "before appears at most once")
	assert.Equal(t,
		findAttr(&before.SchemaBody, "content").Summary,
		findAttr(&after.SchemaBody, "content").Summary)

	protobuf := doc.Blocks["wire_format"].Variants["protobuf"]
	require.NotNil(t, protobuf)
	assert.True(t, findAttr(protobuf, "descriptor_set").Required)
	assert.False(t, findAttr(protobuf, "message").Required,
		"omitting message exposes every message in the set")
	assert.Equal(t, []string{"native", "json"}, findAttr(protobuf, "mode").Enum)
}

// TestSchemaBlockLabelsAreNamed guards a defect the schema work surfaced:
// gohcl takes a nested block's label names straight from the `hcl` tag, and
// nearly every nested block used to leave the name empty. HCL then emitted
// "Missing  for match; All match blocks must have 1 labels ()", and the schema
// fell back to the lowercase field name — publishing labels like "kafkatopic".
//
// GenerateSchema reports an unnamed label as a problem, so this asserts none
// are left, then pins the names that were mangled before.
func TestSchemaBlockLabelsAreNamed(t *testing.T) {
	_, problems := config.GenerateSchema(config.SchemaGenOptions{})
	for _, problem := range problems {
		assert.NotContains(t, problem.Error(), "has no name in its hcl tag", "%v", problem)
	}

	doc := generateTestSchema(t, config.SchemaGenOptions{})
	clients := doc.Blocks["client"].Variants

	// A sender's `topic` label is a vinculum topic pattern; a receiver's
	// `subscription` label is the external topic it maps in from.
	for _, name := range []string{"mqtt", "kafka", "rabbitmq"} {
		assert.Equal(t, []string{"pattern"}, clients[name].Blocks["sender"].Blocks["topic"].Labels,
			"client %q sender topic", name)
	}
	assert.Equal(t, []string{"mqtt_topic"},
		clients["mqtt"].Blocks["receiver"].Blocks["subscription"].Labels)
	assert.Equal(t, []string{"kafka_topic"},
		clients["kafka"].Blocks["receiver"].Blocks["subscription"].Labels)
	assert.Equal(t, []string{"routing_key_pattern"},
		clients["rabbitmq"].Blocks["receiver"].Blocks["subscription"].Labels)
	assert.Equal(t, []string{"routing_key"},
		clients["rabbitmq"].Blocks["receiver"].Blocks["binding"].Labels)
}

// TestSchemaContexts covers the `ctx` shapes. An attribute's `context` is only
// a label; without the shape behind it a completion provider cannot see inside
// an `action =` at all.
func TestSchemaContexts(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})
	require.NotEmpty(t, doc.Contexts)

	// Both halves of the closure the generator checks: every name has a shape,
	// and every shape is named. generateTestSchema already fails on the
	// problems those raise; this asserts the emitted document agrees.
	named := map[string]bool{}
	forEachAttr(doc, func(_ string, attr *config.SchemaAttr) {
		if attr.Context != "" {
			named[attr.Context] = true
		}
	})
	require.NotEmpty(t, named)
	assert.ElementsMatch(t, keysOf(named), keysOf(doc.Contexts))

	for name, shape := range doc.Contexts {
		assert.NotEmpty(t, shape.Summary, "context %q has no summary", name)
		for _, f := range shape.Fields {
			assert.NotEmpty(t, f.Name, "context %q has an unnamed field", name)
			assert.NotEmpty(t, f.Type, "context %s.%s has no type", name, f.Name)
			assert.NotEmpty(t, f.Summary, "context %s.%s has no summary", name, f.Name)
		}
	}

	// The shape a subscription action and every client receiver share.
	msg := doc.Contexts["message"]
	require.NotNil(t, msg)
	assert.Equal(t, []string{"topic", "msg", "fields", "auth", "baggage", "trace_id", "span_id"},
		contextFieldNames(msg))

	// A shape may legitimately have no fields of its own: on_connect fires
	// with no message in flight.
	assert.Equal(t, []string{"auth", "baggage", "trace_id", "span_id"},
		contextFieldNames(doc.Contexts["connection"]))

	// Every shape carries the universal fields, editors included: an editor is
	// called from a handler that is inside a live trace, and used to be unable
	// to see it.
	for name, shape := range doc.Contexts {
		var universal []string
		for _, f := range shape.Fields {
			if f.Universal {
				universal = append(universal, f.Name)
			}
		}
		assert.Equal(t, []string{"auth", "baggage", "trace_id", "span_id"}, universal,
			"context %q", name)
	}
	assert.Equal(t, []string{"filename", "auth", "baggage", "trace_id", "span_id"},
		contextFieldNames(doc.Contexts["editor-content"]))

	// Optional marks a field absent from some evaluations of the same shape:
	// on_init reports a starting state, not a transition, so it has no
	// old_value.
	oldValue := contextField(doc.Contexts["condition-hook"], "old_value")
	require.NotNil(t, oldValue)
	assert.True(t, oldValue.Optional)
	assert.False(t, contextField(doc.Contexts["condition-hook"], "new_value").Optional)

	// Every trigger type has its own shape, and no two are the same — that is
	// why the name is per-attribute rather than per-block.
	for _, name := range []string{
		"trigger-after", "trigger-at", "trigger-cron", "trigger-file",
		"trigger-interval", "trigger-once", "trigger-shutdown", "trigger-signals",
		"trigger-start", "trigger-watch", "trigger-watchdog",
	} {
		assert.NotNil(t, doc.Contexts[name], "trigger context %q missing", name)
	}
	// trigger "cron" identifies the rule that fired, not the block, so unlike
	// every other trigger it has no ctx.trigger or ctx.name.
	assert.Equal(t, []string{"cron_name", "at_name", "auth", "baggage", "trace_id", "span_id"},
		contextFieldNames(doc.Contexts["trigger-cron"]))
}

// TestSchemaOpenContextFields covers the two shapes whose fields are a floor
// rather than the whole list. Both are per-receiver: every on_decode_error
// carries the same five fields and every vinculum_topic the same two, and then
// each adds the identity of its own transport — so a consumer completing inside
// one gets ctx.routing_key on rabbitmq and ctx.mqtt_topic on mqtt, and neither
// on the other.
//
// The two are checked together because the point is that one receiver's two
// hooks agree: a redis stream names the stream `stream` in both, having once
// called it `topic` in one of them.
func TestSchemaOpenContextFields(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{RequireDocs: true})

	decodeError := doc.Contexts["decode-error"]
	require.NotNil(t, decodeError)
	assert.True(t, decodeError.OpenFields, "decode-error must say its field list is open")
	assert.Equal(t, []string{"raw", "error", "wire_format", "topic", "fields",
		"auth", "baggage", "trace_id", "span_id"}, contextFieldNames(decodeError))

	inbound := doc.Contexts["inbound-message"]
	require.NotNil(t, inbound)
	assert.True(t, inbound.OpenFields, "inbound-message must say its field list is open")
	// No `topic`: there is no bus topic in scope, and naming the transport's
	// identifier after one is the mistake this shape exists to have fixed.
	assert.Equal(t, []string{"msg", "fields",
		"auth", "baggage", "trace_id", "span_id"}, contextFieldNames(inbound))

	// Only an open shape may be added to, so no other shape carries additions.
	sites := map[string]map[string][]string{}
	forEachAttr(doc, func(path string, attr *config.SchemaAttr) {
		if len(attr.ContextFields) == 0 {
			return
		}
		if !assert.Contains(t, []string{"decode-error", "inbound-message"}, attr.Context,
			"%s adds fields to a shape that is not open", path) {
			return
		}
		var names []string
		for _, f := range attr.ContextFields {
			assert.NotEmpty(t, f.Type, "%s: context field %q has no type", path, f.Name)
			assert.NotEmpty(t, f.Summary, "%s: context field %q has no summary", path, f.Name)
			names = append(names, f.Name)
		}
		if sites[attr.Context] == nil {
			sites[attr.Context] = map[string][]string{}
		}
		sites[attr.Context][path] = names
	})

	// Every receiver that accepts on_decode_error declares what it adds, so a
	// new one cannot quietly inherit the bare five.
	assert.Equal(t, map[string][]string{
		"client kafka.receiver.on_decode_error":          {"kafka_topic", "partition", "offset", "key"},
		"client mqtt.receiver.on_decode_error":           {"mqtt_topic"},
		"client rabbitmq.receiver.on_decode_error":       {"routing_key", "exchange", "queue"},
		"client redis_pubsub.subscriber.on_decode_error": {"channel", "matched_pattern"},
		"client redis_stream.consumer.on_decode_error":   {"stream", "entry_id", "group", "consumer"},
		"client sqs_receiver.on_decode_error":            {"queue", "message_id"},
	}, sites["decode-error"])

	// And the same for the topic expression, where every name is the
	// transport's own — the two lists above and below say `stream`, `channel`,
	// `mqtt_topic`, `routing_key` in both halves.
	assert.Equal(t, map[string][]string{
		"client kafka.receiver.subscription.vinculum_topic":                  {"kafka_topic", "key"},
		"client mqtt.receiver.subscription.vinculum_topic":                   {"mqtt_topic"},
		"client rabbitmq.receiver.subscription.vinculum_topic":               {"routing_key", "exchange"},
		"client redis_pubsub.subscriber.channel_subscription.vinculum_topic": {"channel"},
		"client redis_stream.consumer.vinculum_topic":                        {"stream", "entry_id"},
		"client sqs_receiver.vinculum_topic":                                 {"queue", "message_id"},
	}, sites["inbound-message"])
}

func contextFieldNames(shape *config.SchemaContext) []string {
	if shape == nil {
		return nil
	}
	names := make([]string, 0, len(shape.Fields))
	for _, f := range shape.Fields {
		names = append(names, f.Name)
	}
	return names
}

func contextField(shape *config.SchemaContext, name string) *config.SchemaContextField {
	for _, f := range shape.Fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// forEachAttr visits every attribute in the document, at any depth.
func forEachAttr(doc *config.SchemaDocument, fn func(path string, attr *config.SchemaAttr)) {
	var walk func(path string, body *config.SchemaBody)
	walk = func(path string, body *config.SchemaBody) {
		for _, attr := range body.Attributes {
			fn(path+"."+attr.Name, attr)
		}
		for name, nested := range body.Blocks {
			walk(path+"."+name, &nested.SchemaBody)
		}
	}
	for blockType, block := range doc.Blocks {
		if block.Body != nil {
			walk(blockType, block.Body)
		}
		for variant, body := range block.Variants {
			walk(blockType+" "+variant, body)
		}
	}
}

// TestSchemaBackendHints covers the two attributes that name a backend: they
// want a specific kind of block, not any client or any server.
func TestSchemaBackendHints(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})

	for _, body := range []*config.SchemaBody{
		doc.Blocks["bus"].Body,
		doc.Blocks["client"].Variants["mqtt"],
		doc.Blocks["client"].Variants["kafka"],
		doc.Blocks["server"].Variants["http"],
	} {
		if metrics := findAttr(body, "metrics"); metrics != nil {
			assert.Equal(t, config.HintMetricsRef, metrics.Hint)
		}
		if tracing := findAttr(body, "tracing"); tracing != nil {
			assert.Equal(t, config.HintTracingRef, tracing.Hint)
		}
	}
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

// A consumer that reads one kind of file wants the document for that language:
// its blocks, the `ctx` shapes those blocks name, and the namespaces their
// expressions may start from. Carrying the rest would describe the generator
// rather than the language being validated.
func TestSchemaCommandFileKind(t *testing.T) {
	out, err := runSchemaCommand(t, "--file-kind", "vinit")
	require.NoError(t, err)

	var vinit struct {
		Blocks     map[string]struct{ File string } `json:"blocks"`
		Contexts   map[string]any                   `json:"contexts"`
		Namespaces map[string]any                   `json:"namespaces"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &vinit))
	assert.Equal(t, []string{"git", "plugin"}, sortedKeysOf(vinit.Blocks))
	assert.Empty(t, vinit.Contexts, "a .vinit attribute may not name a ctx shape")
	// `env` is the whole of the .vinit namespace, read from the eval context
	// that file kind is evaluated against.
	assert.Equal(t, []string{"env"}, sortedKeysOf(vinit.Namespaces))

	out, err = runSchemaCommand(t, "--file-kind", "vcl")
	require.NoError(t, err)

	var vcl struct {
		Blocks     map[string]struct{ File string } `json:"blocks"`
		Contexts   map[string]any                   `json:"contexts"`
		Namespaces map[string]any                   `json:"namespaces"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &vcl))
	assert.NotContains(t, vcl.Blocks, "git")
	assert.Contains(t, vcl.Blocks, "subscription")
	for name, block := range vcl.Blocks {
		assert.Equal(t, string(config.FileVCL), block.File, name)
	}
	assert.NotEmpty(t, vcl.Contexts, "the .vcl blocks name shapes")
	assert.Contains(t, vcl.Namespaces, "bus")

	// Unfiltered is both, which is the default a consumer of the released
	// schema.json gets.
	out, err = runSchemaCommand(t)
	require.NoError(t, err)
	var both struct {
		Blocks map[string]any `json:"blocks"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &both))
	assert.Contains(t, both.Blocks, "git")
	assert.Contains(t, both.Blocks, "subscription")
}

func sortedKeysOf[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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

	// Loading plugins needs both halves. Either alone would silently produce a
	// stock-binary document, which is not what the user asked for.
	_, err = runSchemaCommand(t, "--plugin-path", t.TempDir())
	require.Error(t, err)
	assert.Equal(t, 2, ExitCode(err), "--plugin-path without config paths is a usage error")
	assert.Contains(t, err.Error(), "config paths")

	_, err = runSchemaCommand(t, t.TempDir())
	require.Error(t, err)
	assert.Equal(t, 2, ExitCode(err), "config paths without --plugin-path is a usage error")
	assert.Contains(t, err.Error(), "--plugin-path")

	_, err = runSchemaCommand(t, "--file-kind", "vinculum")
	require.Error(t, err)
	assert.Equal(t, 2, ExitCode(err), "--file-kind takes vcl or vinit")

	// A region names a topic in the whole language, so rendering doc/ from half
	// a document would blank every region describing the other half.
	_, err = runSchemaCommand(t, "--file-kind", "vcl", "--format", "markdown", "--check", t.TempDir())
	require.Error(t, err)
	assert.Equal(t, 2, ExitCode(err), "--file-kind with --check is a usage error")
}

// TestSchemaCommandOmitsPluginsWhenNoneLoaded pins the signal a consumer reads
// to tell a stock-binary document from one that describes plugin types too.
func TestSchemaCommandOmitsPluginsWhenNoneLoaded(t *testing.T) {
	out, err := runSchemaCommand(t)
	require.NoError(t, err)

	// The top-level key specifically: "plugins" also names a member of the sys
	// namespace, so a substring check would find that instead.
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(out), &top))
	assert.NotContains(t, top, "plugins")
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
	// runs, so restore their declared defaults before each one. The array
	// flags matter most: cobra appends to whatever is already there, so a
	// leftover value would silently join the next run's.
	schemaFormat, schemaPretty, schemaOutput = "json", true, ""
	schemaFileKind = ""
	schemaStrict, schemaRequireDocs = false, false
	schemaUpdate, schemaCheck = nil, nil
	pluginPath = ""

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

func attrNamesOf(body *config.SchemaBody) []string {
	names := make([]string, len(body.Attributes))
	for i, attr := range body.Attributes {
		names[i] = attr.Name
	}
	return names
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
