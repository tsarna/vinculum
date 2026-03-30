package ctyutil

import (
	"fmt"

	"github.com/zclconf/go-cty/cty"
)

// GetCapsuleFromValue extracts the encapsulated Go value from a cty capsule,
// or from an object's _capsule attribute if present. Returns the raw interface{}
// so callers can type-assert to the interface they need.
//
// Accepts:
//   - a capsule value directly
//   - an object with a _capsule attribute that is a capsule
func GetCapsuleFromValue(val cty.Value) (interface{}, error) {
	if val.Type().IsObjectType() {
		if val.Type().HasAttribute("_capsule") {
			val = val.GetAttr("_capsule")
		} else {
			return nil, fmt.Errorf("expected capsule or object with _capsule attribute, got object without _capsule")
		}
	}
	if val.Type().IsCapsuleType() {
		return val.EncapsulatedValue(), nil
	}
	return nil, fmt.Errorf("expected capsule or object with _capsule attribute, got %s", val.Type().FriendlyName())
}
