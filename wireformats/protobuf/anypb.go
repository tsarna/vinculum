package protobuf

import (
	"fmt"

	bytescty "github.com/tsarna/bytes-cty-type"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// anyToCty converts a google.protobuf.Any in native mode. When the packed type
// is known to the loaded set, it is unpacked into an object carrying the
// message's fields plus an "@type" key; otherwise the value stays opaque as
// {"@type": url, "value": <bytes object>}.
func (s *schema) anyToCty(m protoreflect.Message) (cty.Value, error) {
	md := m.Descriptor()
	typeURL := m.Get(md.Fields().ByName("type_url")).String()
	value := m.Get(md.Fields().ByName("value")).Bytes()

	mt, err := s.resolver.FindMessageByURL(typeURL)
	if err != nil || mt == nil {
		return cty.ObjectVal(map[string]cty.Value{
			"@type": cty.StringVal(typeURL),
			"value": bytescty.BuildBytesObject(value, cfg.DefaultBytesContentType),
		}), nil
	}

	inner := mt.New()
	if err := proto.Unmarshal(value, inner.Interface()); err != nil {
		return cty.NilVal, fmt.Errorf("unpacking Any %q: %w", typeURL, err)
	}
	decoded, err := s.messageToCty(inner)
	if err != nil {
		return cty.NilVal, err
	}

	if decoded.Type().IsObjectType() {
		attrs := decoded.AsValueMap()
		if attrs == nil {
			attrs = map[string]cty.Value{}
		}
		attrs["@type"] = cty.StringVal(typeURL)
		return cty.ObjectVal(attrs), nil
	}
	// Packed a non-object (e.g. a well-known scalar type): keep it under value.
	return cty.ObjectVal(map[string]cty.Value{
		"@type": cty.StringVal(typeURL),
		"value": decoded,
	}), nil
}

// anyFromNative packs a google.protobuf.Any from a native object with an
// "@type" key. A known type packs its remaining fields; an unknown type
// requires an opaque "value" (bytes) to pass through symmetrically.
func (s *schema) anyFromNative(m protoreflect.Message, v any, path string) error {
	md := m.Descriptor()
	obj, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: Any expects an object with @type, got %T", path, v)
	}
	turl, ok := obj["@type"]
	if !ok {
		return fmt.Errorf("%s: Any object is missing @type", path)
	}
	typeURL, ok := turl.(string)
	if !ok {
		return fmt.Errorf("%s: @type must be a string, got %T", path, turl)
	}

	typeURLFd := md.Fields().ByName("type_url")
	valueFd := md.Fields().ByName("value")

	mt, err := s.resolver.FindMessageByURL(typeURL)
	if err != nil || mt == nil {
		raw, ok := obj["value"]
		if !ok {
			return fmt.Errorf("%s: unknown Any type %q and no opaque \"value\" provided", path, typeURL)
		}
		b, err := toBytes(raw, path+".value")
		if err != nil {
			return err
		}
		m.Set(typeURLFd, protoreflect.ValueOfString(typeURL))
		m.Set(valueFd, protoreflect.ValueOfBytes(b))
		return nil
	}

	inner := mt.New()
	fields := make(map[string]any, len(obj))
	for k, val := range obj {
		if k == "@type" {
			continue
		}
		fields[k] = val
	}
	if err := s.nativeToMessage(inner.Descriptor(), inner, fields, path); err != nil {
		return err
	}
	packed, err := proto.Marshal(inner.Interface())
	if err != nil {
		return fmt.Errorf("%s: packing Any %q: %w", path, typeURL, err)
	}
	m.Set(typeURLFd, protoreflect.ValueOfString(typeURL))
	m.Set(valueFd, protoreflect.ValueOfBytes(packed))
	return nil
}
