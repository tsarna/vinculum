package functions

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	timefmt "github.com/itchyny/timefmt-go"
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

// ParseTimeFunc parses a timestamp string into a time value.
//
// Forms:
//
//	parsetime(s)              — RFC 3339 (timezone required)
//	parsetime(format, s)      — parse s using Go layout (or @name alias)
//	parsetime(format, s, tz)  — same, but interpret s in the given IANA timezone
var ParseTimeFunc = function.New(&function.Spec{
	VarParam: &function.Parameter{Name: "args", Type: cty.String},
	Type: func(args []cty.Value) (cty.Type, error) {
		if len(args) < 1 || len(args) > 3 {
			return cty.NilType, fmt.Errorf("parsetime() takes 1 to 3 arguments")
		}
		return hclutil.TimeCapsuleType, nil
	},
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		switch len(args) {
		case 1:
			s := args[0].AsString()
			t, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				return cty.NilVal, fmt.Errorf("parsetime: invalid RFC 3339 timestamp %q: %s", s, err)
			}
			return hclutil.NewTimeCapsule(t), nil
		case 2:
			layout, err := resolveFormat(args[0].AsString())
			if err != nil {
				return cty.NilVal, err
			}
			t, err := time.Parse(layout, args[1].AsString())
			if err != nil {
				return cty.NilVal, fmt.Errorf("parsetime: cannot parse %q with format %q: %s", args[1].AsString(), args[0].AsString(), err)
			}
			return hclutil.NewTimeCapsule(t), nil
		case 3:
			layout, err := resolveFormat(args[0].AsString())
			if err != nil {
				return cty.NilVal, err
			}
			loc, err := time.LoadLocation(args[2].AsString())
			if err != nil {
				return cty.NilVal, fmt.Errorf("parsetime: invalid timezone %q: %s", args[2].AsString(), err)
			}
			t, err := time.ParseInLocation(layout, args[1].AsString(), loc)
			if err != nil {
				return cty.NilVal, fmt.Errorf("parsetime: cannot parse %q with format %q: %s", args[1].AsString(), args[0].AsString(), err)
			}
			return hclutil.NewTimeCapsule(t), nil
		default:
			return cty.NilVal, fmt.Errorf("parsetime() takes 1 to 3 arguments")
		}
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

// FormatTimeFunc formats a time value using Go's reference-time format or a @name alias.
// Called as formattime("2006-01-02", t) or formattime("@rfc3339", t).
var FormatTimeFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "format", Type: cty.String},
		{Name: "t", Type: hclutil.TimeCapsuleType},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		layout, err := resolveFormat(args[0].AsString())
		if err != nil {
			return cty.NilVal, err
		}
		t, err := hclutil.GetTime(args[1])
		if err != nil {
			return cty.NilVal, err
		}
		return cty.StringVal(t.Format(layout)), nil
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

// --- Unix interop ---

// FromUnixFunc creates a time from a Unix epoch value.
// Called as fromunix(n) for seconds (possibly fractional), or fromunix(n, unit)
// where unit is "s", "ms", "us", or "ns". Always returns UTC.
var FromUnixFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "n", Type: cty.Number},
	},
	VarParam: &function.Parameter{
		Name: "unit",
		Type: cty.String,
	},
	Type: function.StaticReturnType(hclutil.TimeCapsuleType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		unit := "s"
		if len(args) > 1 {
			unit = args[1].AsString()
		}
		n, _ := args[0].AsBigFloat().Float64()
		switch unit {
		case "s":
			secs := int64(n)
			nanos := int64((n - float64(secs)) * 1e9)
			return hclutil.NewTimeCapsule(time.Unix(secs, nanos).UTC()), nil
		case "ms":
			return hclutil.NewTimeCapsule(time.UnixMilli(int64(n)).UTC()), nil
		case "us":
			return hclutil.NewTimeCapsule(time.UnixMicro(int64(n)).UTC()), nil
		case "ns":
			return hclutil.NewTimeCapsule(time.Unix(0, int64(n)).UTC()), nil
		default:
			return cty.NilVal, fmt.Errorf("fromunix: unknown unit %q; valid units: s, ms, us, ns", unit)
		}
	},
})

