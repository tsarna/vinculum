package functions

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tsarna/vinculum/internal/hclutil"
	"github.com/zclconf/go-cty/cty"
)

// --- now ---

func TestNowNoArgs(t *testing.T) {
	before := time.Now()
	result, err := NowFunc.Call([]cty.Value{})
	after := time.Now()
	require.NoError(t, err)
	assert.Equal(t, hclutil.TimeCapsuleType, result.Type())
	got, _ := hclutil.GetTime(result)
	assert.True(t, !got.Before(before) && !got.After(after))
}

func TestNowUTC(t *testing.T) {
	result, err := NowFunc.Call([]cty.Value{cty.StringVal("UTC")})
	require.NoError(t, err)
	got, _ := hclutil.GetTime(result)
	assert.Equal(t, "UTC", got.Location().String())
}

func TestNowNamedTZ(t *testing.T) {
	result, err := NowFunc.Call([]cty.Value{cty.StringVal("America/New_York")})
	require.NoError(t, err)
	got, _ := hclutil.GetTime(result)
	assert.Equal(t, "America/New_York", got.Location().String())
}

func TestNowInvalidTZ(t *testing.T) {
	_, err := NowFunc.Call([]cty.Value{cty.StringVal("Not/ATimezone")})
	assert.Error(t, err)
}

// --- parsetime ---

func TestParseTimeRFC3339(t *testing.T) {
	result, err := ParseTimeFunc.Call([]cty.Value{cty.StringVal("2024-01-15T10:30:00Z")})
	require.NoError(t, err)
	assert.Equal(t, hclutil.TimeCapsuleType, result.Type())
	got, _ := hclutil.GetTime(result)
	assert.Equal(t, 2024, got.Year())
	assert.Equal(t, time.January, got.Month())
	assert.Equal(t, 15, got.Day())
	assert.Equal(t, 10, got.Hour())
	assert.Equal(t, 30, got.Minute())
	assert.Equal(t, 0, got.Second())
	assert.Equal(t, "UTC", got.Location().String())
}

func TestParseTimeRFC3339Nano(t *testing.T) {
	result, err := ParseTimeFunc.Call([]cty.Value{cty.StringVal("2024-01-15T10:30:00.123456789Z")})
	require.NoError(t, err)
	got, _ := hclutil.GetTime(result)
	assert.Equal(t, 123456789, got.Nanosecond())
}

func TestParseTimeWithOffset(t *testing.T) {
	result, err := ParseTimeFunc.Call([]cty.Value{cty.StringVal("2024-01-15T10:30:00+05:30")})
	require.NoError(t, err)
	got, _ := hclutil.GetTime(result)
	_, offset := got.Zone()
	assert.Equal(t, 5*3600+30*60, offset)
}

func TestParseTimeInvalid(t *testing.T) {
	_, err := ParseTimeFunc.Call([]cty.Value{cty.StringVal("not a time")})
	assert.Error(t, err)
}

// --- duration ---

func TestDurationGoFormat(t *testing.T) {
	result, err := DurationFunc.Call([]cty.Value{cty.StringVal("5m30s")})
	require.NoError(t, err)
	assert.Equal(t, hclutil.DurationCapsuleType, result.Type())
	d, _ := hclutil.GetDuration(result)
	assert.Equal(t, 5*time.Minute+30*time.Second, d)
}

func TestDurationISO8601(t *testing.T) {
	result, err := DurationFunc.Call([]cty.Value{cty.StringVal("PT5M")})
	require.NoError(t, err)
	d, _ := hclutil.GetDuration(result)
	assert.Equal(t, 5*time.Minute, d)
}

func TestDurationISO8601Complex(t *testing.T) {
	result, err := DurationFunc.Call([]cty.Value{cty.StringVal("PT1H30M")})
	require.NoError(t, err)
	d, _ := hclutil.GetDuration(result)
	assert.Equal(t, 90*time.Minute, d)
}

func TestDurationCalendarError(t *testing.T) {
	_, err := DurationFunc.Call([]cty.Value{cty.StringVal("P1Y")})
	assert.Error(t, err)

	_, err = DurationFunc.Call([]cty.Value{cty.StringVal("P1M")})
	assert.Error(t, err)
}

