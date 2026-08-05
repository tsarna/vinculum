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

// A field can be more than one thing at once: an added field that some
// deliveries omit is both. The annotations have to read as a list rather than
// collide into "*(added here)*. *(not always present)*".
func TestMarkdownCombinesContextFieldAnnotations(t *testing.T) {
	out := RenderMarkdown([]Event{ContextTable{Shape: "decode-error", Rows: []ContextRow{
		{Name: "matched_pattern", Type: "string", Summary: "The pattern that matched.",
			Added: true, Optional: true},
		{Name: "channel", Type: "string", Summary: "The channel.", Added: true},
		{Name: "key", Type: "string", Summary: "The key.", Optional: true},
		{Name: "auth", Type: "object", Summary: "The identity.", Universal: true},
	}}}, MarkdownOptions{})

	assert.Contains(t, out, "The pattern that matched. *(added here)* *(not always present)* |")
	assert.NotContains(t, out, "*(added here)*. *(not")
	// One annotation still reads as a sentence.
	assert.Contains(t, out, "The channel. *(added here)* |")
	assert.Contains(t, out, "The key. *(not always present)* |")
	assert.Contains(t, out, "The identity. *(every `ctx` carries this)* |")
}

func TestAttrTableHasDefaults(t *testing.T) {
	assert.False(t, AttrTable{}.HasDefaults())
	assert.False(t, AttrTable{Rows: []AttrRow{{Name: "a"}}}.HasDefaults())
	assert.True(t, AttrTable{Rows: []AttrRow{{Name: "a"}, {Name: "b", Default: "1"}}}.HasDefaults())
}