// UnixFunc returns the Unix epoch value for a time.
// Called as unix(t) for fractional seconds, or unix(t, unit) where unit is
// "s" (float), "ms", "us", or "ns" (integers).
var UnixFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "t", Type: hclutil.TimeCapsuleType},
	},
	VarParam: &function.Parameter{
		Name: "unit",
		Type: cty.String,
	},
	Type: function.StaticReturnType(cty.Number),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		t, err := hclutil.GetTime(args[0])
		if err != nil {
			return cty.NilVal, err
		}
		unit := "s"
		if len(args) > 1 {
			unit = args[1].AsString()
		}
		switch unit {
		case "s":
			return cty.NumberFloatVal(float64(t.UnixNano()) / 1e9), nil
		case "ms":
			return cty.NumberIntVal(t.UnixMilli()), nil
		case "us":
			return cty.NumberIntVal(t.UnixMicro()), nil
		case "ns":
			return cty.NumberIntVal(t.UnixNano()), nil
		default:
			return cty.NilVal, fmt.Errorf("unix: unknown unit %q; valid units: s, ms, us, ns", unit)
		}
	},
})

// --- Decomposition ---

// TimePartFunc extracts a named calendar field from a time value in its stored timezone.
// Called as timepart(t, "year"), timepart(t, "month"), etc.
// Valid parts: year, month, day, hour, minute, second, nanosecond, weekday, yearday.
var TimePartFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "t", Type: hclutil.TimeCapsuleType},
		{Name: "part", Type: cty.String},
	},
	Type: function.StaticReturnType(cty.Number),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		t, err := hclutil.GetTime(args[0])
		if err != nil {
			return cty.NilVal, err
		}
		switch args[1].AsString() {
		case "year":
			return cty.NumberIntVal(int64(t.Year())), nil
		case "month":
			return cty.NumberIntVal(int64(t.Month())), nil
		case "day":
			return cty.NumberIntVal(int64(t.Day())), nil
		case "hour":
			return cty.NumberIntVal(int64(t.Hour())), nil
		case "minute":
			return cty.NumberIntVal(int64(t.Minute())), nil
		case "second":
			return cty.NumberIntVal(int64(t.Second())), nil
		case "nanosecond":
			return cty.NumberIntVal(int64(t.Nanosecond())), nil
		case "weekday":
			return cty.NumberIntVal(int64(t.Weekday())), nil
		case "yearday":
			return cty.NumberIntVal(int64(t.YearDay())), nil
		case "isoweek":
			_, week := t.ISOWeek()
			return cty.NumberIntVal(int64(week)), nil
		case "isoyear":
			year, _ := t.ISOWeek()
			return cty.NumberIntVal(int64(year)), nil
		default:
			return cty.NilVal, fmt.Errorf("timepart: unknown part %q; valid parts: year, month, day, hour, minute, second, nanosecond, weekday, yearday, isoweek, isoyear", args[1].AsString())
		}
	},
})

// DurationPartFunc extracts a duration expressed in the given unit.
// "h", "m", "s" return floats; "ms", "us", "ns" return integers.
var DurationPartFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "d", Type: hclutil.DurationCapsuleType},
		{Name: "unit", Type: cty.String},
	},
	Type: function.StaticReturnType(cty.Number),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		d, err := hclutil.GetDuration(args[0])
		if err != nil {
			return cty.NilVal, err
		}
		switch args[1].AsString() {
		case "h":
			return cty.NumberFloatVal(d.Hours()), nil
		case "m":
			return cty.NumberFloatVal(d.Minutes()), nil
		case "s":
			return cty.NumberFloatVal(d.Seconds()), nil
		case "ms":
			return cty.NumberIntVal(d.Milliseconds()), nil
		case "us":
			return cty.NumberIntVal(d.Microseconds()), nil
		case "ns":
			return cty.NumberIntVal(d.Nanoseconds()), nil
		default:
			return cty.NilVal, fmt.Errorf("durationpart: unknown unit %q; valid units: h, m, s, ms, us, ns", args[1].AsString())
		}
	},
})

// --- Timezone ---

// TimezoneFunc returns the timezone name.
// Called as timezone() for the local system timezone, or timezone(t) for the
// timezone stored in a time value.
var TimezoneFunc = function.New(&function.Spec{
	VarParam: &function.Parameter{
		Name: "t",
		Type: cty.DynamicPseudoType,
	},
	Type: func(args []cty.Value) (cty.Type, error) {
		if len(args) > 1 {
			return cty.NilType, fmt.Errorf("timezone() takes 0 or 1 arguments")
		}
		if len(args) == 1 {
			t := args[0].Type()
			if t != hclutil.TimeCapsuleType && t != cty.DynamicPseudoType {
				return cty.NilType, fmt.Errorf("timezone: argument must be a time value, got %s", t.FriendlyName())
			}
		}
		return cty.String, nil
	},
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		if len(args) == 0 {
			return cty.StringVal(time.Local.String()), nil
		}
		t, err := hclutil.GetTime(args[0])
		if err != nil {
			return cty.NilVal, err
		}
		return cty.StringVal(t.Location().String()), nil
	},
})

