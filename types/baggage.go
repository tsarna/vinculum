package types

import (
	"context"
	"fmt"
	"reflect"

	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel/baggage"
)

// Baggage is a cty capsule that exposes the OTel baggage on a Go context to VCL
// via the generic get()/set()/clear()/length()/tostring() functions. It holds
// the SAME *context.Context pointer the _ctx capsule holds (see
// richcty.ContextObjectBuilder.ContextPointer), so a Set/Clear that derives a
// new context and writes it back through that pointer is observed by every
// later side-effect function (send(), http_post(), …) in the same action.
//
// Baggage entry properties (the rarely-used ;key=value metadata tail) are not
// exposed to VCL; the raw baggage.Baggage on the context keeps them intact, so
// outbound propagation preserves them.
type Baggage struct {
	ctxp *context.Context // the SAME pointer the _ctx capsule holds
}

var BaggageCapsuleType = cty.CapsuleWithOps("baggage", reflect.TypeOf((*Baggage)(nil)).Elem(), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		return fmt.Sprintf("baggage(%p)", val)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "Baggage"
	},
})

// NewBaggageCapsule wraps a shared context pointer as a baggage capsule value.
func NewBaggageCapsule(ctxp *context.Context) cty.Value {
	return cty.CapsuleVal(BaggageCapsuleType, &Baggage{ctxp: ctxp})
}

// GetBaggageFromCapsule extracts the *Baggage from a capsule value.
func GetBaggageFromCapsule(val cty.Value) (*Baggage, error) {
	if val.Type() != BaggageCapsuleType {
		return nil, fmt.Errorf("expected Baggage capsule, got %s", val.Type().FriendlyName())
	}
	b, ok := val.EncapsulatedValue().(*Baggage)
	if !ok {
		return nil, fmt.Errorf("encapsulated value is not a Baggage, got %T", val.EncapsulatedValue())
	}
	return b, nil
}

// Get implements richcty.Gettable:
//
//	get(ctx.baggage)               → map(string) of all entries ({} if none)
//	get(ctx.baggage, key)          → string, or null if absent
//	get(ctx.baggage, key, default) → string, or default if absent
func (b *Baggage) Get(_ context.Context, args []cty.Value) (cty.Value, error) {
	bg := baggage.FromContext(*b.ctxp)

	if len(args) == 0 {
		members := bg.Members()
		if len(members) == 0 {
			return cty.MapValEmpty(cty.String), nil
		}
		m := make(map[string]cty.Value, len(members))
		for _, mem := range members {
			m[mem.Key()] = cty.StringVal(mem.Value())
		}
		return cty.MapVal(m), nil
	}

	key := args[0]
	if key.IsNull() || key.Type() != cty.String {
		return cty.NilVal, fmt.Errorf("get(baggage): key must be a string")
	}
	if mem := bg.Member(key.AsString()); mem.Key() != "" {
		return cty.StringVal(mem.Value()), nil
	}
	if len(args) > 1 {
		return args[1], nil // default
	}
	return cty.NullVal(cty.String), nil
}

