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

//go:embed testdata/procedure_while.vcl
var procedureWhileVCL []byte

//go:embed testdata/procedure_break_outside.vcl
var procedureBreakOutsideVCL []byte

//go:embed testdata/procedure_continue_outside.vcl
var procedureContinueOutsideVCL []byte

//go:embed testdata/procedure_range.vcl
var procedureRangeVCL []byte

//go:embed testdata/procedure_switch.vcl
var procedureSwitchVCL []byte

//go:embed testdata/procedure_switch_assign.vcl
var procedureSwitchAssignVCL []byte

//go:embed testdata/procedure_switch_double_default.vcl
var procedureSwitchDoubleDefaultVCL []byte

//go:embed testdata/procedure_switch_unreachable.vcl
var procedureSwitchUnreachableVCL []byte

//go:embed testdata/procedure_edge_cases.vcl
var procedureEdgeCasesVCL []byte

//go:embed testdata/procedure_variadic_overlap.vcl
var procedureVariadicOverlapVCL []byte

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

func TestProcedureWhile(t *testing.T) {
	_, diags := NewConfig().WithSources(procedureWhileVCL).WithLogger(procTestLogger(t)).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}
}

func TestProcedureBreakOutsideLoop(t *testing.T) {
	_, diags := NewConfig().WithSources(procedureBreakOutsideVCL).WithLogger(procTestLogger(t)).Build()
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for break outside loop, got none")
	}
}

func TestProcedureContinueOutsideLoop(t *testing.T) {
	_, diags := NewConfig().WithSources(procedureContinueOutsideVCL).WithLogger(procTestLogger(t)).Build()
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for continue outside loop, got none")
	}
}

func TestProcedureRange(t *testing.T) {
	_, diags := NewConfig().WithSources(procedureRangeVCL).WithLogger(procTestLogger(t)).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}
}

func TestProcedureSwitch(t *testing.T) {
	_, diags := NewConfig().WithSources(procedureSwitchVCL).WithLogger(procTestLogger(t)).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}
}

func TestProcedureSwitchAssignError(t *testing.T) {
	_, diags := NewConfig().WithSources(procedureSwitchAssignVCL).WithLogger(procTestLogger(t)).Build()
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for assignment in switch body, got none")
	}
}

func TestProcedureSwitchDoubleDefault(t *testing.T) {
	_, diags := NewConfig().WithSources(procedureSwitchDoubleDefaultVCL).WithLogger(procTestLogger(t)).Build()
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for duplicate default, got none")
	}
}

func TestProcedureSwitchUnreachable(t *testing.T) {
	_, diags := NewConfig().WithSources(procedureSwitchUnreachableVCL).WithLogger(procTestLogger(t)).Build()
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for unreachable code after switch, got none")
	}
}

func TestProcedureEdgeCases(t *testing.T) {
	_, diags := NewConfig().WithSources(procedureEdgeCasesVCL).WithLogger(procTestLogger(t)).Build()
	if diags.HasErrors() {
		t.Fatal(diags)
	}
}

func TestProcedureVariadicOverlap(t *testing.T) {
	_, diags := NewConfig().WithSources(procedureVariadicOverlapVCL).WithLogger(procTestLogger(t)).Build()
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for variadic param overlapping regular param, got none")
	}
}
