package protobuf

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestLoadSchema_Orders(t *testing.T) {
	sch, err := loadSchema(ordersFDS(t))
	require.NoError(t, err)

	md, ok := sch.messages["acme.test.v1.Order"]
	require.True(t, ok, "Order message should be present")
	assert.Equal(t, protoreflect.FullName("acme.test.v1.Order"), md.FullName())

	// The nested Item message is also collected; the synthetic AttrsEntry map
	// entry is not.
	_, hasItem := sch.messages["acme.test.v1.Order.Item"]
	assert.True(t, hasItem)
	_, hasEntry := sch.messages["acme.test.v1.Order.AttrsEntry"]
	assert.False(t, hasEntry, "map entry messages must not be exposed")
}

func TestLoadSchema_WKTOverlay(t *testing.T) {
	// wktFDS omits the google/protobuf/* files; loading must still succeed via
	// the global-registry overlay.
	sch, err := loadSchema(wktFDS(t))
	require.NoError(t, err)

	_, ok := sch.messages["acme.test.v1.WktMsg"]
	require.True(t, ok)

	// Well-known types are excluded from the user-facing message set.
	_, hasTs := sch.messages["google.protobuf.Timestamp"]
	assert.False(t, hasTs)
}
