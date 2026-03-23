package config

import (
	_ "embed"
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

//go:embed testdata/sys.vcl
var systest []byte

func TestSys(t *testing.T) {
	logger, err := zap.NewDevelopment()
	assert.NoError(t, err)

	_, diags := NewConfig().WithSources(systest).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatalf("failed to build config: %v", diags)
	}
}

func TestGetSysObject(t *testing.T) {
	val := GetSysObject("")
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
		"filepath":  cty.String,
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

	// filepath empty when not set
	assert.Equal(t, "", attrs["filepath"].AsString())

	// filepath reflects baseDir when set
	val2 := GetSysObject("/tmp/myfiles")
	assert.Equal(t, "/tmp/myfiles", val2.AsValueMap()["filepath"].AsString())
}
