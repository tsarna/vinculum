package schemadoc

import (
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"go.uber.org/zap"
)

// callPage calls man::page over the fixtures with the given words.
func callPage(t *testing.T, cat FuncCatalog, words ...string) (cty.Value, error) {
	t.Helper()
	fn := manPageFunc(testDoc, func() FuncCatalog { return cat })
	args := make([]cty.Value, len(words))
	for i, w := range words {
		args[i] = cty.StringVal(w)
	}
	return fn.Call(args)
}

func TestManPageRendersATopicAsMarkdown(t *testing.T) {
	got, err := callPage(t, testCatalog(), "client", "mqtt")
	require.NoError(t, err)

	want := renderNode(Resolve(testDoc(), "", []string{"client", "mqtt"})[0], WalkOptions{})
	assert.Equal(t, want, got.AsString(), "exactly the page `vinculum man --format markdown` renders")
}

func TestManPageRendersAFunction(t *testing.T) {
	got, err := callPage(t, testCatalog(), "send")
	require.NoError(t, err)
	assert.Contains(t, got.AsString(), "# `send()`")
	assert.Contains(t, got.AsString(), "Sends a message.")

	got, err = callPage(t, testCatalog(), "function:send")
	require.NoError(t, err)
	assert.Contains(t, got.AsString(), "# `send()`")
}

// An ambiguous name gets the menu, spelled as bare topic paths, which every
// front door man:: output may be served through accepts unchanged.
func TestManPageAnswersAmbiguityWithAMenu(t *testing.T) {
	got, err := callPage(t, testCatalog(), "http")
	require.NoError(t, err)
	assert.Equal(t, "\"http\" is ambiguous, choose one of:\n\n    client http\n    server http\n", got.AsString())
	assert.NotContains(t, got.AsString(), "man::page", "no front door's call syntax")
}

// Unlike help(), which asks functy first, man::page searches both corpora at
// once, as `vinculum man` does: a name that is a block and a function is
// ambiguous, and a kind: prefix chooses.
func TestManPageSearchesBlocksAndFunctionsTogether(t *testing.T) {
	cat := testCatalog()
	cat.docs["subscription"] = config.FuncDoc{Name: "subscription", Signatures: []string{"subscription() -> bool"}}

	got, err := callPage(t, cat, "subscription")
	require.NoError(t, err)
	assert.Contains(t, got.AsString(), "    block:subscription\n")
	assert.Contains(t, got.AsString(), "    function:subscription\n")

	got, err = callPage(t, cat, "block:subscription")
	require.NoError(t, err)
	assert.Equal(t, renderNode(Resolve(testDoc(), KindBlock, []string{"subscription"})[0], WalkOptions{}), got.AsString())
}

func TestManPageNamesBothNamespacesOfAnAmbiguousFunction(t *testing.T) {
	got, err := callPage(t, testCatalog(), "dup")
	require.NoError(t, err)
	assert.Contains(t, got.AsString(), `"dup" is a function in more than one namespace`)
	assert.Contains(t, got.AsString(), "    a::dup\n")
	assert.Contains(t, got.AsString(), "    b::dup\n")
}

// Null, as help() returns: absence is a normal answer, and the caller decides
// whether to go looking for near misses.
func TestManPageReturnsNullForNothing(t *testing.T) {
	got, err := callPage(t, testCatalog(), "no_such_topic")
	require.NoError(t, err)
	assert.True(t, got.IsNull())
	assert.Equal(t, cty.String, got.Type())

	// A kind that finds nothing is the same answer.
	got, err = callPage(t, testCatalog(), "context:subscription")
	require.NoError(t, err)
	assert.True(t, got.IsNull())
}

// With no function catalog the block corpus still answers.
func TestManPageWithoutACatalog(t *testing.T) {
	got, err := callPage(t, nil, "subscription")
	require.NoError(t, err)
	assert.False(t, got.IsNull())

	got, err = callPage(t, nil, "send")
	require.NoError(t, err)
	assert.True(t, got.IsNull())
}

// Arguments are split on whitespace, so a menu entry or a single topic string
// is a valid call as it stands.
func TestManPageSplitsArgumentsOnSpaces(t *testing.T) {
	want, err := callPage(t, testCatalog(), "client", "mqtt")
	require.NoError(t, err)

	for _, words := range [][]string{
		{"client mqtt"},
		{"  client\tmqtt  "},
	} {
		got, err := callPage(t, testCatalog(), words...)
		require.NoError(t, err, "%q", words)
		assert.Equal(t, want.AsString(), got.AsString(), "%q", words)
	}

	// The kind prefix is on the first word, whichever argument carries it.
	got, err := callPage(t, testCatalog(), "block:client mqtt")
	require.NoError(t, err)
	assert.Equal(t, want.AsString(), got.AsString())

	// Only the first word: later a kind: is literal, as it always was.
	got, err = callPage(t, testCatalog(), "client block:mqtt")
	require.NoError(t, err)
	assert.True(t, got.IsNull())
}