func TestDurationFromNumber(t *testing.T) {
	tests := []struct {
		n    float64
		unit string
		want time.Duration
	}{
		{5, "h", 5 * time.Hour},
		{30, "m", 30 * time.Minute},
		{10, "s", 10 * time.Second},
		{500, "ms", 500 * time.Millisecond},
		{1000, "us", 1000 * time.Microsecond},
		{1000000, "ns", 1000000 * time.Nanosecond},
		{1.5, "s", 1500 * time.Millisecond},
	}
	for _, tt := range tests {
		result, err := DurationFunc.Call([]cty.Value{
			cty.NumberFloatVal(tt.n),
			cty.StringVal(tt.unit),
		})
		require.NoError(t, err, "duration(%v, %q)", tt.n, tt.unit)
		d, _ := hclutil.GetDuration(result)
		assert.Equal(t, tt.want, d, "duration(%v, %q)", tt.n, tt.unit)
	}
}

func TestDurationInvalidUnit(t *testing.T) {
	_, err := DurationFunc.Call([]cty.Value{cty.NumberIntVal(5), cty.StringVal("days")})
	assert.Error(t, err)
}

// --- timeadd ---

func TestTimeAddStringString(t *testing.T) {
	// Backward-compatible string/string form
	result, err := TimeAddFunc.Call([]cty.Value{
		cty.StringVal("2024-01-15T10:30:00Z"),
		cty.StringVal("1h"),
	})
	require.NoError(t, err)
	assert.Equal(t, cty.String, result.Type())
	assert.Equal(t, "2024-01-15T11:30:00Z", result.AsString())
}

func TestTimeAddTimeDuration(t *testing.T) {
	ts := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC))
	dur := hclutil.NewDurationCapsule(time.Hour)
	result, err := TimeAddFunc.Call([]cty.Value{ts, dur})
	require.NoError(t, err)
	assert.Equal(t, hclutil.TimeCapsuleType, result.Type())
	got, _ := hclutil.GetTime(result)
	assert.Equal(t, 11, got.Hour())
}

func TestTimeAddTimeString(t *testing.T) {
	ts := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC))
	result, err := TimeAddFunc.Call([]cty.Value{ts, cty.StringVal("30m")})
	require.NoError(t, err)
	assert.Equal(t, hclutil.TimeCapsuleType, result.Type())
	got, _ := hclutil.GetTime(result)
	assert.Equal(t, 11, got.Hour())
	assert.Equal(t, 0, got.Minute())
}

func TestTimeAddStringDuration(t *testing.T) {
	dur := hclutil.NewDurationCapsule(time.Hour)
	result, err := TimeAddFunc.Call([]cty.Value{
		cty.StringVal("2024-01-15T10:30:00Z"),
		dur,
	})
	require.NoError(t, err)
	assert.Equal(t, hclutil.TimeCapsuleType, result.Type())
	got, _ := hclutil.GetTime(result)
	assert.Equal(t, 11, got.Hour())
}

// --- timesub ---

func TestTimeSubTimesReturnsDuration(t *testing.T) {
	t1 := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 11, 30, 0, 0, time.UTC))
	t2 := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC))
	result, err := TimeSubFunc.Call([]cty.Value{t1, t2})
	require.NoError(t, err)
	assert.Equal(t, hclutil.DurationCapsuleType, result.Type())
	d, _ := hclutil.GetDuration(result)
	assert.Equal(t, time.Hour, d)
}

func TestTimeSubTimesNegative(t *testing.T) {
	t1 := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC))
	t2 := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 11, 30, 0, 0, time.UTC))
	result, err := TimeSubFunc.Call([]cty.Value{t1, t2})
	require.NoError(t, err)
	d, _ := hclutil.GetDuration(result)
	assert.Equal(t, -time.Hour, d)
}

func TestTimeSubTimeDurationReturnsTime(t *testing.T) {
	ts := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 11, 30, 0, 0, time.UTC))
	dur := hclutil.NewDurationCapsule(time.Hour)
	result, err := TimeSubFunc.Call([]cty.Value{ts, dur})
	require.NoError(t, err)
	assert.Equal(t, hclutil.TimeCapsuleType, result.Type())
	got, _ := hclutil.GetTime(result)
	assert.Equal(t, 10, got.Hour())
	assert.Equal(t, 30, got.Minute())
}

// --- since / until ---