// InTimezoneFunc re-expresses a time in a different IANA timezone.
// The instant is unchanged; only the displayed timezone changes.
var InTimezoneFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "t", Type: hclutil.TimeCapsuleType},
		{Name: "tz", Type: cty.String},
	},
	Type: function.StaticReturnType(hclutil.TimeCapsuleType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		t, err := hclutil.GetTime(args[0])
		if err != nil {
			return cty.NilVal, err
		}
		loc, err := time.LoadLocation(args[1].AsString())
		if err != nil {
			return cty.NilVal, fmt.Errorf("intimezone: invalid timezone %q: %s", args[1].AsString(), err)
		}
		return hclutil.NewTimeCapsule(t.In(loc)), nil
	},
})

// Phase 2: Duration misc

// AbsDurationFunc returns the absolute value of a duration.
var AbsDurationFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "d", Type: hclutil.DurationCapsuleType},
	},
	Type: function.StaticReturnType(hclutil.DurationCapsuleType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		d, err := hclutil.GetDuration(args[0])
		if err != nil {
			return cty.NilVal, err
		}
		if d < 0 {
			d = -d
		}
		return hclutil.NewDurationCapsule(d), nil
	},
})

// --- Calendar arithmetic ---

// AddYearsFunc adds n calendar years to a time (calls time.Time.AddDate).
var AddYearsFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "t", Type: hclutil.TimeCapsuleType},
		{Name: "n", Type: cty.Number},
	},
	Type: function.StaticReturnType(hclutil.TimeCapsuleType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		t, err := hclutil.GetTime(args[0])
		if err != nil {
			return cty.NilVal, err
		}
		n, _ := args[1].AsBigFloat().Int64()
		return hclutil.NewTimeCapsule(t.AddDate(int(n), 0, 0)), nil
	},
})

// AddMonthsFunc adds n calendar months to a time (calls time.Time.AddDate).
var AddMonthsFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "t", Type: hclutil.TimeCapsuleType},
		{Name: "n", Type: cty.Number},
	},
	Type: function.StaticReturnType(hclutil.TimeCapsuleType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		t, err := hclutil.GetTime(args[0])
		if err != nil {
			return cty.NilVal, err
		}
		n, _ := args[1].AsBigFloat().Int64()
		return hclutil.NewTimeCapsule(t.AddDate(0, int(n), 0)), nil
	},
})

// AddDaysFunc adds n calendar days to a time (calls time.Time.AddDate).
var AddDaysFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "t", Type: hclutil.TimeCapsuleType},
		{Name: "n", Type: cty.Number},
	},
	Type: function.StaticReturnType(hclutil.TimeCapsuleType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		t, err := hclutil.GetTime(args[0])
		if err != nil {
			return cty.NilVal, err
		}
		n, _ := args[1].AsBigFloat().Int64()
		return hclutil.NewTimeCapsule(t.AddDate(0, 0, int(n))), nil
	},
})

// --- Comparison functions ---
// go-cty does not dispatch </>/<= etc. to capsule types, so ordering comparisons
// are provided as explicit functions.

// TimeBeforeFunc returns true if t1 is before t2.
var TimeBeforeFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "t1", Type: hclutil.TimeCapsuleType},
		{Name: "t2", Type: hclutil.TimeCapsuleType},
	},
	Type: function.StaticReturnType(cty.Bool),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		t1, err := hclutil.GetTime(args[0])
		if err != nil {
			return cty.NilVal, err
		}
		t2, err := hclutil.GetTime(args[1])
		if err != nil {
			return cty.NilVal, err
		}
		return cty.BoolVal(t1.Before(t2)), nil
	},
})

// TimeAfterFunc returns true if t1 is after t2.
var TimeAfterFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "t1", Type: hclutil.TimeCapsuleType},
		{Name: "t2", Type: hclutil.TimeCapsuleType},
	},
	Type: function.StaticReturnType(cty.Bool),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		t1, err := hclutil.GetTime(args[0])
		if err != nil {
			return cty.NilVal, err
		}
		t2, err := hclutil.GetTime(args[1])
		if err != nil {
			return cty.NilVal, err
		}
		return cty.BoolVal(t1.After(t2)), nil
	},
})

