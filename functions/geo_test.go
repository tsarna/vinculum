package functions

import (
	_ "embed"
	"testing"

	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

//go:embed testdata/geo_funcs.vcl
var geoFuncsVCL []byte

func TestGeoFunctions(t *testing.T) {
	logger, err := zap.NewDevelopment()
	if err != nil {
		t.Fatal(err)
	}

	_, diags := config.NewConfig().WithSources(geoFuncsVCL).WithLogger(logger).Build()
	if diags.HasErrors() {
		for _, d := range diags {
			t.Logf("%s: %s", d.Summary, d.Detail)
		}
		t.Fatal("geo functions VCL assertions failed")
	}
}
