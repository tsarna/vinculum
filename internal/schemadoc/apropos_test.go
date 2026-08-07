package schemadoc

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
)

// find returns the one hit whose path and detail match, so an assertion names
// what it is looking for rather than an index into a result order.
func find(t *testing.T, hits []Hit, detail string, path ...string) Hit {
	t.Helper()
	for _, h := range hits {
		if strings.Join(h.Path, " ") == strings.Join(path, " ") && h.Detail == detail {
			return h
		}
	}
	t.Fatalf("no hit for %q %q in:\n%s", strings.Join(path, " "), detail, formatHits(hits))
	return Hit{}
}

func formatHits(hits []Hit) string {
	var b strings.Builder
	for _, h := range hits {
		b.WriteString("  " + strings.Join(h.Path, " "))
		if h.Detail != "" {
			b.WriteString(" [" + h.Detail + "]")
		}
		b.WriteString(" — " + h.Summary + "\n")
	}
	return b.String()
}

// An attribute name is the thing a reader arrives with and the thing resolution
// deliberately refuses to match globally. Finding one without knowing its block
// is the whole reason this exists.
func TestAproposFindsAnAttributeByBareName(t *testing.T) {
	hits := Apropos(testDoc(), nil, "", []string{"broker"})

	h := find(t, hits, "", "client", "mqtt", "broker")
	assert.Equal(t, KindBlock, h.Kind)
	assert.Equal(t, "Broker URL.", h.Summary)
}

// Every part of the corpus is searchable, or the answer depends on which part
// of the language a reader happened to ask about.
func TestAproposSearchesEveryPartOfTheCorpus(t *testing.T) {
	doc := testDoc()
	cat := testCatalog()

	for _, tc := range []struct {
		what   string
		term   string
		path   []string
		detail string
	}{
		{"a top-level block", "subscribes to messages", []string{"subscription"}, ""},
		{"a type variant", "MQTT 5.0", []string{"client", "mqtt"}, ""},
		{"a nested sub-block", "tls", []string{"client", "mqtt", "tls"}, ""},
		{"an attribute of a nested sub-block", "cert_file", []string{"client", "mqtt", "tls", "cert_file"}, ""},
		{"a doubly nested sub-block", "route handler", []string{"server", "http", "handle"}, ""},
		{"a ctx shape", "decode-error", []string{"decode-error"}, ""},
		{"a ctx field", "Decoded payload", []string{"message"}, "ctx.msg"},
		{"a site-added ctx field", "mqtt_topic", []string{"client", "mqtt", "on_decode_error"}, "ctx.mqtt_topic"},
		{"a function", "Reads a timestamp", []string{"parsetime"}, ""},
	} {
		t.Run(tc.what, func(t *testing.T) {
			find(t, Apropos(doc, cat, "", []string{tc.term}), tc.detail, tc.path...)
		})
	}
}

// A multi-word query narrows. ANDing is what makes `-k topic pattern` mean the
// thing that is both, rather than everything that is either.
func TestAproposAndsItsTerms(t *testing.T) {
	doc := testDoc()

	both := Apropos(doc, nil, "", []string{"decode", "payload"})
	require.NotEmpty(t, both)
	for _, h := range both {
		hay := strings.ToLower(strings.Join(h.Path, " ") + " " + h.Detail + " " + h.Summary)
		assert.Contains(t, hay, "decode")
		assert.Contains(t, hay, "payload")
	}

	// Each term alone finds strictly more than the two together do.
	assert.Greater(t, len(Apropos(doc, nil, "", []string{"decode"})), len(both))

	// A term that matches nothing takes the whole query with it.
	assert.Empty(t, Apropos(doc, nil, "", []string{"decode", "zzzznope"}))
}

