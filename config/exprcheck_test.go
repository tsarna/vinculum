package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// buildRefCheckVCL builds a config and returns the joined diagnostics text,
// empty when the config was accepted.
func buildRefCheckVCL(t *testing.T, src string) string {
	t.Helper()
	_, diags := NewConfig().
		WithSources([]byte(src)).
		WithLogger(zap.NewNop()).
		Build()
	if !diags.HasErrors() {
		return ""
	}
	return diags.Error()
}

// subscriptionVCL wraps an action expression in the smallest config that gives
// it a `message` context to be evaluated against.
func subscriptionVCL(action string) string {
	return `
bus "main" {}
var "count" { value = 0 }
const { prefix = "out/" }
subscription "s" {
  target = bus.main
  topics = ["in/#"]
  action = ` + action + `
}
`
}

// A subscription action is evaluated per message, so nothing evaluates it while
// the config is being loaded. Before this check, a name that resolves to
// nothing was reported only by the first message to arrive, and then by every
// message after it.
func TestDeferredReferenceUnknownRoot(t *testing.T) {
	err := buildRefCheckVCL(t, subscriptionVCL(`log::info(totally_bogus.thing)`))

	require.NotEmpty(t, err, "an unresolvable reference in an action must fail the load")
	assert.Contains(t, err, `Unknown reference "totally_bogus"`)
	// The message names what is in scope, since the whole difficulty is that
	// the author cannot see the namespace from where they are standing.
	assert.Contains(t, err, "In scope here:")
	assert.Contains(t, err, "prefix")
}

// The namespaces whose members come from config blocks can be checked a level
// deeper: a misspelled bus is a mistake in a way that a missing environment
// variable is not.
func TestDeferredReferenceUnknownNamespaceMember(t *testing.T) {
	err := buildRefCheckVCL(t, subscriptionVCL(`send(ctx, bus.mian, "out", ctx.msg)`))

	require.NotEmpty(t, err)
	assert.Contains(t, err, `No bus named "mian"`)
	assert.Contains(t, err, "Declared bus names are: main")
}

// A const is settled by the time the check runs and cannot change afterwards,
// so reading a name out of an object-valued one is as knowable as reading a
// name out of a namespace.
func TestDeferredReferenceUnknownConstAttribute(t *testing.T) {
	src := `
bus "main" {}
const { routing = { alpha = "a/#", beta = "b/#" } }
subscription "s" {
  target = bus.main
  topics = ["in/#"]
  action = log::info(routing.gamma)
}
`
	err := buildRefCheckVCL(t, src)

	require.NotEmpty(t, err)
	assert.Contains(t, err, `routing has no attribute "gamma"`)
	assert.Contains(t, err, "The const routing provides: alpha, beta.")
}

// Neither guard on that check may be dropped: a const reached into dynamically
// names nothing to check, and a map-typed one has no fixed attribute set at all.
func TestDeferredReferenceAcceptsDynamicAndMapConsts(t *testing.T) {
	src := `
bus "main" {}
const {
  routing = { alpha = "a/#" }
  kinds   = tomap({ x = "1" })
}
subscription "s" {
  target = bus.main
  topics = ["in/#"]
  action = [log::info(routing[ctx.topic]), log::info(kinds.anything)]
}
`
	assert.Empty(t, buildRefCheckVCL(t, src))
}

// A namespace is checked exactly as far as the schema says the language chooses
// the names, which is the whole point of describing it there.
//
// `env` is the environment of whichever process is running, so a `vinculum
// check` on a build machine must not report a variable that only the deployment
// sets as missing. `sys.signals` is the same problem a level down, since which
// signals exist is the host OS's business. Everything else is fixed, and a
// misspelling of it will fail at every event for the life of the process.
func TestDeferredReferenceAcceptsFreeAmbientMembers(t *testing.T) {
	src := `
bus "main" {}
subscription "s" {
  target = bus.main
  topics = ["in/#"]
  action = [
    log::info(env.NOT_SET_ANYWHERE),
    log::info(sys.hostname),
    log::info(sys.functy.version),
    log::info(sys.signals.SIGUSR1),
    log::info(sys.signals.bynumber["9"]),
    log::info(http_status.NotFound),
    log::info(http_status.bycode["404"]),
  ]
}
`
	assert.Empty(t, buildRefCheckVCL(t, src))
}

func TestDeferredReferenceUnknownAmbientMember(t *testing.T) {
	err := buildRefCheckVCL(t, subscriptionVCL(`log::info(sys.hostnam)`))

	require.NotEmpty(t, err)
	assert.Contains(t, err, `sys has no member "hostnam"`)
	// Twenty-six names would bury the summary, so the detail points at the page
	// that lists them.
	assert.Contains(t, err, "run `vinculum man sys`")
}

