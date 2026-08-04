package schemadoc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
)

func TestHelpResolverIsRegistered(t *testing.T) {
	// The init() in this package is what turns help() on for topics; if it
	// stops running, help("subscription") silently goes back to null.
	var r config.HelpTopicResolver = helpResolver{}
	assert.NotEmpty(t, r.HelpKinds())
}

// "function" is deliberately not offered as a kind: inside help(), functions
// are functy's to answer, and routing them here would reach a resolver with no
// catalog to search.
func TestHelpKindsExcludeFunctions(t *testing.T) {
	assert.Equal(t, []string{"block", "context"}, helpResolver{}.HelpKinds())
	assert.Contains(t, Kinds, KindFunction, "but man --type function still resolves")
}

// Against the real schema, which in this package's test binary carries the
// blocks config itself registers — subscription among them — but not the
// client and server variants that need their subsystems linked.
func TestHelpTopicRendersABlock(t *testing.T) {
	got, ok := helpResolver{}.HelpTopic("", []string{"subscription"})
	require.True(t, ok)

	assert.Contains(t, got, "subscription")
	assert.Contains(t, got, "Attributes")
	// Plain text: no ANSI, and no Markdown table pipes.
	assert.NotContains(t, got, "\x1b[")
	assert.NotContains(t, got, "|---|")
}

func TestHelpTopicReportsWhatItCannotFind(t *testing.T) {
	_, ok := helpResolver{}.HelpTopic("", []string{"no_such_block_xyz"})
	assert.False(t, ok, "false is what lets help() return null")

	// A kind that finds nothing is the same answer.
	_, ok = helpResolver{}.HelpTopic("context", []string{"subscription"})
	assert.False(t, ok)
}

// An ambiguous path is answered with the menu rather than refused: returning
// false would make it indistinguishable from naming nothing at all.
func TestHelpTopicAnswersAmbiguityWithAMenu(t *testing.T) {
	doc := testDoc()
	candidates := Resolve(doc, "", []string{"http"})
	require.Len(t, candidates, 2, "the fixture has http as both a client and a server type")

	menu := MenuFor([]string{"http"}, candidates, HelpSpeller)
	assert.Equal(t, `"http" is ambiguous, choose one of:`, menu.Intro)
	assert.Equal(t, []string{`help("client", "http")`, `help("server", "http")`}, menu.Items)
}

// The menu has to be written in the idiom of the front door printing it: a
// reader inside an expression cannot type a shell command.
func TestHelpSpeller(t *testing.T) {
	assert.Equal(t, `help("subscription")`, HelpSpeller(KindBlock, []string{"subscription"}, false))
	assert.Equal(t, `help("client", "mqtt")`, HelpSpeller(KindBlock, []string{"client", "mqtt"}, false))

	// Qualifying uses the kind: prefix, because a call has nowhere to put a flag.
	assert.Equal(t, `help("block:assert")`, HelpSpeller(KindBlock, []string{"assert"}, true))
	assert.Equal(t, `help("context:message")`, HelpSpeller(KindContext, []string{"message"}, true))

	// Quoting is Go's, so a name needing an escape gets one.
	assert.Equal(t, `help("a\"b")`, HelpSpeller(KindBlock, []string{`a"b`}, false))
}
