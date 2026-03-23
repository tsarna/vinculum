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
