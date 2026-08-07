package schemadoc

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tsarna/vinculum/config"
)

// Keyword search over the whole reference.
//
// Resolution is deliberately narrow: a path names one topic, and attribute
// names are not matched globally because `action` appears in dozens of bodies
// and a menu of dozens is not a menu (see LeadingNames). That leaves the 807
// attributes, 74 nested bodies and 193 `ctx` fields findable only by someone who
// already knows which block owns them, which is the wrong way round — a name is
// exactly what a reader arrives with.
//
// Apropos is the other half: it searches every name and every summary, and
// answers with the canonical invocation that reads each hit. Nothing becomes
// resolvable that was not resolvable before; a hit that names a field of a
// larger topic points at the topic and says which field matched.

// Hit is one search result: what matched, and the topic to read.
type Hit struct {
	Kind Kind
	// Path is the argv that names the topic to read, which is the matched thing
	// itself wherever the matched thing is addressable.
	Path []string
	// Detail spells the matched thing when Path names its container rather than
	// it — `ctx.topic` for a field of a context shape. Empty otherwise.
	Detail string
	// Summary is the one-line description of whatever matched.
	Summary string

	// nameMatched records that the query hit the name rather than only the
	// summary, which is what sorts a hit to the top.
	nameMatched bool
}

// Apropos returns every topic whose name or summary contains all of terms,
// best match first.
//
// Both corpora are searched — the config-language document and, when a catalog
// is supplied, the callable functions — because a reader searching for "topic"
// has no reason to know which of the two owns the answer. kind restricts the
// search the way `--type` restricts resolution.
func Apropos(doc *config.SchemaDocument, cat FuncCatalog, kind Kind, terms []string) []Hit {
	needles := make([]string, 0, len(terms))
	for _, t := range terms {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			needles = append(needles, t)
		}
	}
	if len(needles) == 0 {
		return nil
	}

	s := &search{needles: needles}
	if kind == "" || kind == KindBlock {
		s.blocks(doc)
	}
	if kind == "" || kind == KindContext {
		s.contexts(doc)
	}
	if kind == "" || kind == KindFunction {
		s.functions(cat)
	}

	// Name matches first: someone who typed a name wants the thing with that
	// name, not the dozen whose prose mentions it. Everything below that is
	// tie-breaking, so that the same query always prints the same page.
	sort.SliceStable(s.hits, func(i, j int) bool {
		a, b := s.hits[i], s.hits[j]
		if a.nameMatched != b.nameMatched {
			return a.nameMatched
		}
		if ka, kb := kindOrder(a.Kind), kindOrder(b.Kind); ka != kb {
			return ka < kb
		}
		if pa, pb := strings.Join(a.Path, " "), strings.Join(b.Path, " "); pa != pb {
			return pa < pb
		}
		return a.Detail < b.Detail
	})
	return s.hits
}

type search struct {
	needles []string
	hits    []Hit
}

// consider records a hit when every term matches the name or the summary.
//
// The terms are ANDed so that a multi-word query narrows rather than widens:
// `-k keep alive` should find `keep_alive`, and would otherwise return every
// topic mentioning either word.
func (s *search) consider(kind Kind, path []string, detail, name, summary string) {
	lowerName := strings.ToLower(name)
	haystack := lowerName + " " + strings.ToLower(summary)

	nameMatched := false
	for _, n := range s.needles {
		if !strings.Contains(haystack, n) {
			return
		}
		if strings.Contains(lowerName, n) {
			nameMatched = true
		}
	}

	s.hits = append(s.hits, Hit{
		Kind:        kind,
		Path:        append([]string(nil), path...),
		Detail:      detail,
		Summary:     summary,
		nameMatched: nameMatched,
	})
}

func (s *search) blocks(doc *config.SchemaDocument) {
	if doc == nil {
		return
	}
	for _, name := range sortedBlockKeys(doc.Blocks) {
		block := doc.Blocks[name]
		path := []string{name}

		summary := block.Summary
		if summary == "" && block.Body != nil {
			summary = block.Body.Summary
		}
		s.consider(KindBlock, path, "", name, summary)

		if block.Body != nil {
			s.body(path, block.Body)
		}
		for _, variant := range sortedBodyNames(block.Variants) {
			body := block.Variants[variant]
			vpath := []string{name, variant}
			// A variant matches on its own label — `mqtt`, not `client mqtt` —
			// because that is the word a reader searching for it knows.
			s.consider(KindBlock, vpath, "", variant, body.Summary)
			s.body(vpath, body)
		}
	}
}

