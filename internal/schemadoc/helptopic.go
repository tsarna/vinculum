package schemadoc

import (
	"strconv"
	"strings"
	"sync"

	"github.com/tsarna/vinculum/config"
)

// help() from inside a VCL expression.
//
// The same resolver and the same walk that back `vinculum man`, reached through
// config.HelpTopicResolver so that config — which this package imports — does
// not have to import this one back.
//
// Registration happens in init(), so linking this package is what turns the
// feature on. The binary links it for `vinculum man`; a program that embeds
// only config gets functy's help() and nothing more.
func init() {
	config.RegisterHelpTopicResolver(helpResolver{})
}

// helpWidth is what help() wraps to. It has no terminal to measure — the string
// may be logged, concatenated, or written to a file — so it takes the
// conventional width rather than guessing at a destination.
const helpWidth = DefaultWidth

type helpResolver struct{}

// helpDoc is generated once. The schema is derived from registries that are
// fixed by the time any expression evaluates: plugins register during .vinit
// processing, which precedes every eval. Regenerating it per call would make
// help() in a loop quadratic for no gain.
var helpDoc = sync.OnceValue(func() *config.SchemaDocument {
	// Curation problems are `vinculum schema --strict`'s business, and CI fails
	// on them. Rendering what is there beats refusing to answer.
	doc, _ := config.GenerateSchema(config.SchemaGenOptions{})
	return doc
})

func (helpResolver) HelpKinds() []string {
	out := make([]string, 0, len(Kinds))
	for _, k := range Kinds {
		// Functions are functy's to answer inside help(); offering "function:"
		// as a prefix here would route them to a resolver that has no catalog.
		if k == KindFunction {
			continue
		}
		out = append(out, string(k))
	}
	return out
}

func (helpResolver) HelpTopic(kind string, path []string) (string, bool) {
	doc := helpDoc()
	candidates := Resolve(doc, Kind(kind), path)

	switch len(candidates) {
	case 0:
		return "", false
	case 1:
		return RenderPlain(Walk(candidates[0], WalkOptions{}), helpWidth), true
	default:
		// An ambiguous path is answered, not refused: the menu of exact calls
		// that resolve it is the useful reply, and returning null instead would
		// be indistinguishable from naming nothing at all.
		return RenderPlain([]Event{MenuFor(path, candidates, HelpSpeller)}, helpWidth), true
	}
}

// HelpSpeller spells a candidate as a help() call, for a menu printed inside an
// expression. `vinculum man client mqtt` becomes `help("client", "mqtt")`, and a
// kind qualifier becomes the `kind:` prefix on the first argument rather than a
// flag, because that is the only place a call can carry one.
func HelpSpeller(kind Kind, path []string, qualify bool) string {
	args := make([]string, 0, len(path))
	for i, p := range path {
		if i == 0 && qualify {
			p = string(kind) + ":" + p
		}
		args = append(args, strconv.Quote(p))
	}
	return "help(" + strings.Join(args, ", ") + ")"
}
