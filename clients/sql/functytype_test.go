package sql

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zclconf/go-cty/cty"
)

// TestIsSQLClient verifies the "sql_client" open-type predicate accepts the rich
// client object shape that CtyValue produces (an object whose _capsule is the
// sql_client capsule), the bare capsule, and rejects anything else. This is the
// case identity-on-capsule would miss: client.<db> is an object, not a capsule.
func TestIsSQLClient(t *testing.T) {
	c := &SQLClient{}

	bare := cty.CapsuleVal(SQLClientCapsuleType, c)
	assert.NoError(t, IsSQLClient(bare))

	// The client.<db> shape: an object carrying the sql_client capsule under
	// _capsule (plus, in real use, named query attributes).
	obj := cty.ObjectVal(map[string]cty.Value{"_capsule": bare})
	assert.NoError(t, IsSQLClient(obj))

	assert.Error(t, IsSQLClient(cty.StringVal("nope")))
	assert.Error(t, IsSQLClient(cty.ObjectVal(map[string]cty.Value{"x": cty.True})))
}