// Set implements richcty.Settable:
//
//	set(ctx.baggage, key, value)   add or overwrite one entry; returns value
//	set(ctx.baggage, key, null)    remove one entry; returns null
//	set(ctx.baggage, {k = v, ...}) merge a map of entries; returns the merged map
//
// W3C baggage values are strings, so a non-string, non-null value is rejected.
// Mutations are written back through the shared context pointer.
func (b *Baggage) Set(_ context.Context, args []cty.Value) (cty.Value, error) {
	if len(args) == 0 {
		return cty.NilVal, fmt.Errorf("set(baggage): expected (key, value), (key, null), or (map)")
	}

	bg := baggage.FromContext(*b.ctxp)

	// Map-merge form: a single map/object argument.
	if len(args) == 1 {
		arg := args[0]
		if arg.IsNull() {
			return cty.NilVal, fmt.Errorf("set(baggage): single argument must be a map of entries, got null")
		}
		t := arg.Type()
		if !t.IsMapType() && !t.IsObjectType() {
			return cty.NilVal, fmt.Errorf("set(baggage): single argument must be a map of entries, got %s", t.FriendlyName())
		}
		merged := make(map[string]cty.Value)
		for it := arg.ElementIterator(); it.Next(); {
			k, v := it.Element()
			key := k.AsString()
			var err error
			if bg, err = setOrDelete(bg, key, v); err != nil {
				return cty.NilVal, err
			}
			if !v.IsNull() {
				merged[key] = cty.StringVal(v.AsString())
			}
		}
		*b.ctxp = baggage.ContextWithBaggage(*b.ctxp, bg)
		if len(merged) == 0 {
			return cty.MapValEmpty(cty.String), nil
		}
		return cty.MapVal(merged), nil
	}

	// Key/value form.
	key := args[0]
	if key.IsNull() || key.Type() != cty.String {
		return cty.NilVal, fmt.Errorf("set(baggage): key must be a string")
	}
	value := args[1]
	var err error
	if bg, err = setOrDelete(bg, key.AsString(), value); err != nil {
		return cty.NilVal, err
	}
	*b.ctxp = baggage.ContextWithBaggage(*b.ctxp, bg)
	return value, nil
}

// setOrDelete returns a new Baggage with key set to value, or with key removed
// when value is null. Non-string, non-null values are rejected.
func setOrDelete(bg baggage.Baggage, key string, value cty.Value) (baggage.Baggage, error) {
	if value.IsNull() {
		return bg.DeleteMember(key), nil
	}
	if value.Type() != cty.String {
		return baggage.Baggage{}, fmt.Errorf("set(baggage): value for %q must be a string, got %s", key, value.Type().FriendlyName())
	}
	mem, err := baggage.NewMemberRaw(key, value.AsString())
	if err != nil {
		return baggage.Baggage{}, fmt.Errorf("set(baggage): invalid entry %q: %w", key, err)
	}
	nb, err := bg.SetMember(mem)
	if err != nil {
		return baggage.Baggage{}, fmt.Errorf("set(baggage): %w", err)
	}
	return nb, nil
}

// Delete implements richcty.Deletable:
//
//	delete(ctx.baggage)      remove all entries (same as clear)
//	delete(ctx.baggage, key) remove one entry (same as set(key, null))
//
// Baggage is flat, so more than one key argument is rejected; multi-arg key
// paths are reserved for nestable types.
func (b *Baggage) Delete(_ context.Context, args []cty.Value) (cty.Value, error) {
	bg := baggage.FromContext(*b.ctxp)
	switch len(args) {
	case 0:
		bg = baggage.Baggage{} // delete everything
	case 1:
		key := args[0]
		if key.IsNull() || key.Type() != cty.String {
			return cty.NilVal, fmt.Errorf("delete(baggage): key must be a string")
		}
		bg = bg.DeleteMember(key.AsString())
	default:
		return cty.NilVal, fmt.Errorf("delete(baggage): baggage is flat; expected at most one key, got %d", len(args))
	}
	*b.ctxp = baggage.ContextWithBaggage(*b.ctxp, bg)
	return cty.NullVal(cty.DynamicPseudoType), nil
}

// Clear implements richcty.Clearable: removes all baggage entries.
func (b *Baggage) Clear(_ context.Context) error {
	*b.ctxp = baggage.ContextWithBaggage(*b.ctxp, baggage.Baggage{})
	return nil
}

// Length implements richcty.Lengthable: the entry count (no map materialized).
func (b *Baggage) Length(_ context.Context) (int64, error) {
	return int64(baggage.FromContext(*b.ctxp).Len()), nil
}

// ToString implements richcty.Stringable: the W3C `baggage` header encoding.
func (b *Baggage) ToString(_ context.Context) (string, error) {
	return baggage.FromContext(*b.ctxp).String(), nil
}
