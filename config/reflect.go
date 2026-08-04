package config

import (
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/functy"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// The reflection built-ins, help() and doc().
//
// They are registered as a function plugin rather than merged into the eval context
// after the fact, for two reasons: the plugin getter runs inside GetFunctions, by
// which point both of their dependencies exist (the eval context, and the parsed
// functy Result); and going through the plugin registry means they inherit its
// reserved-name check, so a user `function "help"` block is rejected outright rather
// than silently displacing the built-in.
//
// help() is the reason externs exist. A cty function can only make its *trailing*
// parameters optional, so the rich-cty-types get/set/count family — which takes an
// optional *leading* context, sniffed from the first argument — has to fake it with
// a variadic, and reflects from cty metadata as the useless `get(thing, ...args)`.
// The extern declarations those packages register (RegisterFunctyExterns) carry the
// real signatures, and help() prefers them over the cty fallback.
//
// Vinculum wraps functy's help() rather than registering it directly: an author
// asking `help("subscription")` is asking the same kind of question as
// `help("send")`, and one built-in should answer both. doc() stays functy's,
// unchanged — its tri-state null/""/text contract is what makes
// `assert(doc(x) != null)` work, and a block has no equivalent of "exists but is
// undocumented".
func init() {
	RegisterFunctionPlugin("reflect", func(c *Config) map[string]function.Function {
		evalCtxFn := func() *hcl.EvalContext { return c.evalCtx }

		return map[string]function.Function{
			"help": helpFunc(c.functyResult(), evalCtxFn),
			"doc":  functy.DocFunc(evalCtxFn),
		}
	})
}

// functyResult is the parsed .cty artifacts: the user's own declarations and the
// externs the host registered. Nil before Build, which the reflection builtins
// tolerate — which is what lets them exist in a config with no .cty sources.
func (c *Config) functyResult() *functy.Result {
	if c == nil || c.functyState == nil {
		return nil
	}
	return c.functyState.result
}

// helpFunc builds Vinculum's help(): functy's, extended to the topics the
// configuration language documents.
//
// Precedence is chosen so that nothing functy used to answer can change answer:
//
//  1. No argument — functy's directory of function names.
//  2. An explicit `kind:` prefix, or a multi-word path — the topic resolver.
//     Neither is a thing functy could have been asked, so there is nothing to
//     preserve, and an explicit kind is a reader saying which namespace they meant.
//  3. A single bare name — functy first. If it names a function, that is the
//     answer, exactly as before. Only when it does not does the resolver see it.
//
// Ordering functions ahead of topics matters more than the reverse would: an
// author typing help("x") at a prompt is usually mid-expression, and there are
// far more function names than block names for a bare word to collide with.
func helpFunc(res *functy.Result, evalCtxFn func() *hcl.EvalContext) function.Function {
	delegate := functy.HelpFunc(res, evalCtxFn)

	return function.New(&function.Spec{
		Description: `Return a human-readable help summary by name: help("f") for a function, help("subscription") or help("client", "mqtt") for part of the configuration language. Prefix with a kind — help("block:http") — to choose between them. Called with no argument, help() lists the names of all available functions.`,
		Params:      []function.Parameter{},
		VarParam: &function.Parameter{
			Name:        "topic",
			Type:        cty.String,
			Description: "The function or configuration topic to describe; omit to list all available function names",
		},
		Type: function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			// The no-argument directory and the unknown/null edge cases are
			// functy's to answer, unchanged — including their exact wording.
			if len(args) == 0 || !args[0].IsKnown() || args[0].IsNull() {
				return delegate.Call(args)
			}

			kind, path, err := helpPath(args)
			if err != nil {
				return cty.NilVal, err
			}

			if kind == "" && len(path) == 1 {
				// The one shape functy can be asked. Its answer wins.
				got, err := delegate.Call(args)
				if err != nil {
					return cty.NilVal, err
				}
				if !got.IsNull() {
					return got, nil
				}
			}

			if helpTopics != nil {
				if text, ok := helpTopics.HelpTopic(kind, path); ok {
					return cty.StringVal(text), nil
				}
			}
			// Null for "no such topic", preserving functy's contract: absence is
			// a normal reflection answer, so `help(x) == null` still detects a
			// name that names nothing.
			return cty.NullVal(cty.String), nil
		},
	})
}

// helpPath turns help()'s arguments into a kind and a topic path.
//
// Only the first argument may carry a `kind:` prefix: it selects a namespace for
// the whole path, so `help("client", "block:mqtt")` would be asking two
// questions at once.
func helpPath(args []cty.Value) (kind string, path []string, err error) {
	path = make([]string, 0, len(args))
	for i, a := range args {
		if a.IsNull() || !a.IsKnown() {
			return "", nil, function.NewArgErrorf(i, "topic must not be null")
		}
		s := a.AsString()
		if i == 0 {
			kind, s = splitHelpKind(s)
		}
		if s == "" {
			return "", nil, function.NewArgErrorf(i, "topic must not be empty")
		}
		path = append(path, s)
	}
	return kind, path, nil
}

// FuncNames returns the names of every function callable in this Config's
// assembled eval context, sorted.
//
// Private functy functions are absent — they were never put in the host's map.
func (c *Config) FuncNames() []string {
	if c == nil || c.evalCtx == nil {
		return nil
	}
	out := make([]string, 0, len(c.evalCtx.Functions))
	for name := range c.evalCtx.Functions {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// FuncHelp renders one function's help, or reports false when nothing of that
// name is callable.
//
// This is the same rendering `help("name")` returns from inside an expression,
// because it is the same call: `vinculum man --type function send` and
// `help("send")` cannot describe a function differently.
func (c *Config) FuncHelp(name string) (string, bool) {
	if c == nil {
		return "", false
	}
	evalCtxFn := func() *hcl.EvalContext { return c.evalCtx }
	got, err := functy.HelpFunc(c.functyResult(), evalCtxFn).Call([]cty.Value{cty.StringVal(name)})
	if err != nil || !got.IsKnown() || got.IsNull() {
		return "", false
	}
	return strings.TrimRight(got.AsString(), "\n"), true
}