// Every entry of a menu is a valid call as it stands. cmd's test does this
// against the real registry; this one covers the function-namespace menu,
// which the fixture has and the registry does not.
func TestManPageMenusRoundTrip(t *testing.T) {
	cat := testCatalog()
	cat.docs["subscription"] = config.FuncDoc{Name: "subscription", Signatures: []string{"subscription() -> bool"}}
	cat.docs["a::dup"] = config.FuncDoc{Name: "a::dup", Signatures: []string{"a::dup() -> bool"}}
	cat.docs["b::dup"] = config.FuncDoc{Name: "b::dup", Signatures: []string{"b::dup() -> bool"}}

	for _, query := range []string{"http", "subscription", "dup"} {
		menu, err := callPage(t, cat, query)
		require.NoError(t, err)

		var items []string
		for _, line := range strings.Split(menu.AsString(), "\n") {
			if item, ok := strings.CutPrefix(line, "    "); ok {
				items = append(items, item)
			}
		}
		require.Len(t, items, 2, "%q", query)

		for _, item := range items {
			got, err := callPage(t, cat, item)
			require.NoError(t, err, "%q", item)
			if assert.False(t, got.IsNull(), "%q from the %q menu", item, query) {
				// Every menu opens by quoting the query; a page opens with a heading.
				assert.False(t, strings.HasPrefix(got.AsString(), strconv.Quote(item)+" is "),
					"%q from the %q menu is itself a menu", item, query)
			}
		}
	}
}

// A multi-word path, or one restricted to a kind other than function, cannot
// name a function, so it does not pay for building the function catalog.
func TestManPageBuildsTheCatalogOnlyWhenItCouldMatter(t *testing.T) {
	calls := 0
	fn := manPageFunc(testDoc, func() FuncCatalog {
		calls++
		return testCatalog()
	})
	call := func(words ...string) {
		t.Helper()
		args := make([]cty.Value, len(words))
		for i, w := range words {
			args[i] = cty.StringVal(w)
		}
		_, err := fn.Call(args)
		require.NoError(t, err, "%q", words)
	}

	call("client", "mqtt")
	call("client mqtt")
	call("block:client")
	call("context:message")
	assert.Zero(t, calls)

	call("send")
	call("function:send")
	assert.Equal(t, 2, calls)
}

func TestManPageRejectsMalformedTopics(t *testing.T) {
	for name, words := range map[string][]string{
		"no topic":         {},
		"empty":            {""},
		"only spaces":      {"   "},
		"empty later word": {"client", ""},
		"unknown kind":     {"blok:subscription"},
		"kind, no topic":   {"block:"},
		"kind, then space": {"block: subscription"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := callPage(t, testCatalog(), words...)
			assert.Error(t, err)
		})
	}

	fn := manPageFunc(testDoc, func() FuncCatalog { return testCatalog() })
	_, err := fn.Call([]cty.Value{cty.NullVal(cty.String)})
	assert.Error(t, err, "null is not a topic")

	// Splitting on spaces makes "block: subscription" a kind with no topic; the
	// error says how to write it.
	_, err = callPage(t, testCatalog(), "block: subscription")
	assert.ErrorContains(t, err, "straight after the colon")
}

func TestManIndexRendersTheFrontPage(t *testing.T) {
	got, err := manIndexFunc(testDoc).Call(nil)
	require.NoError(t, err)
	want := RenderMarkdown(append(Index(testDoc(), WalkOptions{}), Usage(pathExamples...)), MarkdownOptions{})
	assert.Equal(t, want, got.AsString())
	assert.Contains(t, got.AsString(), "# Vinculum configuration language")
	assert.Contains(t, got.AsString(), "`subscription`")

	// Functions are not on the index, so the footer is the only place a reader
	// learns they are reachable — spelled as a bare topic path.
	assert.Contains(t, got.AsString(), "\n    send\n")
	assert.NotContains(t, got.AsString(), "vinculum man", "no front door's invocation syntax")
}

