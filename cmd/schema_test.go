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

// curatedBlocks are the top-level blocks whose documentation is complete.
// Phases of the schema rollout add to this list; a block named here must stay
// fully documented, so adding an hcl field without an AttrMeta fails the test.
var curatedBlocks = []string{
	"assert", "bus", "client", "condition", "const", "editor", "fsm",
	"function", "jq", "metric", "server", "subscription", "trigger", "var",
	"wire_format",
}

func TestSchemaCuratedBlocksAreFullyDocumented(t *testing.T) {
	_, problems := config.GenerateSchema(config.SchemaGenOptions{RequireDocs: true})

	for _, problem := range problems {
		for _, block := range curatedBlocks {
			assert.False(t, problemConcerns(problem, block),
				"%s is documented; fix or document the new field: %v", block, problem)
		}
	}

	doc := generateTestSchema(t, config.SchemaGenOptions{})
	for _, name := range curatedBlocks {
		block := doc.Blocks[name]
		require.NotNil(t, block, name)
		assert.False(t, block.Undocumented, "%s should not be flagged undocumented", name)
		assert.NotEmpty(t, block.Summary, "%s has no summary", name)
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

	// The http server's nested blocks, including two levels of auth.
	http := server.Variants["http"]
	assert.ElementsMatch(t,
		[]string{"tls", "auth", "baggage", "real_ip", "handle", "files"},
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

	// tls is embedded by http, mcp, and metrics — same documentation in each.
	var tlsSummary string
	for _, name := range []string{"http", "mcp", "metrics"} {
		tlsBlock := servers[name].Blocks["tls"]
		require.NotNil(t, tlsBlock, "server %q has no tls block", name)
		assert.NotEmpty(t, tlsBlock.Summary)
		assert.NotNil(t, findAttr(&tlsBlock.SchemaBody, "ca_cert"))
		if tlsSummary == "" {
			tlsSummary = tlsBlock.Summary
		}
		assert.Equal(t, tlsSummary, tlsBlock.Summary, "server %q describes tls differently", name)
	}

	// auth reaches nested blocks too, not just the top level of a server.
	handlerAuth := servers["http"].Blocks["handle"].Blocks["auth"]
	require.NotNil(t, handlerAuth)
	assert.Equal(t, []string{"mode"}, handlerAuth.Labels,
		"an unnamed hcl label falls back to the field name")
	assert.NotEmpty(t, handlerAuth.Summary)

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
	ffKinds := map[config.ConstraintKind][]string{}
	for _, c := range flipflop.Constraints {
		ffKinds[c.Kind] = c.Attributes
	}
	assert.Equal(t, []string{"set_on", "reset_on", "toggle_on", "set_from"},
		ffKinds[config.ConstraintAtLeastOneOf])
	assert.Equal(t, []string{"set_from", "gate_on"}, ffKinds[config.ConstraintRequires])
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

// problemConcerns reports whether a schema problem is about the given
// top-level block. Problem paths are dotted ("bus.queue_size"), and a typed
// block's variant paths start "<block> <variant>" ("metric gauge.help").
func problemConcerns(problem error, blockType string) bool {
	msg := problem.Error()
	for _, sep := range []string{":", ".", " "} {
		if strings.HasPrefix(msg, blockType+sep) {
			return true
		}
	}
	return false
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
