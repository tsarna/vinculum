package protobuf

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// schema is the loaded, immutable result of reading a FileDescriptorSet. It is
// built once at config Process() time and shared read-only by every
// protoFormat the block produces.
type schema struct {
	// messages maps every user-facing message full name to its descriptor.
	// google.protobuf.* well-known types are excluded (they are bundled, not
	// user-bindable — see collectMessages).
	messages map[protoreflect.FullName]protoreflect.MessageDescriptor

	// resolver resolves message types by name and URL for protojson and for
	// google.protobuf.Any handling. It prefers the loaded set's dynamic types
	// and falls back to the global registry for well-known types.
	resolver *fallbackTypeResolver
}

// loadSchema reads and parses a compiled FileDescriptorSet from raw bytes.
func loadSchema(raw []byte) (*schema, error) {
	var fds descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(raw, &fds); err != nil {
		return nil, fmt.Errorf("not a valid FileDescriptorSet: %w", err)
	}

	files, err := buildFiles(&fds)
	if err != nil {
		return nil, err
	}

	msgs := collectMessages(files)

	// Build the type resolver: a dynamicpb message type for every message the
	// set defines (including well-known types the set happens to carry), with
	// a fallback to the global registry for well-known types absent from the
	// set.
	local := new(protoregistry.Types)
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		registerMessageTypes(fd.Messages(), local)
		return true
	})

	return &schema{
		messages: msgs,
		resolver: &fallbackTypeResolver{local: local},
	}, nil
}

// buildFiles constructs a *protoregistry.Files from the set, resolving each
// file's imports against the files already built plus the global registry
// (which supplies the compiled-in google/protobuf/* well-known types). This is
// why a user's descriptor set need not include the well-known types, though
// --include_imports harmlessly does.
//
// Files are built in the order they appear in the set; protoc/buf emit
// dependencies first, so intra-set imports resolve from the local registry as
// they are built.
func buildFiles(fds *descriptorpb.FileDescriptorSet) (*protoregistry.Files, error) {
	local := new(protoregistry.Files)
	deps := &fallbackFileResolver{local: local}

	for _, fdp := range fds.GetFile() {
		if _, err := local.FindFileByPath(fdp.GetName()); err == nil {
			continue // duplicate path already registered
		}
		fd, err := protodesc.NewFile(fdp, deps)
		if err != nil {
			return nil, fmt.Errorf("building descriptor for %q: %w", fdp.GetName(), err)
		}
		if err := local.RegisterFile(fd); err != nil {
			return nil, fmt.Errorf("registering descriptor for %q: %w", fdp.GetName(), err)
		}
	}

	return local, nil
}

// wktFilePrefix identifies the bundled well-known-type descriptor files.
const wktFilePrefix = "google/protobuf/"

// collectMessages returns every user-facing message (recursing into nested
// messages) keyed by full name. Messages declared in google/protobuf/* files
// are excluded: they are bundled implementation types, not user-bindable
// messages, and exposing them would pollute the short-name alias namespace
// (Timestamp, Duration, Any, ...).
func collectMessages(files *protoregistry.Files) map[protoreflect.FullName]protoreflect.MessageDescriptor {
	out := make(map[protoreflect.FullName]protoreflect.MessageDescriptor)
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if strings.HasPrefix(fd.Path(), wktFilePrefix) {
			return true
		}
		collectFromContainer(fd.Messages(), out)
		return true
	})
	return out
}

func collectFromContainer(msgs protoreflect.MessageDescriptors, out map[protoreflect.FullName]protoreflect.MessageDescriptor) {
	for i := 0; i < msgs.Len(); i++ {
		md := msgs.Get(i)
		// Map entry messages are synthetic (generated for map<k,v> fields) and
		// never independently bindable.
		if md.IsMapEntry() {
			continue
		}
		out[md.FullName()] = md
		collectFromContainer(md.Messages(), out)
	}
}

func registerMessageTypes(msgs protoreflect.MessageDescriptors, into *protoregistry.Types) {
	for i := 0; i < msgs.Len(); i++ {
		md := msgs.Get(i)
		if md.IsMapEntry() {
			continue
		}
		// Ignore duplicate registration errors: a message can only be
		// registered once, and that is fine.
		_ = into.RegisterMessage(dynamicpb.NewMessageType(md))
		registerMessageTypes(md.Messages(), into)
	}
}

// fallbackFileResolver resolves descriptors from a local set first, then from
// the global registry (for compiled-in well-known types).
type fallbackFileResolver struct {
	local *protoregistry.Files
}

func (r *fallbackFileResolver) FindFileByPath(path string) (protoreflect.FileDescriptor, error) {
	if fd, err := r.local.FindFileByPath(path); err == nil {
		return fd, nil
	}
	return protoregistry.GlobalFiles.FindFileByPath(path)
}

func (r *fallbackFileResolver) FindDescriptorByName(name protoreflect.FullName) (protoreflect.Descriptor, error) {
	if d, err := r.local.FindDescriptorByName(name); err == nil {
		return d, nil
	}
	return protoregistry.GlobalFiles.FindDescriptorByName(name)
}

// fallbackTypeResolver satisfies protojson's resolver interface
// (protoregistry.MessageTypeResolver + ExtensionTypeResolver), preferring the
// loaded set's dynamic types and falling back to the global registry for
// well-known types.
type fallbackTypeResolver struct {
	local *protoregistry.Types
}

func (r *fallbackTypeResolver) FindMessageByName(name protoreflect.FullName) (protoreflect.MessageType, error) {
	if mt, err := r.local.FindMessageByName(name); err == nil {
		return mt, nil
	}
	return protoregistry.GlobalTypes.FindMessageByName(name)
}

func (r *fallbackTypeResolver) FindMessageByURL(url string) (protoreflect.MessageType, error) {
	if mt, err := r.local.FindMessageByURL(url); err == nil {
		return mt, nil
	}
	return protoregistry.GlobalTypes.FindMessageByURL(url)
}

func (r *fallbackTypeResolver) FindExtensionByName(name protoreflect.FullName) (protoreflect.ExtensionType, error) {
	if et, err := r.local.FindExtensionByName(name); err == nil {
		return et, nil
	}
	return protoregistry.GlobalTypes.FindExtensionByName(name)
}

func (r *fallbackTypeResolver) FindExtensionByNumber(message protoreflect.FullName, field protoreflect.FieldNumber) (protoreflect.ExtensionType, error) {
	if et, err := r.local.FindExtensionByNumber(message, field); err == nil {
		return et, nil
	}
	return protoregistry.GlobalTypes.FindExtensionByNumber(message, field)
}