func TestSince(t *testing.T) {
	past := hclutil.NewTimeCapsule(time.Now().Add(-5 * time.Second))
	result, err := SinceFunc.Call([]cty.Value{past})
	require.NoError(t, err)
	d, _ := hclutil.GetDuration(result)
	assert.True(t, d >= 5*time.Second)
	assert.True(t, d < 10*time.Second)
}

func TestUntil(t *testing.T) {
	future := hclutil.NewTimeCapsule(time.Now().Add(5 * time.Second))
	result, err := UntilFunc.Call([]cty.Value{future})
	require.NoError(t, err)
	d, _ := hclutil.GetDuration(result)
	assert.True(t, d > 0)
	assert.True(t, d <= 5*time.Second)
}

// --- formattime ---

func TestFormatTime(t *testing.T) {
	ts := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC))
	result, err := FormatTimeFunc.Call([]cty.Value{
		cty.StringVal("2006-01-02"),
		ts,
	})
	require.NoError(t, err)
	assert.Equal(t, "2024-01-15", result.AsString())
}

func TestFormatTimeRFC3339(t *testing.T) {
	ts := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC))
	result, err := FormatTimeFunc.Call([]cty.Value{
		cty.StringVal("2006-01-02T15:04:05Z07:00"),
		ts,
	})
	require.NoError(t, err)
	assert.Equal(t, "2024-01-15T10:30:00Z", result.AsString())
}

// --- formatduration ---

func TestFormatDurationGo(t *testing.T) {
	dur := hclutil.NewDurationCapsule(90 * time.Minute)
	result, err := FormatDurationFunc.Call([]cty.Value{dur})
	require.NoError(t, err)
	assert.Equal(t, "1h30m0s", result.AsString())
}

func TestFormatDurationGoExplicit(t *testing.T) {
	dur := hclutil.NewDurationCapsule(90 * time.Minute)
	result, err := FormatDurationFunc.Call([]cty.Value{dur, cty.StringVal("go")})
	require.NoError(t, err)
	assert.Equal(t, "1h30m0s", result.AsString())
}

func TestFormatDurationISO(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "PT0S"},
		{5 * time.Minute, "PT5M"},
		{90 * time.Minute, "PT1H30M"},
		{time.Hour + 30*time.Minute + 15*time.Second, "PT1H30M15S"},
		{500 * time.Millisecond, "PT0.5S"},
		{-5 * time.Minute, "-PT5M"},
	}
	for _, tt := range tests {
		dur := hclutil.NewDurationCapsule(tt.d)
		result, err := FormatDurationFunc.Call([]cty.Value{dur, cty.StringVal("iso")})
		require.NoError(t, err, "formatduration(%v, \"iso\")", tt.d)
		assert.Equal(t, tt.want, result.AsString(), "formatduration(%v, \"iso\")", tt.d)
	}
}

func TestFormatDurationInvalidFormat(t *testing.T) {
	dur := hclutil.NewDurationCapsule(time.Minute)
	_, err := FormatDurationFunc.Call([]cty.Value{dur, cty.StringVal("invalid")})
	assert.Error(t, err)
}

// --- equality via Equals/RawEquals ---
// Note: go-cty v1.18.0 only supports == and != for capsule types.
// Ordering (<, >, etc.) requires either a newer go-cty with RichCompare
// or converting to a number first (e.g. via durationpart).

func TestTimeCapsuleEquality(t *testing.T) {
	t1 := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC))
	t2 := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC))
	t3 := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC))

	// Same instant — equal
	assert.True(t, t1.Equals(t3).True())
	// Different instants — not equal
	assert.True(t, t1.Equals(t2).False())
}

func TestTimeCapsuleEqualityAcrossTimezones(t *testing.T) {
	utc := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	ny, _ := time.LoadLocation("America/New_York")
	// Same instant expressed in different timezones
	utcVal := hclutil.NewTimeCapsule(utc)
	nyVal := hclutil.NewTimeCapsule(utc.In(ny))
	assert.True(t, utcVal.Equals(nyVal).True())
}

func TestDurationCapsuleEquality(t *testing.T) {
	d1 := hclutil.NewDurationCapsule(5 * time.Minute)
	d2 := hclutil.NewDurationCapsule(10 * time.Minute)
	d3 := hclutil.NewDurationCapsule(5 * time.Minute)

	assert.True(t, d1.Equals(d3).True())
	assert.True(t, d1.Equals(d2).False())
}

// --- Phase 2: fromunix / unix ---

