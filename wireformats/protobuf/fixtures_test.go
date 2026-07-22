package protobuf

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// This file hand-builds FileDescriptorSet fixtures programmatically. protoc/buf
// are not required to run the tests; the builders here are the source of truth.
// Users generate real descriptor sets with `protoc --include_imports
// --descriptor_set_out=x.binpb x.proto` or `buf build -o x.binpb` (see the doc
// page); the wire behavior is identical.

func p[T any](v T) *T { return &v }

const (
	tString = descriptorpb.FieldDescriptorProto_TYPE_STRING
	tInt32  = descriptorpb.FieldDescriptorProto_TYPE_INT32
	tInt64  = descriptorpb.FieldDescriptorProto_TYPE_INT64
	tBool   = descriptorpb.FieldDescriptorProto_TYPE_BOOL
	tDouble = descriptorpb.FieldDescriptorProto_TYPE_DOUBLE
	tBytes  = descriptorpb.FieldDescriptorProto_TYPE_BYTES
	tEnum   = descriptorpb.FieldDescriptorProto_TYPE_ENUM
	tMsg    = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE

	labelOptional = descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	labelRepeated = descriptorpb.FieldDescriptorProto_LABEL_REPEATED
)

func scalarField(name string, num int32, typ descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name: p(name), Number: p(num), Label: labelOptional.Enum(), Type: typ.Enum(),
	}
}

func repeatedField(name string, num int32, typ descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name: p(name), Number: p(num), Label: labelRepeated.Enum(), Type: typ.Enum(),
	}
}

func namedField(name string, num int32, typ descriptorpb.FieldDescriptorProto_Type, typeName string) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name: p(name), Number: p(num), Label: labelOptional.Enum(), Type: typ.Enum(), TypeName: p(typeName),
	}
}

func mapField(name string, num int32, entryTypeName string) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name: p(name), Number: p(num), Label: labelRepeated.Enum(), Type: tMsg.Enum(), TypeName: p(entryTypeName),
	}
}

func mapEntry(name string, keyType, valType descriptorpb.FieldDescriptorProto_Type) *descriptorpb.DescriptorProto {
	return &descriptorpb.DescriptorProto{
		Name: p(name),
		Field: []*descriptorpb.FieldDescriptorProto{
			{Name: p("key"), Number: p(int32(1)), Label: labelOptional.Enum(), Type: keyType.Enum()},
			{Name: p("value"), Number: p(int32(2)), Label: labelOptional.Enum(), Type: valType.Enum()},
		},
		Options: &descriptorpb.MessageOptions{MapEntry: p(true)},
	}
}

func marshalFDS(t *testing.T, files ...*descriptorpb.FileDescriptorProto) []byte {
	t.Helper()
	b, err := proto.Marshal(&descriptorpb.FileDescriptorSet{File: files})
	require.NoError(t, err)
	return b
}

// ordersFDS covers scalars, enum, nested message, repeated, map, proto3
// optional (presence), and a oneof.
func ordersFDS(t *testing.T) []byte {
	t.Helper()
	order := &descriptorpb.DescriptorProto{
		Name: p("Order"),
		Field: []*descriptorpb.FieldDescriptorProto{
			scalarField("id", 1, tInt64),
			scalarField("name", 2, tString),
			scalarField("active", 3, tBool),
			scalarField("amount", 4, tDouble),
			scalarField("data", 5, tBytes),
			namedField("status", 6, tEnum, ".acme.test.v1.Status"),
			namedField("item", 7, tMsg, ".acme.test.v1.Order.Item"),
			repeatedField("tags", 8, tString),
			mapField("attrs", 9, ".acme.test.v1.Order.AttrsEntry"),
			// proto3 optional "note" -> synthetic oneof index 1
			{Name: p("note"), Number: p(int32(10)), Label: labelOptional.Enum(), Type: tString.Enum(),
				Proto3Optional: p(true), OneofIndex: p(int32(1))},
			// real oneof "choice" index 0
			{Name: p("a"), Number: p(int32(11)), Label: labelOptional.Enum(), Type: tString.Enum(), OneofIndex: p(int32(0))},
			{Name: p("b"), Number: p(int32(12)), Label: labelOptional.Enum(), Type: tInt32.Enum(), OneofIndex: p(int32(0))},
		},
		OneofDecl: []*descriptorpb.OneofDescriptorProto{
			{Name: p("choice")},
			{Name: p("_note")},
		},
		NestedType: []*descriptorpb.DescriptorProto{
			{
				Name: p("Item"),
				Field: []*descriptorpb.FieldDescriptorProto{
					scalarField("sku", 1, tString),
					scalarField("qty", 2, tInt32),
				},
			},
			mapEntry("AttrsEntry", tString, tString),
		},
	}
	file := &descriptorpb.FileDescriptorProto{
		Name:    p("acme/test/orders.proto"),
		Package: p("acme.test.v1"),
		Syntax:  p("proto3"),
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name: p("Status"),
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: p("STATUS_UNKNOWN"), Number: p(int32(0))},
				{Name: p("ACTIVE"), Number: p(int32(1))},
				{Name: p("CLOSED"), Number: p(int32(2))},
			},
		}},
		MessageType: []*descriptorpb.DescriptorProto{order},
	}
	return marshalFDS(t, file)
}

