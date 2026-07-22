package protobuf

import "google.golang.org/protobuf/reflect/protoreflect"

// uniqueShortNames returns the set of short (final-segment) message names that
// are unique across the whole message set. A message whose short name is unique
// receives a bare-attribute alias in the multi-message object; a short name
// shared by two or more messages yields no alias for any of them, so those
// messages are reachable only through their full-name index key.
//
// This keeps the key set predictable: the full-name index is the guaranteed
// contract, and short aliases are sugar present iff unique. Adding a colliding
// message only ever removes a sugar alias — it never renames or breaks a
// full-name key.
func uniqueShortNames(messages map[protoreflect.FullName]protoreflect.MessageDescriptor) map[string]struct{} {
	counts := make(map[string]int, len(messages))
	for _, md := range messages {
		counts[string(md.Name())]++
	}

	unique := make(map[string]struct{})
	for short, n := range counts {
		if n == 1 {
			unique[short] = struct{}{}
		}
	}
	return unique
}