// The check follows the dots as far as the schema describes objects, so a
// member of a member is checked too — and named in full, since `versionx` alone
// would not say where it was read.
func TestDeferredReferenceUnknownNestedAmbientMember(t *testing.T) {
	err := buildRefCheckVCL(t, subscriptionVCL(`log::info(sys.functy.versionx)`))

	require.NotEmpty(t, err)
	assert.Contains(t, err, `sys.functy has no member "versionx"`)
	assert.Contains(t, err, "sys.functy provides: version.")
}

// A namespace short enough to list is listed, rather than sending the reader to
// another command for three names.
func TestDeferredReferenceUnknownMemberListsAShortNamespace(t *testing.T) {
	withCleanAmbientProviders(t)
	ambientProviders = append(ambientProviders, ambientEntry{
		name: "fixture",
		p: func(*Config) cty.Value {
			return cty.ObjectVal(map[string]cty.Value{"alpha": cty.True, "beta": cty.True})
		},
		schema: &NamespaceSchema{
			Summary: "A fixture.",
			Members: map[string]MemberMeta{"alpha": {Summary: "A."}, "beta": {Summary: "B."}},
		},
	})

	err := buildRefCheckVCL(t, subscriptionVCL(`log::info(fixture.gamma)`))

	require.NotEmpty(t, err)
	assert.Contains(t, err, `fixture has no member "gamma"`)
	assert.Contains(t, err, "fixture provides: alpha, beta.")
}

// A provider registered without a schema describes no members, which is not the
// same as describing none — checking against an empty list would report every
// reference a plugin's namespace makes.
func TestDeferredReferenceAcceptsAnUndocumentedNamespace(t *testing.T) {
	withCleanAmbientProviders(t)
	ambientProviders = append(ambientProviders, ambientEntry{
		name: "fixture",
		p: func(*Config) cty.Value {
			return cty.ObjectVal(map[string]cty.Value{"alpha": cty.True})
		},
	})

	assert.Empty(t, buildRefCheckVCL(t, subscriptionVCL(`log::info(fixture.anything)`)))
}

func TestDeferredReferenceUnknownVar(t *testing.T) {
	err := buildRefCheckVCL(t, subscriptionVCL(`increment(var.conut)`))

	require.NotEmpty(t, err)
	assert.Contains(t, err, `No var named "conut"`)
	assert.Contains(t, err, "count")
}

// The shape of `ctx` is a property of the attribute, not of the block, so the
// check reads it from the attribute's declared context.
func TestDeferredReferenceUnknownCtxField(t *testing.T) {
	err := buildRefCheckVCL(t, subscriptionVCL(`log::info(ctx.mesage)`))

	require.NotEmpty(t, err)
	assert.Contains(t, err, `Unknown ctx field "mesage"`)
	assert.Contains(t, err, `"message" context`)
	// Fields are offered in the order the shape declares them, so the ones
	// worth reading come before the universal ones.
	// The site's own additions come after the universal fields, the same way
	// they do for a receiver's on_decode_error.
	assert.Contains(t, err, "It provides: topic, msg, fields, auth, baggage, trace_id, span_id, undeliverable_topic.")
}

// try() and can() exist to reference something that may not be there, so a
// traversal beneath one is not a mistake. Reporting these would have made the
// check unusable for exactly the configs that were careful.
func TestDeferredReferenceAllowsTryAndCan(t *testing.T) {
	src := subscriptionVCL(`[
    log::info(try(ctx.not_a_field, "absent")),
    log::info(try(nothing_at_all.here, "absent")),
    can(bus.nope),
  ]`)

	assert.Empty(t, buildRefCheckVCL(t, src))
}

// A `for` expression's iterator is a local name, not a root-scope reference.
func TestDeferredReferenceAllowsForIterator(t *testing.T) {
	src := subscriptionVCL(`[for part in ["a", "b"] : "${prefix}${part}/${ctx.topic}"]`)

	assert.Empty(t, buildRefCheckVCL(t, src))
}

// Nothing is created from a disabled block, so its expressions are never
// evaluated — and a disabled block is the documented way to park config that
// refers to names the rest of the file no longer publishes.
func TestDeferredReferenceSkipsDisabledBlock(t *testing.T) {
	src := `
bus "main" {}
subscription "parked" {
  disabled = true
  target   = bus.main
  topics   = ["in/#"]
  action   = log::info(ctx.who_knows, gone.away)
}
`
	assert.Empty(t, buildRefCheckVCL(t, src))
}

// The check must not fire on the references a working config makes, which is
// the failure mode that would matter: every name below resolves.
func TestDeferredReferenceAcceptsValidReferences(t *testing.T) {
	src := subscriptionVCL(`[
    log::info("${prefix}${ctx.topic}", ctx.fields),
    set(var.count, length(ctx.msg)),
    send(ctx, bus.main, "out/${ctx.topic}", ctx.msg),
    ctx.trace_id,
  ]`)

	assert.Empty(t, buildRefCheckVCL(t, src))
}

