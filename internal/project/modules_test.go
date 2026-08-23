package project

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/hir"
	"compiler/internal/ir/mir"
	"compiler/internal/phase"
	"compiler/internal/semantics/flowresult"
	"compiler/internal/semantics/ownershipresult"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

func moduleWithArtifacts() *Module {
	return &Module{
		Phase:                     phase.Backend,
		SemanticExportFingerprint: "semantic API",
		ModuleScope:               symbols.NewScope(nil),
		Semantics:                 NewSemanticInfo(),
		TypedASTNodes:             map[ast.NodeID]ast.Node{1: &ast.BadStmt{}},
		HIR:                       &hir.Module{},
		CFG:                       &cfg.Module{Functions: []*cfg.Graph{{}}},
		Flow:                      &flowresult.Result{ExprTypes: map[ast.NodeID]typeinfo.Type{1: typeinfo.DefaultIntegerType()}},
		Ownership:                 ownershipresult.Result{1: &ownershipresult.CleanupPlan{}},
		MIR:                       &mir.Module{},
		LLVMIR:                    "stale IR",
	}
}

func TestModuleResetToPhaseClearsOnlyDownstreamArtifacts(t *testing.T) {
	tests := []struct {
		phase     phase.Phase
		scope     bool
		semantics bool
		exportAPI bool
		astNodes  bool
		hir       bool
		cfg       bool
		flow      bool
		ownership bool
		mir       bool
		llvm      bool
	}{
		{phase: phase.Parsed},
		{phase: phase.Typechecked, scope: true, semantics: true, exportAPI: true, astNodes: true},
		{phase: phase.CFG, scope: true, semantics: true, exportAPI: true, astNodes: true, cfg: true},
		{phase: phase.FlowTyped, scope: true, semantics: true, exportAPI: true, astNodes: true, cfg: true, flow: true},
		{phase: phase.DefiniteInit, scope: true, semantics: true, exportAPI: true, astNodes: true, cfg: true, flow: true},
		{phase: phase.Ownership, scope: true, semantics: true, exportAPI: true, astNodes: true, cfg: true, flow: true, ownership: true},
		{phase: phase.Usage, scope: true, semantics: true, exportAPI: true, astNodes: true, cfg: true, flow: true, ownership: true},
		{phase: phase.HIR, scope: true, semantics: true, exportAPI: true, astNodes: true, hir: true, cfg: true, flow: true, ownership: true},
		{phase: phase.MIR, scope: true, semantics: true, exportAPI: true, astNodes: true, hir: true, cfg: true, flow: true, ownership: true, mir: true},
		{phase: phase.Backend, scope: true, semantics: true, exportAPI: true, astNodes: true, hir: true, cfg: true, flow: true, ownership: true, mir: true, llvm: true},
	}
	for _, test := range tests {
		module := moduleWithArtifacts()
		module.resetToPhase(test.phase)
		if module.Phase != test.phase || (module.ModuleScope != nil) != test.scope ||
			(module.Semantics != nil) != test.semantics || (module.HIR != nil) != test.hir ||
			(module.TypedASTNodes != nil) != test.astNodes ||
			(module.SemanticExportFingerprint != "") != test.exportAPI ||
			(module.CFG != nil) != test.cfg ||
			(module.Flow != nil) != test.flow ||
			(module.Ownership != nil) != test.ownership ||
			(module.MIR != nil) != test.mir ||
			(module.LLVMIR != "") != test.llvm {
			t.Fatalf("phase %v reset = %#v", test.phase, module)
		}
	}
}

func TestModuleResetToPhaseRetainsCFGIdentity(t *testing.T) {
	module := moduleWithArtifacts()
	graph := module.CFG.Functions[0]
	module.resetToPhase(phase.CFG)
	if module.CFG.Functions[0] != graph {
		t.Fatal("phase reset cloned immutable CFG")
	}
	if module.Ownership != nil {
		t.Fatal("phase reset retained ownership result")
	}
}

func TestCompilerContextResetModuleDiscardsOnlyDownstreamDiagnostics(t *testing.T) {
	bag := diagnostics.NewDiagnosticBag()
	bag.BeginPhase(phase.Parsed, "a").Add(diagnostics.NewWarning("a parse"))
	bag.BeginPhase(phase.Typechecked, "a").Add(diagnostics.NewError("a type"))
	bag.BeginPhase(phase.Typechecked, "b").Add(diagnostics.NewError("b type"))
	ctx := New(".", ".peep", bag)
	module := moduleWithArtifacts()
	module.Key = "a"

	ctx.ResetModule(module, phase.Parsed)

	got := bag.Diagnostics()
	if len(got) != 2 || got[0].Message != "a parse" || got[1].Message != "b type" {
		t.Fatalf("diagnostics after context reset = %#v", got)
	}
	if module.Phase != phase.Parsed || module.CFG != nil || module.HIR != nil {
		t.Fatalf("module artifacts after reset = %#v", module)
	}
}

func TestCompilerContextResetPurgesOwnedNamedTypeInstances(t *testing.T) {
	ctx := New(".", ".peep", nil)
	module := &Module{Key: "owner"}
	ctx.typeInstances["owner::Box<i32>"] = namedTypeInstance{
		ownerModuleKey: module.Key,
		typ:            &typeinfo.DefinedType{Name: "Box", Identity: "owner::Box<i32>"},
	}
	ctx.typeInstances["other::Box<i32>"] = namedTypeInstance{
		ownerModuleKey: "other",
		typ:            &typeinfo.DefinedType{Name: "Box", Identity: "other::Box<i32>"},
	}

	ctx.ResetModule(module, phase.Parsed)

	if _, found := ctx.typeInstances["owner::Box<i32>"]; found {
		t.Fatal("reset retained instance owned by reset module")
	}
	if _, found := ctx.typeInstances["other::Box<i32>"]; !found {
		t.Fatal("reset removed instance owned by another module")
	}
}
