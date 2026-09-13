package schemadoc

import (
	"fmt"
	"strings"
	"sync"

	"github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"go.uber.org/zap"
)

// man:: — the reference as Markdown, from inside a config.
//
// help() answers in plain text, for a person reading at a prompt. man:: answers
// in Markdown, for a page or a model to read: the same walk `vinculum man`
// renders when its output is not a terminal. The two also resolve differently,
// on purpose. help() asks functy first, so that nothing it used to answer
// changes. man::page resolves the way the command does, searching both corpora
// at once, so a name that is both a block and a function gets the menu.
//
// It is registered from this package for the same reason help()'s topic
// resolver is: linking the renderer is what turns the feature on, and an
// embedder that links config and functions but not this package gets neither.
func init() {
	config.RegisterFunctionPlugin("man", func(*config.Config) map[string]function.Function {
		return manFunctions()
	})
}

func manFunctions() map[string]function.Function {
	return map[string]function.Function{
		"man::page":     manPageFunc(helpDoc, BuiltinFuncs),
		"man::index":    manIndexFunc(helpDoc),
		"man::synopsis": manSynopsisFunc(helpDoc, BuiltinFuncs),
		"man::apropos":  manAproposFunc(helpDoc, BuiltinFuncs),
	}
}

// BuiltinFuncs is the function corpus of `vinculum man` and man::: the
// functions of a config with no sources of its own. That is every built-in,
// plus whatever the linked libraries and loaded plugins register. The
// functions a flag switches on (--file-path, --write-path, --allow-kill) are
// not in it, since a sourceless config enables no features. Neither are the
// hosting config's own function, jq, and .cty definitions, because a docs site
// should not document its own helpers.
//
// It is built once, on the first call, rather than at registration. Plugins
// register while .vinit files are processed, or in `vinculum man --plugin-path`
// before any lookup, so a lazy build includes them. Build takes no lock, so
// building this inside another config's Build — a const that calls man::page —
// is safe.
func BuiltinFuncs() FuncCatalog { return builtinFuncs() }

// builtinFuncs is a variable so that a test can replace it with a fresh one.
var builtinFuncs = sync.OnceValue(buildBuiltinFuncs)

func buildBuiltinFuncs() FuncCatalog {
	// A discarding logger: building this config is a lookup, and its startup
	// chatter is not the answer to the question being asked.
	cfg, diags := config.NewConfig().WithLogger(zap.NewNop()).Build()
	if diags.HasErrors() || cfg == nil {
		// Nothing to document, rather than an error: the block corpus still
		// answers. Return an untyped nil here, because a nil *Config stored in
		// the interface would not compare equal to nil.
		return nil
	}
	return cfg
}

// manPageFunc builds man::page over the given document and catalog. They are
// passed in as functions so that a test can substitute fixtures.
func manPageFunc(docFn func() *config.SchemaDocument, catFn func() FuncCatalog) function.Function {
	return function.New(&function.Spec{
		Description: `Render one topic of the configuration-language reference as Markdown: man::page("subscription"), man::page("client mqtt") or man::page("client", "mqtt"), or man::page("send") for a function. A name that could mean more than one thing renders a menu of the topic paths that resolve it ("client http"), each of which can be passed back as it stands; a name that means nothing returns null. Prefix the topic with a kind — man::page("block:assert") — to choose.`,
		Params: []function.Parameter{{
			Name:        "topic",
			Type:        cty.String,
			Description: "The topic path, or its first words, separated by spaces; the first word may carry a kind: prefix (" + KindList() + ")",
		}},
		VarParam: &function.Parameter{
			Name:        "subtopics",
			Type:        cty.String,
			Description: "Further words of the topic path, each argument split on spaces the same way",
		},
		Type: function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			q, err := manLookup(docFn, catFn, args)
			if err != nil {
				return cty.NilVal, err
			}
			if len(q.Candidates) == 1 {
				return cty.StringVal(RenderMarkdown(Walk(q.Candidates[0], WalkOptions{}), MarkdownOptions{})), nil
			}
			if events := q.MenuEvents(); events != nil {
				return cty.StringVal(RenderMarkdown(events, MarkdownOptions{})), nil
			}
			// Null means "nothing is called that", as it does for help(). Near
			// misses are left to the caller.
			return cty.NullVal(cty.String), nil
		},
	})
}

// manQuery is a parsed and resolved man:: lookup.
//
// man::page and man::synopsis ask exactly the same question and differ only in
// what they render from the answer, so the parse, the kind prefix, the catalog
// laziness and the menus live here rather than once per function — where they
// would drift, and the two would stop agreeing about what a topic is.
type manQuery struct {
	Kind Kind
	Path []string
	// Cat is nil when the path could not have named a function.
	Cat        FuncCatalog
	Candidates []Node
}

