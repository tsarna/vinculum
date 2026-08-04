package config

import (
	"sort"
	"strings"
)

// Help topics beyond functions.
//
// functy's help() documents functions, which is most of what an author asks
// about from inside an expression. It is not all of it: `help("subscription")`
// is just as reasonable a question, and the answer already exists — the same
// generated description `vinculum man` renders.
//
// The renderer lives in internal/schemadoc, which imports this package to read
// the schema. It therefore cannot be imported from here, so it registers itself
// instead — the same shape as every other extension point in this package, and
// for the same reason.
//
// Registration is optional. A build that never links schemadoc gets functy's
// help() and nothing more, rather than a broken one.

// HelpTopicResolver documents the topics that are not functions: block types,
// their variants and members, and `ctx` shapes.
type HelpTopicResolver interface {
	// HelpKinds returns the kind names this resolver accepts, which are what
	// may appear as a `kind:` prefix — "block:http" to ask for the block when
	// a bare "http" would be ambiguous.
	HelpKinds() []string

	// HelpTopic renders the topic named by path, already formatted for
	// reading. An empty kind searches every kind.
	//
	// The bool distinguishes "nothing is named that" from "here is the answer".
	// An ambiguous path is an answer: the rendered menu of ways to resolve it.
	HelpTopic(kind string, path []string) (string, bool)
}

// helpTopics is the registered resolver, or nil. One slot rather than a list:
// resolution has to order its candidates to report ambiguity, and two
// independent resolvers could not be ordered against each other.
var helpTopics HelpTopicResolver

// RegisterHelpTopicResolver installs the resolver that help() consults for
// topics that are not functions. A second call replaces the first.
func RegisterHelpTopicResolver(r HelpTopicResolver) { helpTopics = r }

// splitHelpKind separates an explicit `kind:` prefix from a topic name.
//
// The prefix is recognized only when what precedes the colon is a kind the
// resolver knows *and* the next character is not a second colon. functy's
// qualified names use `::`, so `time::now` must stay one name — and a
// hypothetical namespace called `block` must not turn `block::f` into a
// request for the block named `:f`.
func splitHelpKind(s string) (kind, rest string) {
	i := strings.Index(s, ":")
	if i <= 0 || helpTopics == nil {
		return "", s
	}
	if strings.HasPrefix(s[i+1:], ":") {
		return "", s
	}
	for _, k := range helpTopics.HelpKinds() {
		if s[:i] == k {
			return k, s[i+1:]
		}
	}
	return "", s
}

// helpKindList names the accepted `kind:` prefixes, for a diagnostic.
func helpKindList() string {
	if helpTopics == nil {
		return ""
	}
	kinds := append([]string(nil), helpTopics.HelpKinds()...)
	sort.Strings(kinds)
	return strings.Join(kinds, ", ")
}
