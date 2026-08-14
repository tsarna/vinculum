// Package release checks the facts about a release that live outside the
// binary: the changelog, the version notes in doc/, and the sample versions
// pinned in files that quote one.
//
// The release itself is automated — a v* tag builds binaries, images, and a
// Homebrew cask, and lifts the release notes out of CHANGELOG.md. What is not
// automated is agreement: several files name a version, and nothing but this
// package notices when one of them names the wrong one. Each check below is a
// fact that has to be true before a tag is pushed, expressed so that the
// failure says which file to edit.
//
// Check runs against a checked-out tree. With releaseVersion empty it checks
// the invariants that hold at any commit; with a version (the tag being built)
// it additionally checks that the tree describes *that* release.
package release

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Problem is one inconsistency, phrased as what is wrong and where.
type Problem struct {
	// File is the repo-relative path to edit, or "" for a whole-tree problem.
	File string
	// Detail says what disagrees with what.
	Detail string
}

func (p Problem) String() string {
	if p.File == "" {
		return p.Detail
	}
	return p.File + ": " + p.Detail
}

// Check returns every inconsistency it finds, in a stable order. An empty
// result means the tree is ready to tag.
//
// releaseVersion is the version being released, with or without a leading "v",
// or "" to check only what must hold at every commit.
func Check(root, releaseVersion string) ([]Problem, error) {
	changelog, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		return nil, err
	}

	cl, problems := parseChangelog(string(changelog))
	if len(cl.released) == 0 {
		return append(problems, Problem{File: "CHANGELOG.md", Detail: "no released version sections found"}), nil
	}
	latest := cl.released[0]

	problems = append(problems, checkLinkRefs(cl)...)

	pending, docProblems := checkDocVersionNotes(root, cl)
	problems = append(problems, docProblems...)

	problems = append(problems, checkPinnedVersions(root, latest)...)

	if releaseVersion != "" {
		problems = append(problems, checkReleaseVersion(strings.TrimPrefix(releaseVersion, "v"), cl, pending)...)
	}

	sort.SliceStable(problems, func(i, j int) bool {
		if problems[i].File != problems[j].File {
			return problems[i].File < problems[j].File
		}
		return problems[i].Detail < problems[j].Detail
	})
	return problems, nil
}

// ---------------------------------------------------------------------------
// CHANGELOG.md
// ---------------------------------------------------------------------------

type changelog struct {
	// released holds every version with a section, newest first.
	released []string
	// dated reports whether a version's heading carries a date.
	dated map[string]bool
	// hasUnreleased reports whether an "## [Unreleased]" section exists.
	hasUnreleased bool
	// linkRefs maps a heading name ("0.44.0", "Unreleased") to its URL.
	linkRefs map[string]string
}

var (
	headingRe = regexp.MustCompile(`^## \[([^\]]+)\](?:\s+-\s+(\d{4}-\d{2}-\d{2}))?\s*$`)
	linkRefRe = regexp.MustCompile(`^\[([^\]]+)\]:\s*(\S+)\s*$`)
	compareRe = regexp.MustCompile(`/compare/v?([^.]+\.[^.]+\.[^.]+)\.\.\.(?:v?(.+))?$`)
)

func parseChangelog(text string) (changelog, []Problem) {
	cl := changelog{
		dated:    map[string]bool{},
		linkRefs: map[string]string{},
	}
	var problems []Problem

	var current string
	for _, line := range strings.Split(text, "\n") {
		if m := headingRe.FindStringSubmatch(line); m != nil {
			current = m[1]
			if current == "Unreleased" {
				cl.hasUnreleased = true
			} else {
				cl.released = append(cl.released, current)
				cl.dated[current] = m[2] != ""
			}
			continue
		}
		if m := linkRefRe.FindStringSubmatch(line); m != nil {
			cl.linkRefs[m[1]] = m[2]
			continue
		}
	}
	// Sections are written newest first, and the release notes are lifted from
	// the top one, so an out-of-order file would ship the wrong notes.
	for i := 1; i < len(cl.released); i++ {
		if compareVersions(cl.released[i-1], cl.released[i]) <= 0 {
			problems = append(problems, Problem{
				File:   "CHANGELOG.md",
				Detail: fmt.Sprintf("section [%s] is not newer than the [%s] section above it; sections must run newest first", cl.released[i-1], cl.released[i]),
			})
		}
	}
	return cl, problems
}