func TestPathSpeller(t *testing.T) {
	assert.Equal(t, "subscription", PathSpeller(KindBlock, []string{"subscription"}, false))
	assert.Equal(t, "client mqtt", PathSpeller(KindBlock, []string{"client", "mqtt"}, false))
	assert.Equal(t, "block:assert", PathSpeller(KindBlock, []string{"assert"}, true))
	assert.Equal(t, "function:assert", PathSpeller(KindFunction, []string{"assert"}, true))

	// It must not modify the path it was given.
	path := []string{"assert"}
	PathSpeller(KindBlock, path, true)
	assert.Equal(t, []string{"assert"}, path)
}

func TestParseKindPrefix(t *testing.T) {
	for _, tc := range []struct {
		in, rest string
		kind     Kind
		err      bool
	}{
		{in: "subscription", rest: "subscription"},
		{in: "block:assert", kind: KindBlock, rest: "assert"},
		{in: "function:send", kind: KindFunction, rest: "send"},
		// A functy qualified name is one word, not a kind.
		{in: "time::now", rest: "time::now"},
		{in: "blok:assert", err: true},
		{in: "block:", err: true},
		{in: ":assert", err: true},
	} {
		kind, rest, err := ParseKindPrefix(tc.in)
		if tc.err {
			assert.Error(t, err, tc.in)
			continue
		}
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.kind, kind, tc.in)
		assert.Equal(t, tc.rest, rest, tc.in)
	}
}

// The live registration, against a real config. functions/metadata_test.go
// holds every other function to this standard, but it cannot hold these: that
// package does not link this one.
func TestManFunctionsAreRegisteredAndDocumented(t *testing.T) {
	cfg, diags := config.NewConfig().WithLogger(zap.NewNop()).Build()
	require.False(t, diags.HasErrors(), "%s", diags)

	for name, params := range map[string][]cty.Type{
		"man::page":  {cty.String, cty.String},
		"man::index": nil,
	} {
		fn, ok := cfg.EvalCtx().Functions[name]
		require.True(t, ok, "%s is not registered", name)
		assert.NotEmpty(t, fn.Description(), name)
		for _, p := range fn.Params() {
			assert.NotEmpty(t, p.Description, "%s param %s", name, p.Name)
		}
		if vp := fn.VarParam(); vp != nil {
			assert.NotEmpty(t, vp.Description, "%s param %s", name, vp.Name)
		}
		ret, err := fn.ReturnType(params)
		require.NoError(t, err, name)
		assert.Equal(t, cty.String, ret, "%s must expose its static return type", name)
	}
}

// man::page from a const is evaluated during Build, and builds a config of its
// own, without sources, to find the built-in functions. That nesting has to work.
func TestManPageWorksDuringBuild(t *testing.T) {
	// A fresh cache, so the nested build happens here whatever ran first.
	built := false
	saved := builtinFuncs
	builtinFuncs = sync.OnceValue(func() FuncCatalog {
		built = true
		return buildBuiltinFuncs()
	})
	t.Cleanup(func() { builtinFuncs = saved })

	cfg, diags := config.NewConfig().WithLogger(zap.NewNop()).
		WithSources([]byte(`const { p = man::page("subscription") }`)).
		Build()
	require.False(t, diags.HasErrors(), "%s", diags)
	assert.True(t, built, "the catalog was built inside the outer Build")

	p := cfg.EvalCtx().Variables["p"]
	require.Equal(t, cty.String, p.Type())
	require.False(t, p.IsNull())
	assert.Contains(t, p.AsString(), "`subscription`")
}

// The function corpus is the built-ins, not the host config's own helpers: a
// docs site should not document itself. help() still finds them.
func TestManPageDoesNotDocumentTheHostConfigsFunctions(t *testing.T) {
	cfg, diags := config.NewConfig().WithLogger(zap.NewNop()).
		WithSources([]byte(`function "my_helper" {
  params = []
  result = 1
}`)).
		Build()
	require.False(t, diags.HasErrors(), "%s", diags)

	fns := cfg.EvalCtx().Functions
	call := func(fn function.Function, arg string) cty.Value {
		t.Helper()
		v, err := fn.Call([]cty.Value{cty.StringVal(arg)})
		require.NoError(t, err)
		return v
	}
	assert.False(t, call(fns["help"], "my_helper").IsNull(), "help() documents the running config")
	assert.True(t, call(fns["man::page"], "my_helper").IsNull(), "man::page documents the built-ins")
	assert.False(t, call(fns["man::page"], "help").IsNull(), "a built-in function is found")
}
