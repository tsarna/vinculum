package repl

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/internal/pager"
	"github.com/tsarna/vinculum/internal/schemadoc"
)

// :man — the reference, at the prompt.
//
// help() already answers the same questions and returns a string, which is the
// right shape for an expression but the wrong one for reading: the value is
// echoed through the result formatter, quoted and bound to _N, and a long page
// scrolls off the top. :man renders the same walk straight to the terminal and
// pages it.
//
// It is spelled :man rather than :help because the engine owns :help and
// dispatches it before host commands, so it cannot be extended here — and
// because sharing the word with the shell command is worth more than the
// alternative anyway: what you learn to type at the prompt is what you type in
// a terminal.

// cmdMan renders a topic, an index, or the menu that resolves an ambiguous name.
//
// Everything it prints goes to stdout, including the diagnostics. `vinculum man`
// sends those to stderr so that redirecting one lookup cannot capture a menu,
// but a REPL session has no per-command redirection — what it has is
// doc/repl.md's suggested `2>vinculum.log`, which would swallow the answer.
func (h *host) cmdMan(args []string, _ io.Writer) bool {
	h.manTo(os.Stdout, args)
	return false
}

// manTo is cmdMan with its destination named, so a test can read what it wrote.
func (h *host) manTo(out io.Writer, args []string) {
	kind, path, err := parseManArgs(args)
	if err != nil {
		fmt.Fprintln(out, err)
		return
	}

	// Curation problems belong to `vinculum schema --strict`; render what is
	// there rather than refusing to answer.
	doc, _ := config.GenerateSchema(config.SchemaGenOptions{})

	if len(path) == 0 {
		h.showMan(out, append(
			schemadoc.Index(doc, schemadoc.WalkOptions{}),
			schemadoc.Usage(manExamples...),
		))
		return
	}

	// Both corpora: the config language, and the functions of this session's own
	// eval context — which, unlike the command's, is the live one, so a function
	// a plugin or a .cty file contributed is documented here too.
	candidates := append(
		schemadoc.Resolve(doc, kind, path),
		schemadoc.ResolveFuncs(h.cfg, kind, path)...,
	)

	switch len(candidates) {
	case 1:
		h.showMan(out, schemadoc.Walk(candidates[0], schemadoc.WalkOptions{}))
	case 0:
		h.manNotFound(out, doc, kind, path)
	default:
		h.showMan(out, []schemadoc.Event{schemadoc.MenuFor(path, candidates, ManSpeller)})
	}
}

// manExamples are the invocations the index footer suggests, in this front
// door's idiom.
var manExamples = []string{
	":man subscription",
	":man client mqtt",
	":man server http handle",
	":man send",
}

// parseManArgs splits an optional leading `kind:` off the first word.
//
// A REPL line is already split on spaces, so there is nowhere to put a --type
// flag without inventing flag parsing for one command. The prefix is the same
// spelling help() accepts, which is one fewer thing to learn.
func parseManArgs(args []string) (kind schemadoc.Kind, path []string, err error) {
	path = make([]string, 0, len(args))
	for _, a := range args {
		if a = strings.TrimSpace(a); a != "" {
			path = append(path, a)
		}
	}
	if len(path) == 0 {
		return "", nil, nil
	}

	name, rest, found := strings.Cut(path[0], ":")
	if !found || strings.HasPrefix(rest, ":") {
		// No prefix, or a functy qualified name (`time::now`), which is one word.
		return "", path, nil
	}
	if !schemadoc.ValidKind(name) {
		return "", nil, fmt.Errorf("unknown kind %q in %q (want one of %s)", name, path[0], manKindList())
	}
	if rest == "" {
		return "", nil, fmt.Errorf("%q names a kind but no topic", path[0])
	}
	path[0] = rest
	return schemadoc.Kind(name), path, nil
}

func (h *host) manNotFound(out io.Writer, doc *config.SchemaDocument, kind schemadoc.Kind, path []string) {
	fmt.Fprintf(out, "no topic named %q\n\n", strings.Join(path, " "))

	near := schemadoc.Suggest(doc, kind, path)
	near = append(near, schemadoc.SuggestFuncs(h.cfg, kind, path)...)
	if len(near) == 0 {
		fmt.Fprintln(out, "Type :man for a list of topics.")
		return
	}
	h.showMan(out, []schemadoc.Event{schemadoc.SuggestMenu(near, ManSpeller)})
}

// showMan renders for the terminal and pages.
//
// Paging is safe from here: the line editor puts the terminal in raw mode only
// while it is reading a line, and its reader goroutine is parked between lines,
// so the pager owns the terminal and stdin for as long as it runs.
func (h *host) showMan(out io.Writer, events []schemadoc.Event) {
	text := schemadoc.RenderTerm(events, schemadoc.TermOptionsFor(out))
	if err := pager.Page(out, text, pager.Options{}); err != nil {
		fmt.Fprintln(out, err)
	}
}

// ManSpeller spells a candidate as a :man invocation, for a menu printed at the
// prompt. Qualifying uses the `kind:` prefix, since a meta-command line has
// nowhere to put a flag.
func ManSpeller(kind schemadoc.Kind, path []string, qualify bool) string {
	words := append([]string(nil), path...)
	if qualify && len(words) > 0 {
		words[0] = string(kind) + ":" + words[0]
	}
	return ":man " + strings.Join(words, " ")
}

func manKindList() string {
	names := make([]string, 0, len(schemadoc.Kinds))
	for _, k := range schemadoc.Kinds {
		names = append(names, string(k))
	}
	return strings.Join(names, ", ")
}
