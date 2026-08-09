package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/config"
)

// docDir is doc/ relative to this package.
const docDir = "../doc"

// TestDocPagesResolve checks that every DocPage in the schema names a file that
// exists and, where it carries a fragment, a heading that is in that file.
//
// This is the whole reason DocPage exists rather than a naming convention. A
// generated index has to link somewhere; a target derived by convention breaks
// silently the first time a page is renamed, which is exactly the class of
// drift the schema was built to prevent. Here the drift fails the build.
func TestDocPagesResolve(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})

	pages := map[string][]string{} // DocPage -> paths that reference it
	for _, blockType := range sortedKeys(doc.Blocks) {
		block := doc.Blocks[blockType]
		if block.DocPage != "" {
			pages[block.DocPage] = append(pages[block.DocPage], blockType)
		}
		if block.Body != nil {
			collectDocPages(blockType, block.Body, pages)
		}
		for _, variant := range sortedKeys(block.Variants) {
			collectDocPages(blockType+" "+variant, block.Variants[variant], pages)
		}
	}
	for _, name := range sortedKeys(doc.Namespaces) {
		if page := doc.Namespaces[name].DocPage; page != "" {
			pages[page] = append(pages[page], name)
		}
	}
	require.NotEmpty(t, pages, "no DocPage is set anywhere; the check would pass vacuously")

	headings := map[string]map[string]bool{} // file -> anchors
	for page, refs := range pages {
		file, fragment, _ := strings.Cut(page, "#")

		path := filepath.Join(docDir, file)
		data, err := os.ReadFile(path)
		if !assert.NoError(t, err, "%s: DocPage %q names a file that does not exist", refs, page) {
			continue
		}
		if fragment == "" {
			continue
		}

		if headings[file] == nil {
			headings[file] = anchorsIn(string(data))
		}
		assert.True(t, headings[file][fragment],
			"%s: DocPage %q names a heading that is not in %s", refs, page, file)
	}
}

// TestEveryVariantHasADocPage is the other half: a type with no reference page
// is one a generated index cannot link to.
func TestEveryVariantHasADocPage(t *testing.T) {
	doc := generateTestSchema(t, config.SchemaGenOptions{})

	for _, blockType := range sortedKeys(doc.Blocks) {
		for _, variant := range sortedKeys(doc.Blocks[blockType].Variants) {
			body := doc.Blocks[blockType].Variants[variant]
			if body.Undocumented {
				continue // already reported by the schema's own coverage check
			}
			assert.NotEmpty(t, body.DocPage, "%s %q has no DocPage", blockType, variant)
		}
	}
}

// sortedKeys is keysOf with a stable order, so the assertions report the same
// first failure on every run.
func sortedKeys[V any](m map[string]V) []string {
	keys := keysOf(m)
	sort.Strings(keys)
	return keys
}

func collectDocPages(path string, body *config.SchemaBody, into map[string][]string) {
	if body.DocPage != "" {
		into[body.DocPage] = append(into[body.DocPage], path)
	}
	for _, name := range sortedKeys(body.Blocks) {
		collectDocPages(path+"."+name, &body.Blocks[name].SchemaBody, into)
	}
}

var headingRe = regexp.MustCompile(`(?m)^#{1,6}\s+(.*)$`)

// anchorsIn returns the set of GitHub heading anchors in a Markdown document.
//
// GitHub lowercases the heading, drops everything that is not a letter, digit,
// space, hyphen, or underscore, and turns spaces into hyphens. That is enough
// for the headings in doc/ — which are plain text and inline code — and being
// approximate is acceptable in the direction it errs: a heading this
// under-generates simply fails the check, which is a nudge to look, not a
// silently accepted bad link.
func anchorsIn(markdown string) map[string]bool {
	anchors := map[string]bool{}
	inFence := false

	for _, line := range strings.Split(markdown, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue // a comment in a code block is not a heading
		}
		m := headingRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		anchors[anchorFor(m[1])] = true
	}
	return anchors
}

func anchorFor(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(heading)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return b.String()
}