// wktFDS covers the well-known types with special native representations. It
// deliberately does NOT include the google/protobuf/* descriptor files, to
// exercise the global-registry overlay.
func wktFDS(t *testing.T) []byte {
	t.Helper()
	msg := &descriptorpb.DescriptorProto{
		Name: p("WktMsg"),
		Field: []*descriptorpb.FieldDescriptorProto{
			namedField("ts", 1, tMsg, ".google.protobuf.Timestamp"),
			namedField("dur", 2, tMsg, ".google.protobuf.Duration"),
			namedField("i64w", 3, tMsg, ".google.protobuf.Int64Value"),
			namedField("strw", 4, tMsg, ".google.protobuf.StringValue"),
			namedField("bytesw", 5, tMsg, ".google.protobuf.BytesValue"),
			namedField("st", 6, tMsg, ".google.protobuf.Struct"),
			namedField("val", 7, tMsg, ".google.protobuf.Value"),
			namedField("lst", 8, tMsg, ".google.protobuf.ListValue"),
			namedField("mask", 9, tMsg, ".google.protobuf.FieldMask"),
			namedField("empty", 10, tMsg, ".google.protobuf.Empty"),
			namedField("anyf", 11, tMsg, ".google.protobuf.Any"),
			scalarField("raw", 12, tBytes),
		},
	}
	file := &descriptorpb.FileDescriptorProto{
		Name:    p("acme/test/wkt.proto"),
		Package: p("acme.test.v1"),
		Syntax:  p("proto3"),
		Dependency: []string{
			"google/protobuf/timestamp.proto",
			"google/protobuf/duration.proto",
			"google/protobuf/wrappers.proto",
			"google/protobuf/struct.proto",
			"google/protobuf/field_mask.proto",
			"google/protobuf/empty.proto",
			"google/protobuf/any.proto",
		},
		MessageType: []*descriptorpb.DescriptorProto{msg},
	}
	return marshalFDS(t, file)
}

// anyFDS has an Envelope with a google.protobuf.Any field plus an Inner
// message that can be packed into it (so the Any resolver knows the type).
func anyFDS(t *testing.T) []byte {
	t.Helper()
	inner := &descriptorpb.DescriptorProto{
		Name:  p("Inner"),
		Field: []*descriptorpb.FieldDescriptorProto{scalarField("greeting", 1, tString)},
	}
	envelope := &descriptorpb.DescriptorProto{
		Name:  p("Envelope"),
		Field: []*descriptorpb.FieldDescriptorProto{namedField("payload", 1, tMsg, ".google.protobuf.Any")},
	}
	file := &descriptorpb.FileDescriptorProto{
		Name:        p("acme/any/any.proto"),
		Package:     p("acme.any.v1"),
		Syntax:      p("proto3"),
		Dependency:  []string{"google/protobuf/any.proto"},
		MessageType: []*descriptorpb.DescriptorProto{inner, envelope},
	}
	return marshalFDS(t, file)
}

// collideFDS has two messages with the same short name "Order" in different
// packages, plus a message "Widget" with a unique short name.
func collideFDS(t *testing.T) []byte {
	t.Helper()
	mkOrder := func(pkg, file string) *descriptorpb.FileDescriptorProto {
		return &descriptorpb.FileDescriptorProto{
			Name:    p(file),
			Package: p(pkg),
			Syntax:  p("proto3"),
			MessageType: []*descriptorpb.DescriptorProto{{
				Name:  p("Order"),
				Field: []*descriptorpb.FieldDescriptorProto{scalarField("id", 1, tInt64)},
			}},
		}
	}
	widget := &descriptorpb.FileDescriptorProto{
		Name:    p("acme/c/widget.proto"),
		Package: p("acme.c"),
		Syntax:  p("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name:  p("Widget"),
			Field: []*descriptorpb.FieldDescriptorProto{scalarField("id", 1, tInt64)},
		}},
	}
	return marshalFDS(t,
		mkOrder("acme.a", "acme/a/order.proto"),
		mkOrder("acme.b", "acme/b/order.proto"),
		widget,
	)
}
