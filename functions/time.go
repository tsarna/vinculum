package functions

import (
	"fmt"
	"strings"
	"time"

	isoduration "github.com/sosodev/duration"
	"github.com/tsarna/vinculum/internal/hclutil"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// NowFunc returns the current time, optionally in the given IANA timezone.
// Called as now() or now("America/New_York").
var NowFunc = function.New(&function.Spec{
	VarParam: &function.Parameter{
		Name: "tz",
		Type: cty.String,
	},
	Type: function.StaticReturnType(hclutil.TimeCapsuleType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		if len(args) == 0 {
			return hclutil.NewTimeCapsule(time.Now()), nil
		}
		tzName := args[0].AsString()
		loc, err := time.LoadLocation(tzName)
		if err != nil {
			return cty.NilVal, fmt.Errorf("invalid timezone %q: %s", tzName, err)
		}
		return hclutil.NewTimeCapsule(time.Now().In(loc)), nil
	},
})

// ParseTimeFunc parses an RFC 3339 timestamp string into a time value.
// The string must include a timezone; strings without timezone are rejected.
// Called as parsetime("2024-01-15T10:30:00Z").
var ParseTimeFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "s", Type: cty.String},
	},
	Type: function.StaticReturnType(hclutil.TimeCapsuleType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		s := args[0].AsString()
		// RFC3339Nano handles both with and without sub-second precision.
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return cty.NilVal, fmt.Errorf("invalid RFC 3339 timestamp %q: %s", s, err)
		}
		return hclutil.NewTimeCapsule(t), nil
	},
})

// DurationFunc creates a duration from a string or from a number and unit.
// Called as duration("5m"), duration("PT5M"), or duration(5, "m").
var DurationFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "val", Type: cty.DynamicPseudoType},
	},
	VarParam: &function.Parameter{
		Name: "unit",
		Type: cty.DynamicPseudoType,
	},
	Type: func(args []cty.Value) (cty.Type, error) {
		switch len(args) {
		case 1:
			t := args[0].Type()
			if t != cty.String && t != cty.DynamicPseudoType {
				return cty.NilType, fmt.Errorf("duration() 1-arg form requires a string, got %s", t.FriendlyName())
			}
			return hclutil.DurationCapsuleType, nil
		case 2:
			t0, t1 := args[0].Type(), args[1].Type()
			if t0 != cty.Number && t0 != cty.DynamicPseudoType {
				return cty.NilType, fmt.Errorf("duration() 2-arg form requires a number as first argument, got %s", t0.FriendlyName())
			}
			if t1 != cty.String && t1 != cty.DynamicPseudoType {
				return cty.NilType, fmt.Errorf("duration() 2-arg form requires a string unit as second argument, got %s", t1.FriendlyName())
			}
			return hclutil.DurationCapsuleType, nil
		default:
			return cty.NilType, fmt.Errorf("duration() requires 1 or 2 arguments, got %d", len(args))
		}
	},
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		if len(args) == 1 {
			return parseDurationString(args[0].AsString())
		}
		// 2-arg form: (number, unit)
		n, _ := args[0].AsBigFloat().Float64()
		return durationFromNumber(n, args[1].AsString())
	},
})