// body searches one body's attributes and recurses into its sub-blocks.
func (s *search) body(path []string, body *config.SchemaBody) {
	if body == nil {
		return
	}
	for _, attr := range body.Attributes {
		apath := append(append([]string(nil), path...), attr.Name)
		s.consider(KindBlock, apath, "", attr.Name, attr.Summary)

		// Fields this site adds to an open `ctx` shape are named nowhere else:
		// `mqtt_topic` exists only as a contribution of one attribute, so the
		// attribute is where a search for it has to land.
		for _, f := range attr.ContextFields {
			s.consider(KindBlock, apath, "ctx."+f.Name, f.Name, f.Summary)
		}
	}
	for _, name := range sortedBlockNames(body.Blocks) {
		nested := body.Blocks[name]
		npath := append(append([]string(nil), path...), name)
		s.consider(KindBlock, npath, "", name, nested.Summary)
		s.body(npath, &nested.SchemaBody)
	}
}

func (s *search) contexts(doc *config.SchemaDocument) {
	if doc == nil {
		return
	}
	for _, name := range sortedContextKeys(doc.Contexts) {
		shape := doc.Contexts[name]
		path := []string{name}
		s.consider(KindContext, path, "", name, shape.Summary)

		// A field is not addressable on its own, so its hit names the shape and
		// says which field matched. Universal fields are skipped: every shape
		// carries them, so matching one would return all 26 shapes and bury
		// whatever else the query found.
		for _, f := range shape.Fields {
			if f.Universal {
				continue
			}
			s.consider(KindContext, path, "ctx."+f.Name, f.Name, f.Summary)
		}
	}
}

func (s *search) functions(cat FuncCatalog) {
	if cat == nil {
		return
	}
	for _, name := range cat.FuncNames() {
		doc, ok := cat.FuncDoc(name)
		if !ok {
			continue
		}
		s.consider(KindFunction, []string{name}, "", name, firstSentence(doc.Doc))
	}
}

// firstSentence is a function's summary. FuncDoc carries prose rather than a
// one-line summary — a function's documentation is written for `help()`, which
// prints all of it — so the lead sentence stands in.
//
// A sentence rather than a line, because a curated Doc wraps where the source
// file wrapped and cutting there would truncate mid-clause; and a sentence
// rather than the whole paragraph, because several run to three hundred
// characters and one row of a listing may not be four lines of it.
//
// go/doc.Synopsis does exactly this and is not used: it is deprecated, and it
// ends the sentence at any period-space, so `typeof`'s "(e.g. list(string))"
// becomes "(e.g.".
func firstSentence(doc string) string {
	// The lead paragraph, unwrapped: a blank line ends it, and the newlines
	// inside it are where the author's editor wrapped rather than meaning.
	if i := strings.Index(doc, "\n\n"); i >= 0 {
		doc = doc[:i]
	}
	doc = strings.Join(strings.Fields(doc), " ")

	for i, r := range doc {
		if r != '.' && r != '?' && r != '!' {
			continue
		}
		rest := doc[i+1:]
		if !strings.HasPrefix(rest, " ") {
			continue
		}
		// A new sentence starts with a capital. Anything else after the period
		// makes it an abbreviation or a decimal rather than a full stop, which
		// is what keeps "e.g. list(string)" and "e.g. 1700000000000-0" whole.
		next := strings.TrimLeft(rest, " ")
		if next != "" && next[0] >= 'A' && next[0] <= 'Z' {
			return doc[:i+1]
		}
	}
	return doc
}

func kindOrder(k Kind) int {
	for i, want := range Kinds {
		if k == want {
			return i
		}
	}
	return len(Kinds)
}

// ResultsFor renders hits as the event a sink prints, spelling each hit in the
// idiom of the front door that is asking — a shell command for `vinculum man`,
// a meta-command for the REPL — the same way a Menu is spelled.
//
// Every printed command must read the hit it is printed against, so a name that
// means something in two kinds — `assert` is a block type and a function — is
// spelled with its kind. Unqualified it would resolve to the ambiguity menu
// instead of to the row the reader is pointing at.
func ResultsFor(terms []string, hits []Hit, spell Speller) Results {
	kinds := map[string]Kind{}
	crossKind := map[string]bool{}
	for _, h := range hits {
		plain := spell(h.Kind, h.Path, false)
		if seen, ok := kinds[plain]; ok && seen != h.Kind {
			crossKind[plain] = true
		}
		kinds[plain] = h.Kind
	}

	rows := make([]ResultRow, 0, len(hits))
	for _, h := range hits {
		rows = append(rows, ResultRow{
			Command: spell(h.Kind, h.Path, crossKind[spell(h.Kind, h.Path, false)]),
			Detail:  h.Detail,
			Summary: h.Summary,
		})
	}
	return Results{
		Intro: fmt.Sprintf("%d topics match %q:", len(hits), strings.Join(terms, " ")),
		Rows:  rows,
	}
}