func TestFromUnixSeconds(t *testing.T) {
	result, err := FromUnixFunc.Call([]cty.Value{cty.NumberIntVal(0)})
	require.NoError(t, err)
	got, _ := hclutil.GetTime(result)
	assert.Equal(t, time.Unix(0, 0).UTC(), got)
}

func TestFromUnixFractionalSeconds(t *testing.T) {
	result, err := FromUnixFunc.Call([]cty.Value{cty.NumberFloatVal(1.5)})
	require.NoError(t, err)
	got, _ := hclutil.GetTime(result)
	assert.Equal(t, time.Unix(1, 500_000_000).UTC(), got)
}

func TestFromUnixUnits(t *testing.T) {
	base := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	tests := []struct {
		n    int64
		unit string
	}{
		{base.Unix(), "s"},
		{base.UnixMilli(), "ms"},
		{base.UnixMicro(), "us"},
		{base.UnixNano(), "ns"},
	}
	for _, tt := range tests {
		result, err := FromUnixFunc.Call([]cty.Value{cty.NumberIntVal(tt.n), cty.StringVal(tt.unit)})
		require.NoError(t, err, "fromunix(%d, %q)", tt.n, tt.unit)
		got, _ := hclutil.GetTime(result)
		assert.True(t, base.Equal(got), "fromunix(%d, %q): got %v", tt.n, tt.unit, got)
	}
}

func TestFromUnixInvalidUnit(t *testing.T) {
	_, err := FromUnixFunc.Call([]cty.Value{cty.NumberIntVal(0), cty.StringVal("days")})
	assert.Error(t, err)
}

func TestUnixSeconds(t *testing.T) {
	ts := hclutil.NewTimeCapsule(time.Unix(1705312200, 500_000_000).UTC())
	result, err := UnixFunc.Call([]cty.Value{ts})
	require.NoError(t, err)
	f, _ := result.AsBigFloat().Float64()
	assert.InDelta(t, 1705312200.5, f, 1e-6)
}

func TestUnixUnits(t *testing.T) {
	base := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	ts := hclutil.NewTimeCapsule(base)
	tests := []struct {
		unit string
		want int64
	}{
		{"ms", base.UnixMilli()},
		{"us", base.UnixMicro()},
		{"ns", base.UnixNano()},
	}
	for _, tt := range tests {
		result, err := UnixFunc.Call([]cty.Value{ts, cty.StringVal(tt.unit)})
		require.NoError(t, err)
		got, _ := result.AsBigFloat().Int64()
		assert.Equal(t, tt.want, got, "unix(t, %q)", tt.unit)
	}
}

// --- Phase 2: timepart ---

func TestTimePart(t *testing.T) {
	// 2024-01-15 (Monday) 10:30:45.123456789 UTC, day 15, yearday 15
	ts := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 10, 30, 45, 123456789, time.UTC))
	tests := []struct {
		part string
		want int64
	}{
		{"year", 2024},
		{"month", 1},
		{"day", 15},
		{"hour", 10},
		{"minute", 30},
		{"second", 45},
		{"nanosecond", 123456789},
		{"weekday", 1}, // Monday
		{"yearday", 15},
	}
	for _, tt := range tests {
		result, err := TimePartFunc.Call([]cty.Value{ts, cty.StringVal(tt.part)})
		require.NoError(t, err, "timepart(t, %q)", tt.part)
		got, _ := result.AsBigFloat().Int64()
		assert.Equal(t, tt.want, got, "timepart(t, %q)", tt.part)
	}
}

func TestTimePartInvalid(t *testing.T) {
	ts := hclutil.NewTimeCapsule(time.Now())
	_, err := TimePartFunc.Call([]cty.Value{ts, cty.StringVal("quarter")})
	assert.Error(t, err)
}

func TestTimePartUsesStoredTimezone(t *testing.T) {
	// 10:30 UTC = 05:30 New York (EST, UTC-5)
	utc := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	ny, _ := time.LoadLocation("America/New_York")
	ts := hclutil.NewTimeCapsule(utc.In(ny))
	result, err := TimePartFunc.Call([]cty.Value{ts, cty.StringVal("hour")})
	require.NoError(t, err)
	got, _ := result.AsBigFloat().Int64()
	assert.Equal(t, int64(5), got) // 05:30 in New York
}

// --- Phase 2: durationpart ---