// DurationLtFunc returns true if d1 < d2.
var DurationLtFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "d1", Type: hclutil.DurationCapsuleType},
		{Name: "d2", Type: hclutil.DurationCapsuleType},
	},
	Type: function.StaticReturnType(cty.Bool),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		d1, err := hclutil.GetDuration(args[0])
		if err != nil {
			return cty.NilVal, err
		}
		d2, err := hclutil.GetDuration(args[1])
		if err != nil {
			return cty.NilVal, err
		}
		return cty.BoolVal(d1 < d2), nil
	},
})

// DurationGtFunc returns true if d1 > d2.
var DurationGtFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "d1", Type: hclutil.DurationCapsuleType},
		{Name: "d2", Type: hclutil.DurationCapsuleType},
	},
	Type: function.StaticReturnType(cty.Bool),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		d1, err := hclutil.GetDuration(args[0])
		if err != nil {
			return cty.NilVal, err
		}
		d2, err := hclutil.GetDuration(args[1])
		if err != nil {
			return cty.NilVal, err
		}
		return cty.BoolVal(d1 > d2), nil
	},
})

// --- strftime / strptime ---

// StrftimeFunc formats a time using a strftime-style format string (via itchyny/timefmt-go).
// Called as strftime("%Y-%m-%d", t).
var StrftimeFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "format", Type: cty.String},
		{Name: "t", Type: hclutil.TimeCapsuleType},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		t, err := hclutil.GetTime(args[1])
		if err != nil {
			return cty.NilVal, err
		}
		return cty.StringVal(timefmt.Format(t, args[0].AsString())), nil
	},
})

// StrptimeFunc parses a time string using a strftime-style format (via itchyny/timefmt-go).
// Called as strptime("%Y-%m-%d", "2024-01-15") or strptime("%Y-%m-%d", "2024-01-15", "UTC").
var StrptimeFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "format", Type: cty.String},
		{Name: "s", Type: cty.String},
	},
	VarParam: &function.Parameter{Name: "tz", Type: cty.String},
	Type: func(args []cty.Value) (cty.Type, error) {
		if len(args) > 3 {
			return cty.NilType, fmt.Errorf("strptime() takes 2 or 3 arguments")
		}
		return hclutil.TimeCapsuleType, nil
	},
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		t, err := timefmt.Parse(args[1].AsString(), args[0].AsString())
		if err != nil {
			return cty.NilVal, fmt.Errorf("strptime: cannot parse %q with format %q: %s", args[1].AsString(), args[0].AsString(), err)
		}
		if len(args) == 3 {
			loc, err := time.LoadLocation(args[2].AsString())
			if err != nil {
				return cty.NilVal, fmt.Errorf("strptime: invalid timezone %q: %s", args[2].AsString(), err)
			}
			// Reinterpret the parsed wall-clock components as being in the given timezone,
			// rather than converting the UTC instant.
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), loc)
		}
		return hclutil.NewTimeCapsule(t), nil
	},
})

// --- duration arithmetic ---

// DurationAddFunc adds two durations: d1 + d2
var DurationAddFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "d1", Type: hclutil.DurationCapsuleType},
		{Name: "d2", Type: hclutil.DurationCapsuleType},
	},
	Type: function.StaticReturnType(hclutil.DurationCapsuleType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		d1, _ := hclutil.GetDuration(args[0])
		d2, _ := hclutil.GetDuration(args[1])
		return hclutil.NewDurationCapsule(d1 + d2), nil
	},
})

// DurationSubFunc subtracts durations: d1 - d2
var DurationSubFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "d1", Type: hclutil.DurationCapsuleType},
		{Name: "d2", Type: hclutil.DurationCapsuleType},
	},
	Type: function.StaticReturnType(hclutil.DurationCapsuleType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		d1, _ := hclutil.GetDuration(args[0])
		d2, _ := hclutil.GetDuration(args[1])
		return hclutil.NewDurationCapsule(d1 - d2), nil
	},
})

// DurationMulFunc multiplies a duration by a scalar: d * n
var DurationMulFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "d", Type: hclutil.DurationCapsuleType},
		{Name: "n", Type: cty.Number},
	},
	Type: function.StaticReturnType(hclutil.DurationCapsuleType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		d, _ := hclutil.GetDuration(args[0])
		n, _ := args[1].AsBigFloat().Float64()
		return hclutil.NewDurationCapsule(time.Duration(float64(d) * n)), nil
	},
})

