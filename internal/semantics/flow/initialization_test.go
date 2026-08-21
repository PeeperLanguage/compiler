package flow

import (
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/semantics/cfg"
	"compiler/internal/semantics/symbols"
)

const (
	valueSymbol symbols.SymbolID = 1
	flagSymbol  symbols.SymbolID = 2
)

func analyzeInitializationBody(t *testing.T, build func(i32, boolType ir.TypeID) []hir.Stmt) (Result, *diagnostics.DiagnosticBag) {
	t.Helper()
	types := ir.NewTypeTable()
	i32 := types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 32})
	boolType := types.Intern(ir.Type{Kind: ir.TypeBool})
	fn := &hir.Function{
		Name:       "choose",
		NodeID:     100,
		Params:     []ir.Param{{Name: "flag", Type: boolType, SymbolID: flagSymbol}},
		ReturnType: i32,
		Body:       &hir.Block{NodeID: 101, Stmts: build(i32, boolType)},
	}
	module := &hir.Module{Types: types, Funcs: []*hir.Function{fn}}
	diag := diagnostics.NewDiagnosticBag()
	return Analyze(module, cfg.BuildModule(module), diag), diag
}

func uninitializedValueBinding(i32 ir.TypeID) *hir.Binding {
	return &hir.Binding{Name: "value", Type: i32, SymbolID: valueSymbol, NodeID: 1}
}

func valueAssignment(i32 ir.TypeID, value string) *hir.Assign {
	return &hir.Assign{
		Target: &ir.Place{Root: &ir.Ident{Name: "value", Type: i32, SymbolID: valueSymbol}, Type: i32},
		Value:  &ir.IntLit{Value: value, Type: i32},
	}
}

func valueReturn(i32 ir.TypeID) *hir.Return {
	return &hir.Return{Value: &ir.Ident{Name: "value", Type: i32, SymbolID: valueSymbol}}
}

func TestInitializationIgnoresTerminatingBranchAtJoin(t *testing.T) {
	result, diag := analyzeInitializationBody(t, func(i32, boolType ir.TypeID) []hir.Stmt {
		return []hir.Stmt{
			uninitializedValueBinding(i32),
			&hir.If{
				Cond: &ir.Ident{Name: "flag", Type: boolType, SymbolID: flagSymbol},
				Then: &hir.Block{Stmts: []hir.Stmt{valueAssignment(i32, "7")}},
				Else: &hir.Block{Stmts: []hir.Stmt{&hir.Return{Value: &ir.IntLit{Value: "3", Type: i32}}}},
			},
			valueReturn(i32),
		}
	})
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	if function := result[100]; function == nil || len(function.In) == 0 || len(function.Out) == 0 {
		t.Fatalf("initialization result = %#v, want per-site states", result)
	}
}

func TestInitializationRejectsContinuingUninitializedBranch(t *testing.T) {
	_, diag := analyzeInitializationBody(t, func(i32, boolType ir.TypeID) []hir.Stmt {
		binding := uninitializedValueBinding(i32)
		binding.Name = "value$28993"
		return []hir.Stmt{
			binding,
			&hir.If{Cond: &ir.Ident{Name: "flag", Type: boolType, SymbolID: flagSymbol}, Then: &hir.Block{Stmts: []hir.Stmt{valueAssignment(i32, "7")}}},
			&hir.Return{Value: &ir.Ident{Name: "value$28993", Type: i32, SymbolID: valueSymbol}},
		}
	})
	if !hasDiagnosticCode(diag, diagnostics.ErrUninitializedVariable) {
		t.Fatalf("expected uninitialized diagnostic:\n%s", diag.EmitAllToString())
	}
	if got := diag.EmitAllToString(); !strings.Contains(got, "symbol `value` used before it's initialized") || strings.Contains(got, "value$28993") {
		t.Fatalf("diagnostic leaked lowered symbol name:\n%s", got)
	}
}

func TestInitializationAcceptsAssignmentOnBothBranches(t *testing.T) {
	_, diag := analyzeInitializationBody(t, func(i32, boolType ir.TypeID) []hir.Stmt {
		return []hir.Stmt{
			uninitializedValueBinding(i32),
			&hir.If{
				Cond: &ir.Ident{Name: "flag", Type: boolType, SymbolID: flagSymbol},
				Then: &hir.Block{Stmts: []hir.Stmt{valueAssignment(i32, "7")}},
				Else: &hir.Block{Stmts: []hir.Stmt{valueAssignment(i32, "3")}},
			},
			valueReturn(i32),
		}
	})
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestInitializationLoopMayExecuteZeroTimes(t *testing.T) {
	_, diag := analyzeInitializationBody(t, func(i32, boolType ir.TypeID) []hir.Stmt {
		return []hir.Stmt{
			uninitializedValueBinding(i32),
			&hir.For{Cond: &ir.Ident{Name: "flag", Type: boolType, SymbolID: flagSymbol}, Body: &hir.Block{Stmts: []hir.Stmt{valueAssignment(i32, "7")}}},
			valueReturn(i32),
		}
	})
	if !hasDiagnosticCode(diag, diagnostics.ErrUninitializedVariable) {
		t.Fatalf("expected uninitialized diagnostic:\n%s", diag.EmitAllToString())
	}
}

func TestInitializationAcceptsDirectAssignment(t *testing.T) {
	_, diag := analyzeInitializationBody(t, func(i32, _ ir.TypeID) []hir.Stmt {
		return []hir.Stmt{uninitializedValueBinding(i32), valueAssignment(i32, "7"), valueReturn(i32)}
	})
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func hasDiagnosticCode(diag *diagnostics.DiagnosticBag, code string) bool {
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == code {
			return true
		}
	}
	return false
}