// manLookup parses man::-style arguments and resolves them the way `vinculum
// man` does, searching both corpora at once.
func manLookup(docFn func() *config.SchemaDocument, catFn func() FuncCatalog, args []cty.Value) (manQuery, error) {
	// Every argument is split on whitespace, so a menu entry — or an MCP
	// tool's one topic string — is a valid call as it stands. No topic
	// name contains a space, so nothing is lost by it.
	var q manQuery
	for i, a := range args {
		words := strings.Fields(a.AsString())
		if len(words) == 0 {
			return q, function.NewArgErrorf(i, "topic must not be empty")
		}
		if i == 0 {
			k, rest, err := ParseKindPrefix(words[0])
			if err != nil {
				return q, function.NewArgError(i, err)
			}
			q.Kind, words[0] = k, rest
		}
		q.Path = append(q.Path, words...)
	}

	// A function name is a whole path, so a longer one cannot name a
	// function, and neither can a path restricted to another kind. Don't
	// pay for building the catalog to find that out, just as
	// `vinculum man client mqtt` and `vinculum man --type block client`
	// don't.
	if len(q.Path) == 1 && (q.Kind == "" || q.Kind == KindFunction) {
		q.Cat = catFn()
	}
	q.Candidates = append(Resolve(docFn(), q.Kind, q.Path), ResolveFuncs(q.Cat, q.Kind, q.Path)...)
	return q, nil
}

// MenuEvents is what to render instead of a topic when the query did not
// resolve to exactly one — either because it named several things, or because
// it named a bare function declared in two namespaces, which resolves to
// nothing exactly as a misspelling does and which only the menu tells apart.
// nil when the query resolved to one topic, or to nothing at all.
func (q manQuery) MenuEvents() []Event {
	if len(q.Candidates) > 1 {
		return []Event{MenuFor(q.Path, q.Candidates, PathSpeller)}
	}
	if len(q.Candidates) == 0 {
		if names := AmbiguousFuncName(q.Cat, q.Kind, q.Path); len(names) > 0 {
			return []Event{AmbiguousFuncMenu(q.Path, names, PathSpeller)}
		}
	}
	return nil
}

// manSynopsisFunc builds man::synopsis over the given document and catalog.
func manSynopsisFunc(docFn func() *config.SchemaDocument, catFn func() FuncCatalog) function.Function {
	return function.New(&function.Spec{
		Description: `Render just the skeleton of one topic of the configuration-language reference as Markdown: a block's header with its attributes and sub-blocks — man::synopsis("client mqtt") — or a function's calling conventions, one line per form. It is the opening of the page man::page renders, for a reader who wants the shape of a block rather than its documentation. Resolution is man::page's, so an ambiguous name renders the same menu and a name that means nothing returns null. A topic that has no skeleton of its own — an attribute, a ctx shape, a namespace member — is an error, so fall back with try(man::synopsis(x), man::page(x)).`,
		Params: []function.Parameter{{
			Name:        "topic",
			Type:        cty.String,
			Description: "The topic path, or its first words, separated by spaces; the first word may carry a kind: prefix (" + KindList() + ")",
		}},
		VarParam: &function.Parameter{
			Name:        "subtopics",
			Type:        cty.String,
			Description: "Further words of the topic path, each argument split on spaces the same way",
		},
		Type: function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			q, err := manLookup(docFn, catFn, args)
			if err != nil {
				return cty.NilVal, err
			}
			if len(q.Candidates) == 1 {
				n := q.Candidates[0]
				// A typed block has no one skeleton: its shape is its type's. The
				// page's stub says "see below" about the types listed under it,
				// which a synopsis alone does not have, so answer with the menu
				// of types instead — the same next step an ambiguous name offers.
				if n.shape == shapeBlock && n.block.VariantLabel != "" {
					return cty.StringVal(RenderMarkdown([]Event{typeMenu(n)}, MarkdownOptions{})), nil
				}
				// An error rather than null where a topic has no skeleton: null
				// already means "nothing is named that", and one answer for both
				// would make a real topic indistinguishable from a typo.
				events, err := WalkSection(n, SectionSynopsis, WalkOptions{})
				if err != nil {
					return cty.NilVal, function.NewArgError(0, err)
				}
				return cty.StringVal(RenderMarkdown(events, MarkdownOptions{})), nil
			}
			if events := q.MenuEvents(); events != nil {
				return cty.StringVal(RenderMarkdown(events, MarkdownOptions{})), nil
			}
			return cty.NullVal(cty.String), nil
		},
	})
}

// typeMenu is the menu of a typed block's variants, each spelled as the topic
// path whose synopsis is a real skeleton.
func typeMenu(n Node) Menu {
	names := sortedBodyNames(n.block.Variants)
	items := make([]string, 0, len(names))
	for _, v := range names {
		items = append(items, PathSpeller(KindBlock, []string{n.Path[0], v}, false))
	}
	return Menu{
		Intro: fmt.Sprintf("%q takes a %s label, and each has its own skeleton; choose one of:",
			n.Path[0], n.block.VariantLabel),
		Items: items,
	}
}