// checkLinkRefs verifies that every section has a link definition and that the
// compare ranges chain, which is the half of Keep a Changelog that is easy to
// forget because nothing renders it wrong.
func checkLinkRefs(cl changelog) []Problem {
	var problems []Problem

	for i, v := range cl.released {
		url, ok := cl.linkRefs[v]
		if !ok {
			problems = append(problems, Problem{
				File:   "CHANGELOG.md",
				Detail: fmt.Sprintf("section [%s] has no link definition at the bottom of the file", v),
			})
			continue
		}
		// The oldest section compares against a tag with no section of its
		// own, so only check the chain where both ends are sections here.
		if i+1 < len(cl.released) {
			prev := cl.released[i+1]
			if from, to := compareRange(url); from != prev || to != v {
				problems = append(problems, Problem{
					File:   "CHANGELOG.md",
					Detail: fmt.Sprintf("[%s] link should compare v%s...v%s, has %q", v, prev, v, url),
				})
			}
		}
	}

	if cl.hasUnreleased {
		url, ok := cl.linkRefs["Unreleased"]
		switch {
		case !ok:
			problems = append(problems, Problem{File: "CHANGELOG.md", Detail: "[Unreleased] has no link definition at the bottom of the file"})
		default:
			if from, to := compareRange(url); from != cl.released[0] || to != "HEAD" {
				problems = append(problems, Problem{
					File:   "CHANGELOG.md",
					Detail: fmt.Sprintf("[Unreleased] link should compare v%s...HEAD, has %q", cl.released[0], url),
				})
			}
		}
	}

	for name := range cl.linkRefs {
		if name == "Unreleased" {
			if !cl.hasUnreleased {
				problems = append(problems, Problem{File: "CHANGELOG.md", Detail: "[Unreleased] link definition remains but the section is gone"})
			}
			continue
		}
		if !cl.dated[name] && !contains(cl.released, name) {
			problems = append(problems, Problem{
				File:   "CHANGELOG.md",
				Detail: fmt.Sprintf("[%s] link definition has no matching section", name),
			})
		}
	}
	return problems
}

// compareRange pulls the two ends out of a GitHub compare URL.
func compareRange(url string) (from, to string) {
	m := compareRe.FindStringSubmatch(url)
	if m == nil {
		return "", ""
	}
	return m[1], m[2]
}

// ---------------------------------------------------------------------------
// doc/ version notes
// ---------------------------------------------------------------------------

// docNoteRe matches the "> **Changed in 0.45.0.**" admonitions the reference
// pages use to date a behavior change.
var docNoteRe = regexp.MustCompile(`\b(?:Added|Changed|Removed|Deprecated|New) in (\d+\.\d+\.\d+)\b`)

// checkDocVersionNotes verifies that every version a doc page names is either
// released or the one release being prepared, and returns that pending version
// if there is one.
//
// The pending version is inferred rather than declared, because it is already
// written down: a page that says "Changed in 0.45.0" before 0.45.0 exists *is*
// the statement of intent. Two different unreleased versions means two
// different intents, which is the bug this catches.
func checkDocVersionNotes(root string, cl changelog) (pending string, problems []Problem) {
	docDir := filepath.Join(root, "doc")
	entries, err := os.ReadDir(docDir)
	if err != nil {
		return "", []Problem{{File: "doc", Detail: err.Error()}}
	}

	unreleased := map[string][]string{} // version -> files naming it
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(docDir, e.Name()))
		if err != nil {
			problems = append(problems, Problem{File: "doc/" + e.Name(), Detail: err.Error()})
			continue
		}
		for _, m := range docNoteRe.FindAllStringSubmatch(string(body), -1) {
			v := m[1]
			if contains(cl.released, v) {
				continue
			}
			if compareVersions(v, cl.released[0]) <= 0 {
				problems = append(problems, Problem{
					File:   "doc/" + e.Name(),
					Detail: fmt.Sprintf("names version %s, which is not newer than the latest release (%s) and has no CHANGELOG section", v, cl.released[0]),
				})
				continue
			}
			unreleased[v] = appendUnique(unreleased[v], "doc/"+e.Name())
		}
	}

	switch len(unreleased) {
	case 0:
	case 1:
		for v := range unreleased {
			pending = v
		}
	default:
		versions := make([]string, 0, len(unreleased))
		for v := range unreleased {
			versions = append(versions, fmt.Sprintf("%s (%s)", v, strings.Join(unreleased[v], ", ")))
		}
		sort.Strings(versions)
		problems = append(problems, Problem{
			File:   "doc",
			Detail: "pages promise more than one unreleased version: " + strings.Join(versions, "; ") + " — they must agree on the next version",
		})
	}
	return pending, problems
}