// DurationDivFunc divides a duration by a scalar: d / n (returns duration)
var DurationDivFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "d", Type: hclutil.DurationCapsuleType},
		{Name: "n", Type: cty.Number},
	},
	Type: function.StaticReturnType(hclutil.DurationCapsuleType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		d, _ := hclutil.GetDuration(args[0])
		n, _ := args[1].AsBigFloat().Float64()
		if n == 0 {
			return cty.NilVal, fmt.Errorf("durationdiv: division by zero")
		}
		return hclutil.NewDurationCapsule(time.Duration(float64(d) / n)), nil
	},
})

// DurationTruncateFunc truncates d to a multiple of m: d.Truncate(m)
var DurationTruncateFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "d", Type: hclutil.DurationCapsuleType},
		{Name: "m", Type: hclutil.DurationCapsuleType},
	},
	Type: function.StaticReturnType(hclutil.DurationCapsuleType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		d, _ := hclutil.GetDuration(args[0])
		m, _ := hclutil.GetDuration(args[1])
		return hclutil.NewDurationCapsule(d.Truncate(m)), nil
	},
})

// DurationRoundFunc rounds d to the nearest multiple of m: d.Round(m)
var DurationRoundFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "d", Type: hclutil.DurationCapsuleType},
		{Name: "m", Type: hclutil.DurationCapsuleType},
	},
	Type: function.StaticReturnType(hclutil.DurationCapsuleType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		d, _ := hclutil.GetDuration(args[0])
		m, _ := hclutil.GetDuration(args[1])
		return hclutil.NewDurationCapsule(d.Round(m)), nil
	},
})

// --- DNS zone serial numbers ---

// parseSerialArg parses a zone serial from a cty.Number or cty.String value.
func parseSerialArg(v cty.Value, funcName string) (int64, error) {
	switch v.Type() {
	case cty.Number:
		n, _ := v.AsBigFloat().Int64()
		return n, nil
	case cty.String:
		n, err := strconv.ParseInt(v.AsString(), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s: invalid serial %q: %s", funcName, v.AsString(), err)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("%s: serial must be a number or string, got %s", funcName, v.Type().FriendlyName())
	}
}

// NextZoneSerialFunc computes the next DNS zone serial number in YYYYMMDDNN format.
//
// Called as nextzoneserial(s) or nextzoneserial(s, t).
//
//	s: current serial (number or string)
//	t: optional time capsule; defaults to now()
//
// Computes x = first serial of the day for t (YYYYMMDD * 100), then returns max(s+1, x).
var NextZoneSerialFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "s", Type: cty.DynamicPseudoType},
	},
	VarParam: &function.Parameter{
		Name: "t",
		Type: cty.DynamicPseudoType,
	},
	Type: func(args []cty.Value) (cty.Type, error) {
		if len(args) > 2 {
			return cty.NilType, fmt.Errorf("nextzoneserial() takes 1 or 2 arguments")
		}
		t0 := args[0].Type()
		if t0 != cty.Number && t0 != cty.String && t0 != cty.DynamicPseudoType {
			return cty.NilType, fmt.Errorf("nextzoneserial: serial must be a number or string, got %s", t0.FriendlyName())
		}
		if len(args) == 2 {
			t1 := args[1].Type()
			if t1 != hclutil.TimeCapsuleType && t1 != cty.DynamicPseudoType {
				return cty.NilType, fmt.Errorf("nextzoneserial: second argument must be a time value, got %s", t1.FriendlyName())
			}
		}
		return cty.Number, nil
	},
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		s, err := parseSerialArg(args[0], "nextzoneserial")
		if err != nil {
			return cty.NilVal, err
		}
		var t time.Time
		if len(args) == 2 {
			t, err = hclutil.GetTime(args[1])
			if err != nil {
				return cty.NilVal, err
			}
		} else {
			t = time.Now()
		}
		year, month, day := t.Date()
		x := int64(year)*1_000_000 + int64(month)*10_000 + int64(day)*100
		return cty.NumberIntVal(max(s+1, x)), nil
	},
})

