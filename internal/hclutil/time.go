package hclutil

import (
	"fmt"
	"reflect"
	"time"

	"github.com/zclconf/go-cty/cty"
)

// TimeCapsuleType is a cty capsule type wrapping time.Time.
// Supports equality (==, !=) via Equals/RawEquals.
// Note: ordering operators (<, >, etc.) are not available for capsule types
// in go-cty — use timesub() and compare the resulting duration instead.
var TimeCapsuleType = cty.CapsuleWithOps("time", reflect.TypeOf(time.Time{}), &cty.CapsuleOps{
	// Equals uses time.Time.Equal so that two instants in different timezones
	// that represent the same moment compare as equal.
	Equals: func(a, b any) cty.Value {
		ta := a.(*time.Time)
		tb := b.(*time.Time)
		return cty.BoolVal(ta.Equal(*tb))
	},
	RawEquals: func(a, b any) bool {
		ta := a.(*time.Time)
		tb := b.(*time.Time)
		return ta.Equal(*tb)
	},
	GoString: func(val any) string {
		return fmt.Sprintf("time(%q)", val.(*time.Time).Format(time.RFC3339Nano))
	},
	TypeGoString: func(_ reflect.Type) string {
		return "time"
	},
})

// DurationCapsuleType is a cty capsule type wrapping time.Duration.
// Supports equality (==, !=) via Equals/RawEquals.
// Note: ordering operators (<, >, etc.) are not available for capsule types
// in go-cty — use durationpart() to extract a numeric value and compare that instead.
var DurationCapsuleType = cty.CapsuleWithOps("duration", reflect.TypeOf(time.Duration(0)), &cty.CapsuleOps{
	Equals: func(a, b any) cty.Value {
		da := a.(*time.Duration)
		db := b.(*time.Duration)
		return cty.BoolVal(*da == *db)
	},
	RawEquals: func(a, b any) bool {
		da := a.(*time.Duration)
		db := b.(*time.Duration)
		return *da == *db
	},
	GoString: func(val any) string {
		return fmt.Sprintf("duration(%q)", val.(*time.Duration).String())
	},
	TypeGoString: func(_ reflect.Type) string {
		return "duration"
	},
})

// NewTimeCapsule wraps a time.Time in a cty capsule value.
func NewTimeCapsule(t time.Time) cty.Value {
	return cty.CapsuleVal(TimeCapsuleType, &t)
}

// GetTime extracts a time.Time from a cty capsule value.
// Returns an error if the value is not a TimeCapsuleType.
func GetTime(val cty.Value) (time.Time, error) {
	if val.Type() != TimeCapsuleType {
		return time.Time{}, fmt.Errorf("expected time capsule, got %s", val.Type().FriendlyName())
	}
	return *val.EncapsulatedValue().(*time.Time), nil
}

// NewDurationCapsule wraps a time.Duration in a cty capsule value.
func NewDurationCapsule(d time.Duration) cty.Value {
	return cty.CapsuleVal(DurationCapsuleType, &d)
}

// GetDuration extracts a time.Duration from a cty capsule value.
// Returns an error if the value is not a DurationCapsuleType.
func GetDuration(val cty.Value) (time.Duration, error) {
	if val.Type() != DurationCapsuleType {
		return 0, fmt.Errorf("expected duration capsule, got %s", val.Type().FriendlyName())
	}
	return *val.EncapsulatedValue().(*time.Duration), nil
}
