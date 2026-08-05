package schemadoc

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The Default column is conditional, because most bodies have no defaults and
// an empty column in every table would cost a reader more than it tells them.

func TestMarkdownOmitsTheDefaultColumnWhenNothingHasOne(t *testing.T) {
	out := RenderMarkdown([]Event{AttrTable{Rows: []AttrRow{
		{Name: "listen", Type: "string", Required: true, Summary: "Listen address."},
	}}}, MarkdownOptions{})

	assert.Contains(t, out, "| Attribute | Type | Required | Description |")
	assert.NotContains(t, out, "Default")
	assert.Contains(t, out, "| `listen` | string | yes | Listen address. |")
}

func TestMarkdownAddsTheDefaultColumnWhenSomethingHasOne(t *testing.T) {
	out := RenderMarkdown([]Event{AttrTable{Rows: []AttrRow{
		{Name: "brokers", Type: "list", Required: true, Summary: "Broker addresses."},
		{Name: "keep_alive", Type: "string", Default: "30s", Summary: "Ping interval."},
	}}}, MarkdownOptions{})

	assert.Contains(t, out, "| Attribute | Type | Required | Default | Description |")
	// A required attribute leaves the cell empty rather than claiming a default
	// it cannot have, and every row keeps the same column count.
	assert.Contains(t, out, "| `brokers` | list | yes |  | Broker addresses. |")
	assert.Contains(t, out, "| `keep_alive` | string |  | `30s` | Ping interval. |")
}

func TestAttrTableHasDefaults(t *testing.T) {
	assert.False(t, AttrTable{}.HasDefaults())
	assert.False(t, AttrTable{Rows: []AttrRow{{Name: "a"}}}.HasDefaults())
	assert.True(t, AttrTable{Rows: []AttrRow{{Name: "a"}, {Name: "b", Default: "1"}}}.HasDefaults())
}