// ParseZoneSerialFunc converts a DNS zone serial back to an approximate time value.
// The serial format is YYYYMMDDNN; the NN sequence number is ignored.
// For out-of-range date components, the nearest valid date is used:
// month > 12 → December 31; day > days in month → last day of month.
var ParseZoneSerialFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "s", Type: cty.DynamicPseudoType},
	},
	Type: func(args []cty.Value) (cty.Type, error) {
		t := args[0].Type()
		if t != cty.Number && t != cty.String && t != cty.DynamicPseudoType {
			return cty.NilType, fmt.Errorf("parsezoneserial: serial must be a number or string, got %s", t.FriendlyName())
		}
		return hclutil.TimeCapsuleType, nil
	},
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		s, err := parseSerialArg(args[0], "parsezoneserial")
		if err != nil {
			return cty.NilVal, err
		}
		datepart := s / 100
		year := int(datepart / 10_000)
		month := time.Month((datepart / 100) % 100)
		day := int(datepart % 100)

		// Snap invalid components to the nearest valid date.
		if month < 1 {
			month = 1
		}
		if month > 12 {
			month = 12
			day = 31 // last day of December — already valid
		}
		if day < 1 {
			day = 1
		}
		if last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day(); day > last {
			day = last
		}
		return hclutil.NewTimeCapsule(time.Date(year, month, day, 0, 0, 0, 0, time.UTC)), nil
	},
})

// GetTimeFunctions returns all time-related functions for registration in the eval context.
// The "timeadd" entry here replaces the stdlib version.
func GetTimeFunctions() map[string]function.Function {
	return map[string]function.Function{
		// Phase 1
		"now":            NowFunc,
		"parsetime":      ParseTimeFunc,
		"duration":       DurationFunc,
		"timeadd":        TimeAddFunc,
		"timesub":        TimeSubFunc,
		"since":          SinceFunc,
		"until":          UntilFunc,
		"formattime":     FormatTimeFunc,
		"formatduration": FormatDurationFunc,
		// Phase 2
		"fromunix":     FromUnixFunc,
		"unix":         UnixFunc,
		"timepart":     TimePartFunc,
		"durationpart": DurationPartFunc,
		"timezone":     TimezoneFunc,
		"intimezone":   InTimezoneFunc,
		"absduration":  AbsDurationFunc,
		"addyears":     AddYearsFunc,
		"addmonths":    AddMonthsFunc,
		"adddays":      AddDaysFunc,
		"timebefore":   TimeBeforeFunc,
		"timeafter":    TimeAfterFunc,
		"durationlt":   DurationLtFunc,
		"durationgt":   DurationGtFunc,
		// Phase 3
		"strftime":          StrftimeFunc,
		"strptime":          StrptimeFunc,
		"durationadd":       DurationAddFunc,
		"durationsub":       DurationSubFunc,
		"durationmul":       DurationMulFunc,
		"durationdiv":       DurationDivFunc,
		"durationtruncate":  DurationTruncateFunc,
		"durationround":     DurationRoundFunc,
		// DNS zone serials
		"nextzoneserial":  NextZoneSerialFunc,
		"parsezoneserial": ParseZoneSerialFunc,
	}
}

// --- helpers ---

// namedFormats maps @name aliases to Go reference-time layout strings.
var namedFormats = map[string]string{
	"ansic":       time.ANSIC,
	"unixdate":    time.UnixDate,
	"rubydate":    time.RubyDate,
	"rfc822":      time.RFC822,
	"rfc822z":     time.RFC822Z,
	"rfc850":      time.RFC850,
	"rfc1123":     time.RFC1123,
	"rfc1123z":    time.RFC1123Z,
	"rfc3339":     time.RFC3339,
	"rfc3339nano": time.RFC3339Nano,
	"kitchen":     time.Kitchen,
	"stamp":       time.Stamp,
	"stampmilli":  time.StampMilli,
	"stampmicro":  time.StampMicro,
	"stampnano":   time.StampNano,
	"datetime":    time.DateTime,
	"date":        time.DateOnly,
	"time":        time.TimeOnly,
}

// resolveFormat resolves an @-prefixed named format to its Go layout string.
// Strings not starting with @ are returned unchanged.
func resolveFormat(s string) (string, error) {
	if !strings.HasPrefix(s, "@") {
		return s, nil
	}
	name := strings.ToLower(s[1:])
	if layout, ok := namedFormats[name]; ok {
		return layout, nil
	}
	return "", fmt.Errorf("unknown named format %q; valid names: @ansic, @unixdate, @rubydate, @rfc822, @rfc822z, @rfc850, @rfc1123, @rfc1123z, @rfc3339, @rfc3339nano, @kitchen, @stamp, @stampmilli, @stampmicro, @stampnano, @datetime, @date, @time", s)
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