func TestDurationPart(t *testing.T) {
	base := 90*time.Minute + 30*time.Second + 500*time.Millisecond
	d := hclutil.NewDurationCapsule(base)

	floatCases := []struct {
		unit string
		want float64
	}{
		{"h", base.Hours()},
		{"m", base.Minutes()},
		{"s", base.Seconds()},
	}
	for _, tt := range floatCases {
		result, err := DurationPartFunc.Call([]cty.Value{d, cty.StringVal(tt.unit)})
		require.NoError(t, err, "durationpart(d, %q)", tt.unit)
		got, _ := result.AsBigFloat().Float64()
		assert.InDelta(t, tt.want, got, 1e-9, "durationpart(d, %q)", tt.unit)
	}

	intCases := []struct {
		unit string
		want int64
	}{
		{"ms", base.Milliseconds()},
		{"us", base.Microseconds()},
		{"ns", base.Nanoseconds()},
	}
	for _, tt := range intCases {
		result, err := DurationPartFunc.Call([]cty.Value{d, cty.StringVal(tt.unit)})
		require.NoError(t, err, "durationpart(d, %q)", tt.unit)
		got, _ := result.AsBigFloat().Int64()
		assert.Equal(t, tt.want, got, "durationpart(d, %q)", tt.unit)
	}
}

// --- Phase 2: timezone / intimezone ---

func TestTimezoneNoArgs(t *testing.T) {
	result, err := TimezoneFunc.Call([]cty.Value{})
	require.NoError(t, err)
	assert.Equal(t, cty.String, result.Type())
	// Should be a non-empty string
	assert.NotEmpty(t, result.AsString())
}

func TestTimezoneWithTime(t *testing.T) {
	utc := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC))
	result, err := TimezoneFunc.Call([]cty.Value{utc})
	require.NoError(t, err)
	assert.Equal(t, "UTC", result.AsString())
}

func TestTimezoneNamedZone(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	ts := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 10, 0, 0, 0, ny))
	result, err := TimezoneFunc.Call([]cty.Value{ts})
	require.NoError(t, err)
	assert.Equal(t, "America/New_York", result.AsString())
}

func TestInTimezone(t *testing.T) {
	utc := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	ts := hclutil.NewTimeCapsule(utc)
	result, err := InTimezoneFunc.Call([]cty.Value{ts, cty.StringVal("America/New_York")})
	require.NoError(t, err)
	got, _ := hclutil.GetTime(result)
	// Same instant
	assert.True(t, utc.Equal(got))
	// Different display timezone
	assert.Equal(t, "America/New_York", got.Location().String())
	// 10:00 UTC = 05:00 EST
	assert.Equal(t, 5, got.Hour())
}

func TestInTimezoneInvalidZone(t *testing.T) {
	ts := hclutil.NewTimeCapsule(time.Now())
	_, err := InTimezoneFunc.Call([]cty.Value{ts, cty.StringVal("Not/ATimezone")})
	assert.Error(t, err)
}

// --- Phase 2: absduration ---

func TestAbsDurationPositive(t *testing.T) {
	d := hclutil.NewDurationCapsule(5 * time.Minute)
	result, err := AbsDurationFunc.Call([]cty.Value{d})
	require.NoError(t, err)
	got, _ := hclutil.GetDuration(result)
	assert.Equal(t, 5*time.Minute, got)
}

func TestAbsDurationNegative(t *testing.T) {
	d := hclutil.NewDurationCapsule(-5 * time.Minute)
	result, err := AbsDurationFunc.Call([]cty.Value{d})
	require.NoError(t, err)
	got, _ := hclutil.GetDuration(result)
	assert.Equal(t, 5*time.Minute, got)
}

// --- Phase 2: calendar arithmetic ---

func TestAddYears(t *testing.T) {
	ts := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC))
	result, err := AddYearsFunc.Call([]cty.Value{ts, cty.NumberIntVal(2)})
	require.NoError(t, err)
	got, _ := hclutil.GetTime(result)
	assert.Equal(t, 2026, got.Year())
	assert.Equal(t, time.January, got.Month())
	assert.Equal(t, 15, got.Day())
}

