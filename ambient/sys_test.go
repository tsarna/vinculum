package ambient_test

import (
	_ "embed"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/tsarna/vinculum/ambient"
	"github.com/tsarna/vinculum/config"
	timecty "github.com/tsarna/time-cty-funcs"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

//go:embed testdata/sys.vcl
var systest []byte

func TestSys(t *testing.T) {
	logger, err := zap.NewDevelopment()
	assert.NoError(t, err)

	_, diags := config.NewConfig().WithSources(systest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatalf("failed to build config: %v", diags)
	}
}

func TestGetSysObject(t *testing.T) {
	val := ambient.GetSysObject("", "")
	assert.Equal(t, cty.Object(map[string]cty.Type{
		"pid":        cty.Number,
		"hostname":   cty.String,
		"user":       cty.String,
		"uid":        cty.Number,
		"group":      cty.String,
		"gid":        cty.Number,
		"os":         cty.String,
		"arch":       cty.String,
		"cpus":       cty.Number,
		"executable": cty.String,
		"cwd":        cty.String,
		"homedir":    cty.String,
		"tempdir":    cty.String,
		"filepath":   cty.String,
		"writepath":  cty.String,
		"starttime":  timecty.TimeCapsuleType,
		"boottime":   timecty.TimeCapsuleType,
	}), val.Type())

	attrs := val.AsValueMap()

	// pid should match current process
	pidVal, _ := attrs["pid"].AsBigFloat().Int64()
	assert.Equal(t, int64(os.Getpid()), pidVal)

	// hostname should match
	hostname, _ := os.Hostname()
	assert.Equal(t, hostname, attrs["hostname"].AsString())

	// platform values should match runtime constants
	assert.Equal(t, runtime.GOOS, attrs["os"].AsString())
	assert.Equal(t, runtime.GOARCH, attrs["arch"].AsString())

	// cpus should be positive
	cpus, _ := attrs["cpus"].AsBigFloat().Int64()
	assert.Greater(t, cpus, int64(0))

	// tempdir should match
	assert.Equal(t, os.TempDir(), attrs["tempdir"].AsString())

	// filepath and writepath empty when not set
	assert.Equal(t, "", attrs["filepath"].AsString())
	assert.Equal(t, "", attrs["writepath"].AsString())

	// filepath and writepath reflect values when set
	val2 := ambient.GetSysObject("/tmp/myfiles", "/tmp/myfiles/out")
	attrs2 := val2.AsValueMap()
	assert.Equal(t, "/tmp/myfiles", attrs2["filepath"].AsString())
	assert.Equal(t, "/tmp/myfiles/out", attrs2["writepath"].AsString())

	// starttime should be in the past and stable across calls
	before := time.Now()
	st, err := timecty.GetTime(attrs["starttime"])
	assert.NoError(t, err)
	assert.True(t, !st.After(before), "starttime should not be in the future")
	st2, _ := timecty.GetTime(ambient.GetSysObject("", "").AsValueMap()["starttime"])
	assert.True(t, st.Equal(st2), "starttime should be stable across GetSysObject calls")

	// boottime should be at or before starttime
	bt, err := timecty.GetTime(attrs["boottime"])
	assert.NoError(t, err)
	assert.True(t, !bt.After(st), "boottime should not be after starttime")
}
