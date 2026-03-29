package ctyutil

import "github.com/zclconf/go-cty/cty"

// GetStringAttr returns an attribute value as a string if it exists and is a string.
func GetStringAttr(val cty.Value, attr string) (string, bool) {
	if !val.Type().HasAttribute(attr) {
		return "", false
	}
	a := val.GetAttr(attr)
	if a.IsNull() || !a.IsKnown() || a.Type() != cty.String {
		return "", false
	}
	return a.AsString(), true
}

// GetIntAttr returns an attribute value as an int if it exists and is a number.
func GetIntAttr(val cty.Value, attr string) (int, bool) {
	if !val.Type().HasAttribute(attr) {
		return 0, false
	}
	a := val.GetAttr(attr)
	if a.IsNull() || !a.IsKnown() || a.Type() != cty.Number {
		return 0, false
	}
	i, _ := a.AsBigFloat().Int64()
	return int(i), true
}

// GetFloat32Attr returns an attribute value as a float32 if it exists and is a number.
func GetFloat32Attr(val cty.Value, attr string) (float32, bool) {
	if !val.Type().HasAttribute(attr) {
		return 0, false
	}
	a := val.GetAttr(attr)
	if a.IsNull() || !a.IsKnown() || a.Type() != cty.Number {
		return 0, false
	}
	f, _ := a.AsBigFloat().Float32()
	return f, true
}