// `ctx` passed whole — to send(), to http::get() — is not an attribute access
// and has nothing to check.
func TestDeferredReferenceAcceptsBareCtx(t *testing.T) {
	assert.Empty(t, buildRefCheckVCL(t, subscriptionVCL(`send(ctx, bus.main, "out", "x")`)))
}

// An index step says nothing about a name, so the traversal stops there rather
// than guessing.
func TestDeferredReferenceIgnoresIndexSteps(t *testing.T) {
	assert.Empty(t, buildRefCheckVCL(t, subscriptionVCL(`log::info(ctx.fields["anything"])`)))
}

// An attribute evaluated while the config loads is already checked by being
// evaluated; the deferred check must not double up on it, and in particular
// must not treat a `topics` list or a `transforms` pipeline as an action.
func TestDeferredReferenceLeavesLoadTimeAttributesAlone(t *testing.T) {
	src := `
bus "main" {}
subscription "s" {
  target     = bus.main
  topics     = ["in/#"]
  transforms = [add_topic_prefix("out/")]
  action     = log::info(ctx.topic)
}
`
	assert.Empty(t, buildRefCheckVCL(t, src))
}

// A misspelled function in an action used to be found by the first message to
// arrive, and then by every message after it: `const { x = nosuchfunc() }`
// failed the load, but `action = nosuchfunc()` did not.
func TestDeferredReferenceUnknownFunction(t *testing.T) {
	err := buildRefCheckVCL(t, subscriptionVCL(`totally_bogus_function(ctx.msg)`))

	require.NotEmpty(t, err, "a call to a function that does not exist must fail the load")
	assert.Contains(t, err, "Call to unknown function")
	assert.Contains(t, err, `There is no function named "totally_bogus_function".`)
}

// The wording is hcl's own, so the load-time report and the one the first event
// would have produced are the same sentence.
func TestDeferredReferenceUnknownFunctionSuggests(t *testing.T) {
	err := buildRefCheckVCL(t, subscriptionVCL(`lenght(ctx.msg)`))

	assert.Contains(t, err, `There is no function named "lenght". Did you mean "length"?`)
}

// A namespaced name is reported against its own namespace, and the suggestion
// is qualified — hcl's runtime one compares the bare name against qualified
// candidates and so almost never offers anything.
func TestDeferredReferenceUnknownFunctionInNamespace(t *testing.T) {
	err := buildRefCheckVCL(t, subscriptionVCL(`log::inf("hi")`))

	assert.Contains(t, err, `There is no function named "inf" in namespace log::.`)
	assert.Contains(t, err, `Did you mean log::info?`)
}

// A misspelled namespace is a different mistake, and listing the names in a
// namespace that does not exist would be nonsense.
func TestDeferredReferenceUnknownFunctionNamespace(t *testing.T) {
	err := buildRefCheckVCL(t, subscriptionVCL(`lgo::info("hi")`))

	assert.Contains(t, err, `There are no functions in namespace "lgo::".`)
}

// try() catches the diagnostic an unknown function raises just as it catches an
// unresolvable reference, so a call beneath one is not a mistake — but the
// try() itself is an ordinary name that can be misspelled.
func TestDeferredReferenceAllowsUnknownFunctionUnderTry(t *testing.T) {
	assert.Empty(t, buildRefCheckVCL(t, subscriptionVCL(`try(who_knows(ctx.msg), "absent")`)))

	err := buildRefCheckVCL(t, subscriptionVCL(`tyr(ctx.msg, "absent")`))
	assert.Contains(t, err, `There is no function named "tyr". Did you mean "try"?`)
}

// Which functions this process has is partly a property of how it was launched:
// file() needs --file-path, kill() needs --allow-kill, and `vinculum check` has
// no --allow-kill to be given. Reporting those would fail a config that runs.
func TestDeferredReferenceAcceptsFeatureGatedFunctions(t *testing.T) {
	src := subscriptionVCL(`[
    log::info(file("greeting.txt")),
    log::info(templatefile("t.tmpl", {})),
    filewrite("out.txt", ctx.msg),
    kill(1, 15),
  ]`)

	assert.Empty(t, buildRefCheckVCL(t, src))
}

// A user-defined function is callable by the same name as any other, including
// from a namespace declared in a .cty file.
func TestDeferredReferenceAcceptsUserFunctions(t *testing.T) {
	src := `
bus "main" {}
function "shout" {
  params = [s]
  result = upper(s)
}
jq "field" {
  query = ".field"
}
subscription "s" {
  target = bus.main
  topics = ["in/#"]
  action = log::info(shout(ctx.topic), field(ctx.msg))
}
`
	assert.Empty(t, buildRefCheckVCL(t, src))
}

// A call inside a string template is a call like any other.
func TestDeferredReferenceChecksFunctionsInTemplates(t *testing.T) {
	err := buildRefCheckVCL(t, subscriptionVCL(`"out/${uppercase(ctx.topic)}"`))

	assert.Contains(t, err, `There is no function named "uppercase".`)
}