// TimeAddFunc adds a duration to a time. Backward-compatible with the stdlib
// timeadd(string, string) form; also accepts capsule types.
//
// Signatures:
//
//	timeadd(string, string) → string   (standard hcl behavior)
//	timeadd(time, duration) → time
//	timeadd(time, string)   → time     (string auto-parsed as duration)
//	timeadd(string, duration) → time   (string auto-parsed as RFC 3339)
var TimeAddFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "ts", Type: cty.DynamicPseudoType},
		{Name: "dur", Type: cty.DynamicPseudoType},
	},
	Type: func(args []cty.Value) (cty.Type, error) {
		t0, t1 := args[0].Type(), args[1].Type()
		// Unknown types at check time — defer to runtime
		if t0 == cty.DynamicPseudoType || t1 == cty.DynamicPseudoType {
			return cty.DynamicPseudoType, nil
		}
		// (string, string) — backward-compatible path returns string
		if t0 == cty.String && t1 == cty.String {
			return cty.String, nil
		}
		// All other valid combinations return time
		validTS := t0 == hclutil.TimeCapsuleType || t0 == cty.String
		validDur := t1 == hclutil.DurationCapsuleType || t1 == cty.String
		if validTS && validDur {
			return hclutil.TimeCapsuleType, nil
		}
		return cty.NilType, fmt.Errorf("timeadd: unsupported argument types %s and %s", t0.FriendlyName(), t1.FriendlyName())
	},
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		// (string, string) — backward-compatible behavior preserved exactly
		if args[0].Type() == cty.String && args[1].Type() == cty.String {
			ts, err := time.Parse(time.RFC3339, args[0].AsString())
			if err != nil {
				return cty.NilVal, fmt.Errorf("timeadd: invalid timestamp %q: %s", args[0].AsString(), err)
			}
			dur, err := time.ParseDuration(args[1].AsString())
			if err != nil {
				return cty.NilVal, fmt.Errorf("timeadd: invalid duration %q: %s", args[1].AsString(), err)
			}
			return cty.StringVal(ts.Add(dur).Format(time.RFC3339)), nil
		}

		// Get the time value
		var t time.Time
		switch args[0].Type() {
		case cty.String:
			var err error
			t, err = time.Parse(time.RFC3339Nano, args[0].AsString())
			if err != nil {
				return cty.NilVal, fmt.Errorf("timeadd: invalid timestamp %q: %s", args[0].AsString(), err)
			}
		case hclutil.TimeCapsuleType:
			var err error
			t, err = hclutil.GetTime(args[0])
			if err != nil {
				return cty.NilVal, err
			}
		default:
			return cty.NilVal, fmt.Errorf("timeadd: first argument must be a time or string, got %s", args[0].Type().FriendlyName())
		}

		// Get the duration value
		var d time.Duration
		switch args[1].Type() {
		case cty.String:
			v, err := parseDurationString(args[1].AsString())
			if err != nil {
				return cty.NilVal, err
			}
			d, err = hclutil.GetDuration(v)
			if err != nil {
				return cty.NilVal, err
			}
		case hclutil.DurationCapsuleType:
			var err error
			d, err = hclutil.GetDuration(args[1])
			if err != nil {
				return cty.NilVal, err
			}
		default:
			return cty.NilVal, fmt.Errorf("timeadd: second argument must be a duration or string, got %s", args[1].Type().FriendlyName())
		}

		return hclutil.NewTimeCapsule(t.Add(d)), nil
	},
})

// TimeSubFunc subtracts a time or duration from a time.
//
// Signatures:
//
//	timesub(time, time)     → duration   (elapsed from t2 to t1; negative if t1 < t2)
//	timesub(time, duration) → time       (time minus duration)
var TimeSubFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "t1", Type: cty.DynamicPseudoType},
		{Name: "t2", Type: cty.DynamicPseudoType},
	},
	Type: func(args []cty.Value) (cty.Type, error) {
		t0, t1 := args[0].Type(), args[1].Type()
		if t0 == cty.DynamicPseudoType || t1 == cty.DynamicPseudoType {
			return cty.DynamicPseudoType, nil
		}
		if t0 != hclutil.TimeCapsuleType {
			return cty.NilType, fmt.Errorf("timesub: first argument must be a time, got %s", t0.FriendlyName())
		}
		switch t1 {
		case hclutil.TimeCapsuleType:
			return hclutil.DurationCapsuleType, nil
		case hclutil.DurationCapsuleType:
			return hclutil.TimeCapsuleType, nil
		default:
			return cty.NilType, fmt.Errorf("timesub: second argument must be a time or duration, got %s", t1.FriendlyName())
		}
	},
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		t1, err := hclutil.GetTime(args[0])
		if err != nil {
			return cty.NilVal, err
		}
		switch args[1].Type() {
		case hclutil.TimeCapsuleType:
			t2, err := hclutil.GetTime(args[1])
			if err != nil {
				return cty.NilVal, err
			}
			return hclutil.NewDurationCapsule(t1.Sub(t2)), nil
		case hclutil.DurationCapsuleType:
			d, err := hclutil.GetDuration(args[1])
			if err != nil {
				return cty.NilVal, err
			}
			return hclutil.NewTimeCapsule(t1.Add(-d)), nil
		default:
			return cty.NilVal, fmt.Errorf("timesub: second argument must be a time or duration, got %s", args[1].Type().FriendlyName())
		}
	},
})

// SinceFunc returns the duration elapsed since the given time (equivalent to timesub(now(), t)).
var SinceFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "t", Type: hclutil.TimeCapsuleType},
	},
	Type: function.StaticReturnType(hclutil.DurationCapsuleType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		t, err := hclutil.GetTime(args[0])
		if err != nil {
			return cty.NilVal, err
		}
		return hclutil.NewDurationCapsule(time.Since(t)), nil
	},
})

// UntilFunc returns the duration until the given time (equivalent to timesub(t, now())).
var UntilFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "t", Type: hclutil.TimeCapsuleType},
	},
	Type: function.StaticReturnType(hclutil.DurationCapsuleType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		t, err := hclutil.GetTime(args[0])
		if err != nil {
			return cty.NilVal, err
		}
		return hclutil.NewDurationCapsule(time.Until(t)), nil
	},
})

