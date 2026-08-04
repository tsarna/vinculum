package config

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/functy"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// helpTestConfig builds a config with no sources of its own: every built-in
// function, every extern the linked libraries register, and no user code.
func helpTestConfig(t *testing.T) *Config {
	t.Helper()
	c, diags := NewConfig().WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), "%s", diags)
	require.NotNil(t, c)
	return c
}

func callHelp(t *testing.T, c *Config, args ...string) cty.Value {
	t.Helper()
	fn, ok := c.EvalCtx().Functions["help"]
	require.True(t, ok, "help must be in the eval context")

	vals := make([]cty.Value, 0, len(args))
	for _, a := range args {
		vals = append(vals, cty.StringVal(a))
	}
	got, err := fn.Call(vals)
	require.NoError(t, err)
	return got
}

// greedyResolver answers every topic, so a test can prove that something is
// *not* being routed to the resolver. A resolver that answered nothing would
// make the delegation test pass for the wrong reason.
type greedyResolver struct{ lastKind string }

func (greedyResolver) HelpKinds() []string { return []string{"block", "context"} }

func (g *greedyResolver) HelpTopic(kind string, path []string) (string, bool) {
	g.lastKind = kind
	return "RESOLVER:" + kind + ":" + path[0], true
}

// withResolver installs a resolver for one test and restores the previous one,
// since it is process-global and schemadoc registers the real one from init().
func withResolver(t *testing.T, r HelpTopicResolver) {
	t.Helper()
	prev := helpTopics
	helpTopics = r
	t.Cleanup(func() { helpTopics = prev })
}

// The contract Vinculum's help() must never break: for every name that is a
// function, it answers exactly what functy's help() answers.
//
// A greedy resolver is installed for the duration, so any name that leaked to
// the resolver fails loudly rather than silently returning something plausible.
// The test is near-tautological while help() merely delegates — which is the
// point of writing it now. It is the contract phase 4's re-implementation has
// to satisfy.
func TestHelpIsByteIdenticalToFunctyForEveryFunction(t *testing.T) {
	c := helpTestConfig(t)
	withResolver(t, &greedyResolver{})

	evalCtxFn := func() *hcl.EvalContext { return c.evalCtx }
	delegate := functy.HelpFunc(c.functyResult(), evalCtxFn)

	names := c.FuncNames()
	require.NotEmpty(t, names)
	checked := 0

	for _, name := range names {
		want, err := delegate.Call([]cty.Value{cty.StringVal(name)})
		require.NoError(t, err, name)
		if want.IsNull() {
			continue // functy declines to describe it; nothing to hold identical
		}

		got := callHelp(t, c, name)
		require.False(t, got.IsNull(), "help(%q) went null where functy answered", name)
		assert.Equal(t, want.AsString(), got.AsString(), "help(%q) diverged from functy", name)
		checked++
	}
	t.Logf("%d function names held identical", checked)
	assert.Greater(t, checked, 100, "the eval context should carry a lot of functions")
}

// The no-argument directory is functy's, verbatim: it lists functions, and
// changing its wording would break every script that parses it.
func TestHelpWithNoArgumentsIsFunctys(t *testing.T) {
	c := helpTestConfig(t)
	withResolver(t, &greedyResolver{})

	evalCtxFn := func() *hcl.EvalContext { return c.evalCtx }
	want, err := functy.HelpFunc(c.functyResult(), evalCtxFn).Call(nil)
	require.NoError(t, err)

	assert.Equal(t, want.AsString(), callHelp(t, c).AsString())
}

func TestHelpFallsThroughToTheResolver(t *testing.T) {
	c := helpTestConfig(t)
	withResolver(t, &greedyResolver{})

	// Not a function, so the resolver sees it.
	assert.Equal(t, "RESOLVER::subscription", callHelp(t, c, "subscription").AsString())
}

// A path of more than one word is not a shape functy can be asked, so it goes
// straight to the resolver.
func TestHelpRoutesAPathToTheResolver(t *testing.T) {
	c := helpTestConfig(t)
	withResolver(t, &greedyResolver{})

	assert.Equal(t, "RESOLVER::client", callHelp(t, c, "client", "mqtt").AsString())
}

func TestHelpKindPrefixSelectsTheNamespace(t *testing.T) {
	c := helpTestConfig(t)
	r := &greedyResolver{}
	withResolver(t, r)

	// "assert" is both a block type and a function; the prefix says which.
	got := callHelp(t, c, "block:assert")
	assert.Equal(t, "RESOLVER:block:assert", got.AsString())
	assert.Equal(t, "block", r.lastKind)

	// Without the prefix the function wins, which is the precedence rule.
	assert.NotContains(t, callHelp(t, c, "assert").AsString(), "RESOLVER")
}

// A qualified functy name uses "::" and must survive intact — otherwise
// help("block::f") would become a request for the block named ":f", and any
// namespace sharing a name with a kind would be unreachable.
func TestHelpDoesNotMistakeQualifiedNamesForKindPrefixes(t *testing.T) {
	c := helpTestConfig(t)
	r := &greedyResolver{}
	withResolver(t, r)

	got := callHelp(t, c, "block::thing")
	// Not a function and not a kind prefix: the whole string goes to the
	// resolver as one name.
	assert.Equal(t, "RESOLVER::block::thing", got.AsString())
	assert.Empty(t, r.lastKind)
}

func TestHelpIsNullForSomethingThatNamesNothing(t *testing.T) {
	c := helpTestConfig(t)
	// No resolver at all: the shape a build that never links schemadoc has.
	withResolver(t, nil)

	assert.True(t, callHelp(t, c, "no_such_thing_xyz").IsNull())
	// And a real function still answers without one.
	assert.False(t, callHelp(t, c, "send").IsNull())
}

func TestHelpRejectsAnEmptyTopic(t *testing.T) {
	c := helpTestConfig(t)
	fn := c.EvalCtx().Functions["help"]

	_, err := fn.Call([]cty.Value{cty.StringVal("")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be empty")
}

func TestSplitHelpKind(t *testing.T) {
	withResolver(t, &greedyResolver{})

	for _, tc := range []struct{ in, kind, rest string }{
		{"subscription", "", "subscription"},
		{"block:http", "block", "http"},
		{"context:message", "context", "message"},
		// Not a kind the resolver knows.
		{"widget:http", "", "widget:http"},
		// A functy qualified name.
		{"time::now", "", "time::now"},
		{"block::f", "", "block::f"},
		// A leading colon names no kind.
		{":http", "", ":http"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			kind, rest := splitHelpKind(tc.in)
			assert.Equal(t, tc.kind, kind)
			assert.Equal(t, tc.rest, rest)
		})
	}
}

// With no resolver registered there are no kinds, so nothing may be read as a
// prefix — a name containing a colon has to survive.
func TestSplitHelpKindWithNoResolver(t *testing.T) {
	withResolver(t, nil)

	kind, rest := splitHelpKind("block:http")
	assert.Empty(t, kind)
	assert.Equal(t, "block:http", rest)
}

func TestFuncHelpMatchesTheEvalContext(t *testing.T) {
	c := helpTestConfig(t)

	got, ok := c.FuncHelp("send")
	require.True(t, ok)
	assert.Contains(t, got, "send(")

	_, ok = c.FuncHelp("no_such_thing_xyz")
	assert.False(t, ok)
}

func TestFuncNamesAreSortedAndNonEmpty(t *testing.T) {
	names := helpTestConfig(t).FuncNames()

	require.NotEmpty(t, names)
	assert.IsIncreasing(t, names)
	assert.Contains(t, names, "send")
	assert.Contains(t, names, "help")
}