// ---------------------------------------------------------------------------
// Files that quote a version
// ---------------------------------------------------------------------------

// pinned is a file carrying a version that has to track the latest release,
// with the pattern that finds it and a sentence saying why it is there.
var pinned = []struct {
	file    string
	pattern *regexp.Regexp
	why     string
}{
	{
		file:    "doc/schema.md",
		pattern: regexp.MustCompile(`"vinculumVersion":\s*"(\d+\.\d+\.\d+)"`),
		why:     "sample output quotes the version the document was generated by",
	},
	{
		file:    "testdata/plugin-smoke/go.mod",
		pattern: regexp.MustCompile(`github\.com/tsarna/vinculum v(\d+\.\d+\.\d+)`),
		why:     "the default the smoke gate builds against when run locally",
	},
}

func checkPinnedVersions(root, latest string) []Problem {
	var problems []Problem
	for _, p := range pinned {
		body, err := os.ReadFile(filepath.Join(root, p.file))
		if err != nil {
			problems = append(problems, Problem{File: p.file, Detail: err.Error()})
			continue
		}
		m := p.pattern.FindStringSubmatch(string(body))
		if m == nil {
			problems = append(problems, Problem{
				File:   p.file,
				Detail: fmt.Sprintf("no version found matching %s (%s)", p.pattern, p.why),
			})
			continue
		}
		if m[1] != latest {
			problems = append(problems, Problem{
				File:   p.file,
				Detail: fmt.Sprintf("pins %s, latest release is %s — %s", m[1], latest, p.why),
			})
		}
	}
	return problems
}

// ---------------------------------------------------------------------------
// Tag-time checks
// ---------------------------------------------------------------------------

// checkReleaseVersion verifies the tree describes the release being tagged.
// Run from the release workflows, where the version is the tag.
func checkReleaseVersion(version string, cl changelog, pending string) []Problem {
	var problems []Problem

	core, pre := splitPrerelease(version)
	if parseVersion(core) == nil {
		return []Problem{{Detail: fmt.Sprintf("release version %q is not X.Y.Z, optionally with a -prerelease suffix", version)}}
	}

	latest := cl.released[0]
	switch {
	case pre != "":
		// A prerelease is a rehearsal of the pipeline, and is cut either
		// before or after the tree is prepared — so accept both. What it may
		// not be is a rehearsal of a version nothing describes, which would
		// rehearse the wrong release notes.
		if latest != core && pending != core {
			problems = append(problems, Problem{
				Detail: fmt.Sprintf("%s is a prerelease of %s, which neither the newest changelog section (%s) nor doc/ describes", version, core, latest),
			})
		}
	case latest != core:
		problems = append(problems, Problem{
			File:   "CHANGELOG.md",
			Detail: fmt.Sprintf("newest section is [%s], but the release being built is %s — rename the [Unreleased] section before tagging", latest, core),
		})
	case !cl.dated[core]:
		problems = append(problems, Problem{
			File:   "CHANGELOG.md",
			Detail: fmt.Sprintf("section [%s] carries no date; the heading should read \"## [%s] - YYYY-MM-DD\"", core, core),
		})
	}

	// An empty [Unreleased] section is deliberate here: the release commit
	// renames the old one and leaves a fresh empty heading behind, which is
	// what v0.44.0 and every tag before it look like.

	if pending != "" && pending != core {
		problems = append(problems, Problem{
			File:   "doc",
			Detail: fmt.Sprintf("pages promise changes in %s, but the release being built is %s", pending, core),
		})
	}
	return problems
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// splitPrerelease separates a tag's core version from any -rc1 / +build
// suffix, so a rehearsal tag is measured against the release it rehearses.
func splitPrerelease(v string) (core, pre string) {
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

// parseVersion returns the three components of an X.Y.Z version, or nil.
func parseVersion(v string) []int {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return nil
	}
	out := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out[i] = n
	}
	return out
}

// compareVersions orders two X.Y.Z versions, treating an unparseable one as
// lowest so a malformed heading sorts rather than panics.
func compareVersions(a, b string) int {
	av, bv := parseVersion(a), parseVersion(b)
	switch {
	case av == nil && bv == nil:
		return strings.Compare(a, b)
	case av == nil:
		return -1
	case bv == nil:
		return 1
	}
	for i := range av {
		if av[i] != bv[i] {
			if av[i] < bv[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

func appendUnique(list []string, v string) []string {
	if contains(list, v) {
		return list
	}
	return append(list, v)
}
