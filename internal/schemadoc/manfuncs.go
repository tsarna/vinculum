package schemadoc

import (
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
		"man::page":  manPageFunc(helpDoc, BuiltinFuncs),
		"man::index": manIndexFunc(helpDoc),
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
			// Every argument is split on whitespace, so a menu entry — or an MCP
			// tool's one topic string — is a valid call as it stands. No topic
			// name contains a space, so nothing is lost by it.
			var kind Kind
			var path []string
			for i, a := range args {
				words := strings.Fields(a.AsString())
				if len(words) == 0 {
					return cty.NilVal, function.NewArgErrorf(i, "topic must not be empty")
				}
				if i == 0 {
					k, rest, err := ParseKindPrefix(words[0])
					if err != nil {
						return cty.NilVal, function.NewArgError(i, err)
					}
					kind, words[0] = k, rest
				}
				path = append(path, words...)
			}

			// A function name is a whole path, so a longer one cannot name a
			// function, and neither can a path restricted to another kind. Don't
			// pay for building the catalog to find that out, just as
			// `vinculum man client mqtt` and `vinculum man --type block client`
			// don't.
			var cat FuncCatalog
			if len(path) == 1 && (kind == "" || kind == KindFunction) {
				cat = catFn()
			}
			doc := docFn()
			candidates := append(Resolve(doc, kind, path), ResolveFuncs(cat, kind, path)...)

			var events []Event
			switch len(candidates) {
			case 1:
				events = Walk(candidates[0], WalkOptions{})
			case 0:
				// A bare function name declared in two namespaces resolves to
				// nothing, just as a misspelling does. Only the menu tells them
				// apart.
				names := AmbiguousFuncName(cat, kind, path)
				if len(names) == 0 {
					// Null means "nothing is called that", as it does for help().
					// Near misses are left to the caller.
					return cty.NullVal(cty.String), nil
				}
				events = []Event{AmbiguousFuncMenu(path, names, PathSpeller)}
			default:
				events = []Event{MenuFor(path, candidates, PathSpeller)}
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
