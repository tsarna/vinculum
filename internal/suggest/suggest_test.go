package suggest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDistance(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"mqqt", "mqtt", 1},
		{"cleint", "client", 2},
		{"subscriptions", "subscription", 1},
		// Runes, not bytes: one accented character is one edit.
		{"café", "cafe", 1},
	} {
		assert.Equal(t, tc.want, Distance(tc.a, tc.b), "%q vs %q", tc.a, tc.b)
	}
}

func TestNearOrdersByDistanceThenName(t *testing.T) {
	// `bus` and `var` are both one edit from `vus`; `client` is far enough away
	// to be no suggestion at all.
	got := Near("vus", []string{"var", "client", "bus", "subscription"})

	assert.Equal(t, []string{"bus", "var"}, got)
}

// The candidates usually arrive from a map iteration, so the answer must not
// depend on the order they came in.
func TestNearIsOrderIndependent(t *testing.T) {
	first := Near("lenght", []string{"length", "keys", "lower", "values"})
	second := Near("lenght", []string{"values", "lower", "keys", "length"})

	assert.Equal(t, first, second)
	assert.Equal(t, []string{"length"}, first)
}

// A capitalization mistake is a near miss rather than a distance away, but the
// candidate comes back spelled the way it really is — which is the point.
func TestNearFoldsCase(t *testing.T) {
	assert.Equal(t, "jsonencode", Nearest("JSONencode", []string{"jsonencode", "jsondecode"}))
}

func TestNearestIsEmptyWhenNothingIsClose(t *testing.T) {
	assert.Equal(t, "", Nearest("totally_bogus", []string{"length", "keys", "upper"}))
}

// Three edits is where a suggestion stops being a correction and starts being
// a different word.
func TestNearestStopsAtMaxDistance(t *testing.T) {
	assert.Equal(t, "length", Nearest("lenxxh", []string{"length"}), "two edits is a near miss")
	assert.Equal(t, "", Nearest("lenxxxh", []string{"length"}), "three is not")
}