func TestAddYearsLeapDay(t *testing.T) {
	// Feb 29 on leap year + 1 year = Feb 28 on non-leap year (Go's AddDate behaviour)
	ts := hclutil.NewTimeCapsule(time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC))
	result, err := AddYearsFunc.Call([]cty.Value{ts, cty.NumberIntVal(1)})
	require.NoError(t, err)
	got, _ := hclutil.GetTime(result)
	assert.Equal(t, 2025, got.Year())
	assert.Equal(t, time.March, got.Month()) // Go normalises Feb 29 → Mar 1
	assert.Equal(t, 1, got.Day())
}

func TestAddMonths(t *testing.T) {
	ts := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC))
	result, err := AddMonthsFunc.Call([]cty.Value{ts, cty.NumberIntVal(3)})
	require.NoError(t, err)
	got, _ := hclutil.GetTime(result)
	assert.Equal(t, time.April, got.Month())
	assert.Equal(t, 15, got.Day())
}

func TestAddDays(t *testing.T) {
	ts := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC))
	result, err := AddDaysFunc.Call([]cty.Value{ts, cty.NumberIntVal(20)})
	require.NoError(t, err)
	got, _ := hclutil.GetTime(result)
	assert.Equal(t, time.February, got.Month())
	assert.Equal(t, 4, got.Day())
}

func TestAddDaysNegative(t *testing.T) {
	ts := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC))
	result, err := AddDaysFunc.Call([]cty.Value{ts, cty.NumberIntVal(-5)})
	require.NoError(t, err)
	got, _ := hclutil.GetTime(result)
	assert.Equal(t, time.January, got.Month())
	assert.Equal(t, 10, got.Day())
}

// --- Phase 2: comparison functions ---

func TestTimeBefore(t *testing.T) {
	t1 := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC))
	t2 := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC))

	result, err := TimeBeforeFunc.Call([]cty.Value{t1, t2})
	require.NoError(t, err)
	assert.True(t, result.True())

	result, err = TimeBeforeFunc.Call([]cty.Value{t2, t1})
	require.NoError(t, err)
	assert.False(t, result.True())

	// Equal times: not before
	result, err = TimeBeforeFunc.Call([]cty.Value{t1, t1})
	require.NoError(t, err)
	assert.False(t, result.True())
}

func TestTimeAfter(t *testing.T) {
	t1 := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC))
	t2 := hclutil.NewTimeCapsule(time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC))

	result, err := TimeAfterFunc.Call([]cty.Value{t2, t1})
	require.NoError(t, err)
	assert.True(t, result.True())

	result, err = TimeAfterFunc.Call([]cty.Value{t1, t2})
	require.NoError(t, err)
	assert.False(t, result.True())
}

func TestDurationLt(t *testing.T) {
	d1 := hclutil.NewDurationCapsule(5 * time.Minute)
	d2 := hclutil.NewDurationCapsule(10 * time.Minute)

	result, err := DurationLtFunc.Call([]cty.Value{d1, d2})
	require.NoError(t, err)
	assert.True(t, result.True())

	result, err = DurationLtFunc.Call([]cty.Value{d2, d1})
	require.NoError(t, err)
	assert.False(t, result.True())

	// Equal: not less than
	result, err = DurationLtFunc.Call([]cty.Value{d1, d1})
	require.NoError(t, err)
	assert.False(t, result.True())
}

func TestDurationGt(t *testing.T) {
	d1 := hclutil.NewDurationCapsule(5 * time.Minute)
	d2 := hclutil.NewDurationCapsule(10 * time.Minute)

	result, err := DurationGtFunc.Call([]cty.Value{d2, d1})
	require.NoError(t, err)
	assert.True(t, result.True())

	result, err = DurationGtFunc.Call([]cty.Value{d1, d2})
	require.NoError(t, err)
	assert.False(t, result.True())
}

// --- durationToISO8601 helper ---

func TestDurationToISO8601(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "PT0S"},
		{time.Second, "PT1S"},
		{time.Minute, "PT1M"},
		{time.Hour, "PT1H"},
		{24 * time.Hour, "PT24H"},
		{time.Hour + 30*time.Minute + 45*time.Second, "PT1H30M45S"},
		{500 * time.Millisecond, "PT0.5S"},
		{1500 * time.Millisecond, "PT1.5S"},
		{time.Microsecond, "PT0.000001S"},
		{time.Nanosecond, "PT0.000000001S"},
		{-time.Minute, "-PT1M"},
	}
	for _, tt := range tests {
		got := durationToISO8601(tt.d)
		assert.Equal(t, tt.want, got, "durationToISO8601(%v)", tt.d)
	}
}