// Someone who typed a name wants the thing with that name, not the dozen whose
// prose mentions it.
func TestAproposRanksNameMatchesFirst(t *testing.T) {
	hits := Apropos(testDoc(), nil, "", []string{"tls"})
	require.NotEmpty(t, hits)

	// The `tls` sub-block matches by name; the attributes below it match only
	// because their summaries say TLS.
	assert.Equal(t, []string{"client", "mqtt", "tls"}, hits[0].Path)

	// Every name match precedes every summary-only one, so the flags read as a
	// run of true followed by a run of false.
	seenSummaryOnly := false
	for _, h := range hits {
		byName := h.nameMatched
		if seenSummaryOnly {
			assert.False(t, byName,
				"a name match sorted below a summary match:\n%s", formatHits(hits))
		}
		seenSummaryOnly = seenSummaryOnly || !byName
	}
}

func TestAproposIsCaseInsensitive(t *testing.T) {
	assert.Equal(t,
		Apropos(testDoc(), nil, "", []string{"BROKER"}),
		Apropos(testDoc(), nil, "", []string{"broker"}))
}

// A ctx field is not addressable on its own, so its hit names the shape that
// carries it and says which field matched. Nothing becomes resolvable that was
// not resolvable before.
func TestAproposNamesTheShapeForAContextField(t *testing.T) {
	h := find(t, Apropos(testDoc(), nil, "", []string{"msg"}), "ctx.msg", "message")
	assert.Equal(t, KindContext, h.Kind)
	assert.Equal(t, "Decoded payload.", h.Summary)
}

// Universal fields are on all 26 shapes, so matching one would return every
// shape in the language and bury whatever else the query found.
func TestAproposSkipsUniversalContextFields(t *testing.T) {
	hits := Apropos(testDoc(), nil, "", []string{"trace_id"})
	for _, h := range hits {
		assert.NotEqual(t, "ctx.trace_id", h.Detail,
			"a universal field matched, which returns every shape:\n%s", formatHits(hits))
	}
}

func TestAproposRestrictsToAKind(t *testing.T) {
	doc, cat := testDoc(), testCatalog()

	for _, tc := range []struct {
		kind Kind
		want Kind
	}{{KindBlock, KindBlock}, {KindContext, KindContext}, {KindFunction, KindFunction}} {
		hits := Apropos(doc, cat, tc.kind, []string{"message"})
		require.NotEmpty(t, hits, "kind %q found nothing", tc.kind)
		for _, h := range hits {
			assert.Equal(t, tc.want, h.Kind)
		}
	}
}

func TestAproposWithNoUsableTermsFindsNothing(t *testing.T) {
	assert.Empty(t, Apropos(testDoc(), testCatalog(), "", nil))
	assert.Empty(t, Apropos(testDoc(), testCatalog(), "", []string{"  "}))
}

func TestAproposToleratesANilDocumentAndCatalog(t *testing.T) {
	assert.Empty(t, Apropos(nil, nil, "", []string{"broker"}))
}

// The contract of the output: every printed command reads the row it is printed
// against. A row that resolves to an ambiguity menu, or to nothing, is a row
// that lied.
func TestEveryResultRowIsAWorkingInvocation(t *testing.T) {
	doc, cat := testDoc(), testCatalog()

	for _, term := range []string{"a", "e", "message", "http", "topic", "client"} {
		for _, h := range Apropos(doc, cat, "", []string{term}) {
			candidates := append(
				Resolve(doc, h.Kind, h.Path),
				ResolveFuncs(cat, h.Kind, h.Path)...,
			)
			assert.Len(t, candidates, 1,
				"%q: `%s` does not resolve to exactly one topic",
				term, strings.Join(h.Path, " "))
		}
	}
}