// FormatTimeFunc formats a time value using Go's reference-time format.
// Called as formattime("2006-01-02", t).
var FormatTimeFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "format", Type: cty.String},
		{Name: "t", Type: hclutil.TimeCapsuleType},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		format := args[0].AsString()
		t, err := hclutil.GetTime(args[1])
		if err != nil {
			return cty.NilVal, err
		}
		return cty.StringVal(t.Format(format)), nil
	},
})

// FormatDurationFunc formats a duration as a string.
// Called as formatduration(d) for Go format (default) or formatduration(d, "iso") for ISO 8601.
var FormatDurationFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "d", Type: hclutil.DurationCapsuleType},
	},
	VarParam: &function.Parameter{
		Name: "fmt",
		Type: cty.String,
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		d, err := hclutil.GetDuration(args[0])
		if err != nil {
			return cty.NilVal, err
		}
		format := "go"
		if len(args) > 1 {
			format = args[1].AsString()
		}
		switch format {
		case "go", "":
			return cty.StringVal(d.String()), nil
		case "iso":
			return cty.StringVal(durationToISO8601(d)), nil
		default:
			return cty.NilVal, fmt.Errorf("formatduration: unknown format %q; valid values are \"go\" and \"iso\"", format)
		}
	},
})

// GetTimeFunctions returns all time-related functions for registration in the eval context.
// The "timeadd" entry here replaces the stdlib version.
func GetTimeFunctions() map[string]function.Function {
	return map[string]function.Function{
		"now":            NowFunc,
		"parsetime":      ParseTimeFunc,
		"duration":       DurationFunc,
		"timeadd":        TimeAddFunc,
		"timesub":        TimeSubFunc,
		"since":          SinceFunc,
		"until":          UntilFunc,
		"formattime":     FormatTimeFunc,
		"formatduration": FormatDurationFunc,
	}
}

// --- helpers ---

var durationUnits = map[string]time.Duration{
	"h":  time.Hour,
	"m":  time.Minute,
	"s":  time.Second,
	"ms": time.Millisecond,
	"us": time.Microsecond,
	"ns": time.Nanosecond,
}

// parseDurationString parses a Go-format or ISO 8601 duration string.
// Calendar durations (years, months) are rejected.
func parseDurationString(s string) (cty.Value, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "P") || strings.HasPrefix(s, "-P") {
		d, err := isoduration.Parse(strings.TrimPrefix(s, "-"))
		if err != nil {
			return cty.NilVal, fmt.Errorf("invalid ISO 8601 duration %q: %s", s, err)
		}
		if d.Years != 0 || d.Months != 0 {
			return cty.NilVal, fmt.Errorf("calendar durations with years or months cannot be represented as a fixed duration; use addyears() or addmonths() instead")
		}
		td := d.ToTimeDuration()
		if strings.HasPrefix(s, "-") {
			td = -td
		}
		return hclutil.NewDurationCapsule(td), nil
	}
	td, err := time.ParseDuration(s)
	if err != nil {
		return cty.NilVal, fmt.Errorf("invalid duration %q: expected ISO 8601 (e.g. \"PT5M\") or Go format (e.g. \"5m30s\"): %s", s, err)
	}
	return hclutil.NewDurationCapsule(td), nil
}

// durationFromNumber constructs a duration from a number and a unit string.
func durationFromNumber(n float64, unit string) (cty.Value, error) {
	factor, ok := durationUnits[unit]
	if !ok {
		return cty.NilVal, fmt.Errorf("unknown duration unit %q; valid units: h, m, s, ms, us, ns", unit)
	}
	return hclutil.NewDurationCapsule(time.Duration(n * float64(factor))), nil
}

// durationToISO8601 formats a time.Duration as an ISO 8601 duration string (P-notation).
func durationToISO8601(d time.Duration) string {
	if d == 0 {
		return "PT0S"
	}

	prefix := ""
	if d < 0 {
		prefix = "-"
		d = -d
	}

	hours := int64(d / time.Hour)
	d -= time.Duration(hours) * time.Hour
	minutes := int64(d / time.Minute)
	d -= time.Duration(minutes) * time.Minute

	totalNs := d.Nanoseconds()
	seconds := totalNs / 1_000_000_000
	fracNs := totalNs % 1_000_000_000

	var b strings.Builder
	b.WriteString(prefix + "PT")
	if hours > 0 {
		fmt.Fprintf(&b, "%dH", hours)
	}
	if minutes > 0 {
		fmt.Fprintf(&b, "%dM", minutes)
	}
	if seconds > 0 || fracNs > 0 || (hours == 0 && minutes == 0) {
		if fracNs == 0 {
			fmt.Fprintf(&b, "%dS", seconds)
		} else {
			fracStr := strings.TrimRight(fmt.Sprintf("%09d", fracNs), "0")
			fmt.Fprintf(&b, "%d.%sS", seconds, fracStr)
		}
	}
	return b.String()
}
