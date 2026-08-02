package protobuf

import (
	"testing"

	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSchemaBodyMatchesParser guards the one place the schema's anti-drift
// guarantee does not hold on its own: Process decodes the block by hand
// against blockSchema, so protobufBody — which is what `vinculum schema`
// reflects — is a parallel declaration rather than the parser's own. Comparing
// the two here restores the guarantee.
func TestSchemaBodyMatchesParser(t *testing.T) {
	implied, _ := gohcl.ImpliedBodySchema(&protobufBody{})

	documented := map[string]bool{}
	for _, attr := range implied.Attributes {
		documented[attr.Name] = attr.Required
	}

	parsed := map[string]bool{}
	for _, attr := range blockSchema.Attributes {
		parsed[attr.Name] = attr.Required
	}

	// gohcl reports a pointer field as optional whatever the tag says, which
	// is exactly how the optional attributes are declared here.
	assert.Equal(t, parsed, documented)
	require.Empty(t, implied.Blocks, "the protobuf block has no sub-blocks")
}
