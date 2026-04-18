package functions

import (
	_ "embed"
	"testing"

	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

//go:embed testdata/barcode_funcs.vcl
var barcodeFuncsVCL []byte

func TestBarcodeFunctions(t *testing.T) {
	logger, err := zap.NewDevelopment()
	if err != nil {
		t.Fatal(err)
	}

	_, diags := config.NewConfig().WithSources(barcodeFuncsVCL).WithLogger(logger).Build()
	if diags.HasErrors() {
		for _, d := range diags {
			t.Logf("%s: %s", d.Summary, d.Detail)
		}
		t.Fatal("barcode functions VCL assertions failed")
	}
}