// The same word in two kinds spells the same command, which would resolve to
// the menu rather than to either row. The kind has to be named.
func TestResultsForQualifiesACrossKindName(t *testing.T) {
	doc := testDoc()
	// `message` is a ctx shape in the fixture; make it a function name too.
	cat := fakeCatalog{docs: map[string]config.FuncDoc{
		"message": {Name: "message", Doc: "Builds a message."},
	}}

	res := ResultsFor([]string{"message"}, Apropos(doc, cat, "", []string{"message"}), CommandSpeller)

	var commands []string
	for _, r := range res.Rows {
		commands = append(commands, r.Command)
	}
	assert.Contains(t, commands, "vinculum man --type context message")
	assert.Contains(t, commands, "vinculum man --type function message")
	assert.NotContains(t, commands, "vinculum man message")

	// Only the collision pays for the qualifier; everything else stays short.
	plain := ResultsFor([]string{"broker"}, Apropos(doc, nil, "", []string{"broker"}), CommandSpeller)
	require.NotEmpty(t, plain.Rows)
	for _, r := range plain.Rows {
		assert.NotContains(t, r.Command, "--type")
	}
}

func TestResultsForCountsAndSpellsInTheCallersIdiom(t *testing.T) {
	hits := Apropos(testDoc(), nil, "", []string{"broker"})
	res := ResultsFor([]string{"broker"}, hits, func(_ Kind, path []string, _ bool) string {
		return ":man " + strings.Join(path, " ")
	})

	assert.Equal(t, `1 topics match "broker":`, res.Intro)
	assert.Equal(t, ":man client mqtt broker", res.Rows[0].Command)
}

// The two sinks that can be reached with a search result.

func TestTermRendersResults(t *testing.T) {
	out := RenderTerm([]Event{ResultsFor(
		[]string{"msg"},
		Apropos(testDoc(), nil, "", []string{"msg"}),
		CommandSpeller,
	)}, TermOptions{Width: 78})

	assert.Contains(t, out, `topics match "msg"`)
	assert.Contains(t, out, "vinculum man message")
	// The matched field leads the description, since the command names the shape.
	assert.Contains(t, out, "ctx.msg — Decoded payload.")
	// No Markdown survives.
	assert.NotContains(t, out, "|")
	assert.NotContains(t, out, "`")
	for _, line := range strings.Split(out, "\n") {
		assert.LessOrEqual(t, len([]rune(line)), 78, "line overflows the width: %q", line)
	}
}

func TestMarkdownRendersResultsAsATable(t *testing.T) {
	out := RenderMarkdown([]Event{ResultsFor(
		[]string{"msg"},
		Apropos(testDoc(), nil, "", []string{"msg"}),
		CommandSpeller,
	)}, MarkdownOptions{})

	assert.Contains(t, out, "| Topic | Description |")
	assert.Contains(t, out, "| `vinculum man message` | `ctx.msg` — Decoded payload. |")
}

func TestSinksIgnoreAnEmptyResultSet(t *testing.T) {
	assert.Empty(t, strings.TrimSpace(RenderTerm([]Event{Results{}}, TermOptions{Width: 78})))
	assert.Empty(t, strings.TrimSpace(RenderMarkdown([]Event{Results{}}, MarkdownOptions{})))
}

func TestFirstSentenceIsAFunctionsSummary(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{
			"stops at the first sentence",
			"Sends a message. Returns true on success.",
			"Sends a message.",
		},
		{
			// A curated Doc wraps where its source file wrapped, so a line is
			// not a unit of meaning and cutting at one truncates mid-clause.
			"unwraps the lead paragraph",
			"Sends a message to a bus subscriber\nunder a topic.\n\nMore about it.",
			"Sends a message to a bus subscriber under a topic.",
		},
		{
			"keeps a paragraph with no sentence end whole",
			"Returns the type of a value in functy's annotation grammar",
			"Returns the type of a value in functy's annotation grammar",
		},
		{
			// The case go/doc.Synopsis gets wrong.
			"does not end a sentence at an abbreviation",
			"Returns the type (e.g. list(string), object({})). And more.",
			"Returns the type (e.g. list(string), object({})).",
		},
		{
			"does not end a sentence at a decimal",
			"Redis entry ID, e.g. 1700000000000-0.",
			"Redis entry ID, e.g. 1700000000000-0.",
		},
		{"a question ends one too", "What is it? It is a thing.", "What is it?"},
		{"nothing", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, firstSentence(tc.in))
		})
	}
}