// aproposMaxRows bounds a search's answer. A one-letter term matches most of the
// language — "a" is over a thousand rows, a hundred KB — which no reader wants
// and no MCP client should be handed. Name matches sort first, so the rows kept
// are the likeliest ones. A variable so a test can lower it.
var aproposMaxRows = 50

// manAproposFunc builds man::apropos over the given document and catalog.
func manAproposFunc(docFn func() *config.SchemaDocument, catFn func() FuncCatalog) function.Function {
	return function.New(&function.Spec{
		Description: `Search the configuration-language reference by keyword, for the reader who knows a word but not which block owns it: every block, attribute, sub-block, ctx field, namespace member and function whose name or summary contains all of the terms, as a Markdown table. man::apropos("keep alive") and man::apropos("keep", "alive") are the same search. Each row names a topic path that man::page accepts as it stands, so a search leads straight to a page. At most fifty rows are shown — a name that is exactly a term first, then other name matches — with a count of the rest. Null when nothing matches.`,
		Params: []function.Parameter{{
			Name:        "term",
			Type:        cty.String,
			Description: "A keyword to match against names and one-line summaries; an argument holding several words, separated by spaces, is several keywords",
		}},
		VarParam: &function.Parameter{
			Name:        "terms",
			Type:        cty.String,
			Description: "Further keywords, each argument split on spaces the same way; every keyword must match, so more words narrow the search",
		},
		Type: function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			// Split on whitespace for the reason man::page does: one string of
			// keywords, as a search box or an MCP tool would pass it, is a valid
			// call as it stands.
			var terms []string
			for i, a := range args {
				words := strings.Fields(a.AsString())
				if len(words) == 0 {
					return cty.NilVal, function.NewArgErrorf(i, "a search term must not be empty")
				}
				terms = append(terms, words...)
			}

			// A search always pays for the function catalog, where a lookup does
			// not: a keyword can match a function's name or its prose whatever
			// else it matches, and leaving the corpus out would make the answer
			// depend on how many words were typed.
			hits := Apropos(docFn(), catFn(), "", terms)
			if len(hits) == 0 {
				// Null, as man::page answers for a topic nothing is named: an
				// empty result set renders as the empty string, which is a worse
				// answer than "there is nothing".
				return cty.NullVal(cty.String), nil
			}
			// PathSpeller, so each row is a bare topic path man::page accepts —
			// the same reason a menu is spelled that way. Qualification is
			// decided over every hit before the rows are cut, so a kept row is
			// spelled the same whether or not its twin made the cut.
			results := ResultsFor(terms, hits, PathSpeller)
			events := []Event{results}
			if more := len(results.Rows) - aproposMaxRows; more > 0 {
				results.Rows = results.Rows[:aproposMaxRows]
				events = []Event{results, Prose{Markdown: fmt.Sprintf(
					"%d more topics match, and are not shown. Add a word to narrow the search.", more)}}
			}
			return cty.StringVal(RenderMarkdown(events, MarkdownOptions{})), nil
		},
	})
}

// pathExamples are the topics the index footer suggests, spelled as bare paths
// for the reason PathSpeller gives. Functions are not on the index, so the
// footer is where a reader learns that they are reachable at all.
var pathExamples = []string{
	"subscription",
	"client mqtt",
	"server http handle",
	"send",
}

// manIndexFunc builds man::index over the given document.
func manIndexFunc(docFn func() *config.SchemaDocument) function.Function {
	return function.New(&function.Spec{
		Description: "Render the front page of the configuration-language reference as Markdown: every block, `ctx` shape, and namespace, each with a one-line summary, followed by a few example topic paths to pass to man::page().",
		Params:      []function.Parameter{},
		Type:        function.StaticReturnType(cty.String),
		Impl: func(_ []cty.Value, _ cty.Type) (cty.Value, error) {
			events := append(Index(docFn(), WalkOptions{}), Usage(pathExamples...))
			return cty.StringVal(RenderMarkdown(events, MarkdownOptions{})), nil
		},
	})
}

// PathSpeller spells a candidate as its bare topic path — `client http`, and
// `block:assert` when qualified — for a menu rendered by man::.
//
// man:: has no one reader to address. Its output may be served to an MCP client
// (through a tool with a name of its own, taking the path as one
// space-separated string), rendered by a docs site, or read by a config.
// Spelling a menu as a man::page() call would make each of them translate. A
// path is what man::page accepts as it stands — man::page("client http")
// splits its arguments on spaces for exactly this reason — and so does anything
// built on it, and so does the REPL's :man. `vinculum man` does not: it takes
// the words as separate arguments and a kind as --type, which is why its own
// menus are spelled as commands.
func PathSpeller(kind Kind, path []string, qualify bool) string {
	words := append([]string(nil), path...)
	if qualify && len(words) > 0 {
		words[0] = string(kind) + ":" + words[0]
	}
	return strings.Join(words, " ")
}
