package schemadoc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tsarna/vinculum/config"
)

// A name that is exactly a search term ranks above one that merely contains it,
// and a namespaced function's exact name includes the part after its `::`.
// Without that, kind and path decide — which is how a capped answer came to cut
// the thing searched for. Here path order alone would put `azz` first.
func TestAproposRanksAnExactNameFirst(t *testing.T) {
	cat := fakeCatalog{docs: map[string]config.FuncDoc{
		"azz":   {Name: "azz", Doc: "Contains the term."},
		"b::zz": {Name: "b::zz", Doc: "Is the term, in a namespace."},
		"zz":    {Name: "zz", Doc: "Is the term."},
	}}

	var got []string
	for _, h := range Apropos(nil, cat, KindFunction, []string{"zz"}) {
		got = append(got, h.Path[0])
	}
	assert.Equal(t, []string{"b::zz", "zz", "azz"}, got)
}
