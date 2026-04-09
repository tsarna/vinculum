package config

import (
	_ "embed"
	"testing"

	"go.uber.org/zap"
)

func procTestLogger(t *testing.T) *zap.Logger {
	t.Helper()
	logger, err := zap.NewDevelopment()
	if err != nil {
		t.Fatal(err)
	}
	return logger
}

//go:embed testdata/procedure_basic.vcl
var procedureBasicVCL []byte

//go:embed testdata/procedure_discard.vcl
var procedureDiscardVCL []byte

//go:embed testdata/procedure_variadic.vcl
var procedureVariadicVCL []byte

//go:embed testdata/procedure_errors.vcl
var procedureErrorsVCL []byte

//go:embed testdata/procedure_dup.vcl
var procedureDupVCL []byte

//go:embed testdata/procedure_if.vcl
var procedureIfVCL []byte

//go:embed testdata/procedure_if_errors.vcl
var procedureIfErrorsVCL []byte

//go:embed testdata/procedure_orphan_else.vcl
var procedureOrphanElseVCL []byte

//go:embed testdata/procedure_double_else.vcl
var procedureDoubleElseVCL []byte

//go:embed testdata/procedure_if_unreachable.vcl
var procedureIfUnreachableVCL []byte

func TestProcedureBasic(t *testing.T) {
	_, diags := NewConfig().WithSources(procedureBasicVCL).WithLogger(procTestLogger(t)).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}
}

func TestProcedureDiscard(t *testing.T) {
	_, diags := NewConfig().WithSources(procedureDiscardVCL).WithLogger(procTestLogger(t)).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}
}

func TestProcedureVariadic(t *testing.T) {
	_, diags := NewConfig().WithSources(procedureVariadicVCL).WithLogger(procTestLogger(t)).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}
}

func TestProcedureUnreachable(t *testing.T) {
	_, diags := NewConfig().WithSources(procedureErrorsVCL).WithLogger(procTestLogger(t)).Build()
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for unreachable code, got none")
	}
}

func TestProcedureDuplicate(t *testing.T) {
	_, diags := NewConfig().WithSources(procedureDupVCL).WithLogger(procTestLogger(t)).Build()
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for duplicate procedure, got none")
	}
}

func TestProcedureIf(t *testing.T) {
	_, diags := NewConfig().WithSources(procedureIfVCL).WithLogger(procTestLogger(t)).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}
}

func TestProcedureOrphanElif(t *testing.T) {
	_, diags := NewConfig().WithSources(procedureIfErrorsVCL).WithLogger(procTestLogger(t)).Build()
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for orphan elif, got none")
	}
}

func TestProcedureOrphanElse(t *testing.T) {
	_, diags := NewConfig().WithSources(procedureOrphanElseVCL).WithLogger(procTestLogger(t)).Build()
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for orphan else, got none")
	}
}

func TestProcedureDoubleElse(t *testing.T) {
	_, diags := NewConfig().WithSources(procedureDoubleElseVCL).WithLogger(procTestLogger(t)).Build()
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for double else, got none")
	}
}

func TestProcedureIfUnreachable(t *testing.T) {
	_, diags := NewConfig().WithSources(procedureIfUnreachableVCL).WithLogger(procTestLogger(t)).Build()
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for unreachable code after all-branch return, got none")
	}
}
