// Package suggest measures how close a mistyped name is to the real ones, so
// that every "did you mean" Vinculum offers draws the line in the same place.
//
// Two shapes of caller share it. `vinculum man` answers a topic that did not
// resolve with a menu of near misses; the deferred-reference check answers a
// function that does not exist with the single closest name. Both ask the same
// question of the same measure, and a name either side would offer is a name
// the other would too.
package suggest

import (
	"sort"
	"strings"
)

// MaxDistance is how far a typo may stray and still be guessed at. Two covers a
// transposition plus a dropped character; three starts suggesting words that
// merely rhyme. It is also hclsyntax's own threshold, so a suggestion Vinculum
// makes about a name and one hcl makes about the same name agree.
const MaxDistance = 2

// Near returns the candidates within MaxDistance of name, closest first and
// then alphabetically — so the answer does not depend on the order the
// candidates arrived in, which is a map iteration as often as not.
//
// Comparison folds case, which makes a capitalization mistake a near miss
// rather than a distance away, but the candidates come back spelled as they
// were given: the whole point is to show the author the real name.
func Near(name string, candidates []string) []string {
	folded := strings.ToLower(name)

	type scored struct {
		name string
		dist int
	}
	var near []scored
	for _, candidate := range candidates {
		if d := Distance(folded, strings.ToLower(candidate)); d <= MaxDistance {
			near = append(near, scored{candidate, d})
		}
	}
	sort.Slice(near, func(i, j int) bool {
		if near[i].dist != near[j].dist {
			return near[i].dist < near[j].dist
		}
		return near[i].name < near[j].name
	})

	out := make([]string, len(near))
	for i, s := range near {
		out[i] = s.name
	}
	return out
}

// Nearest returns the closest candidate, or "" when none is near enough.
func Nearest(name string, candidates []string) string {
	if near := Near(name, candidates); len(near) > 0 {
		return near[0]
	}
	return ""
}

// Distance is the Levenshtein distance between a and b, over runes so a
// multi-byte character counts as one edit rather than several.
func Distance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}

	// One row of the matrix at a time: the full table is never needed, only
	// the previous row.
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}
