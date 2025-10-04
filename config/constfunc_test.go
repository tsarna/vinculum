package config

import (
	_ "embed"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

//go:embed testdata/constfunc.vcl
var testdata []byte

//go:embed testdata/constfunc1.vcl
var testdata1 []byte

//go:embed testdata/constfunc2.vcl
var testdata2 []byte

//go:embed testdata/assertfail.vcl
var assertFailure []byte

func TestConstAndFuncs(t *testing.T) {
	logger, err := zap.NewDevelopment()
	assert.NoError(t, err)

	_, diags := NewConfig().WithSources(testdata).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}

}

func TestConstAndFuncsSplit(t *testing.T) {
	logger, err := zap.NewDevelopment()
	assert.NoError(t, err)

	_, diags := NewConfig().WithSources(testdata1, testdata2).WithLogger(logger).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}
}

func TestAssertFailure(t *testing.T) {
	logger, err := zap.NewDevelopment()
	assert.NoError(t, err)

	_, diags := NewConfig().WithSources(assertFailure).WithLogger(logger).Build()
	if !diags.HasErrors() {
		t.Fatal("expected errors, didn't get any")
	}
}
