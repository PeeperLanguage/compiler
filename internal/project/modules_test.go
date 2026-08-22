package project

import (
	"testing"

	"compiler/internal/frontend/ast"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/hir"
	"compiler/internal/ir/mir"
	"compiler/internal/semantics/ownershipresult"
	"compiler/internal/semantics/table"
)

func moduleWithArtifacts() *Module {
	return &Module{
		Phase:                     PhaseBackend,
		SemanticExportFingerprint: "semantic API",
		ModuleScope:               table.New(nil),
		Semantics:                 NewSemanticInfo(),
		TypedASTNodes:             map[ast.NodeID]ast.Node{1: &ast.BadStmt{}},
		HIR:                       &hir.Module{},
		CFG:                       &cfg.Module{Functions: []*cfg.Graph{{}}},
		Ownership:                 ownershipresult.Result{1: &ownershipresult.CleanupPlan{}},
		MIR:                       &mir.Module{},
		LLVMIR:                    "stale IR",
	}
}

func TestModuleResetToPhaseClearsOnlyDownstreamArtifacts(t *testing.T) {
	tests := []struct {
		phase     ModulePhase
		scope     bool
		semantics bool
		exportAPI bool
		astNodes  bool
		hir       bool
		cfg       bool
		ownership bool
		mir       bool
		llvm      bool
	}{
		{phase: PhaseParsed},
		{phase: PhaseTypechecked, scope: true, semantics: true, exportAPI: true, astNodes: true},
		{phase: PhaseCFG, scope: true, semantics: true, exportAPI: true, astNodes: true, cfg: true},
		{phase: PhaseDefiniteInit, scope: true, semantics: true, exportAPI: true, astNodes: true, cfg: true},
		{phase: PhaseOwnership, scope: true, semantics: true, exportAPI: true, astNodes: true, cfg: true, ownership: true},
		{phase: PhaseHIR, scope: true, semantics: true, exportAPI: true, astNodes: true, hir: true, cfg: true, ownership: true},
		{phase: PhaseMIR, scope: true, semantics: true, exportAPI: true, astNodes: true, hir: true, cfg: true, ownership: true, mir: true},
		{phase: PhaseBackend, scope: true, semantics: true, exportAPI: true, astNodes: true, hir: true, cfg: true, ownership: true, mir: true, llvm: true},
	}
	for _, test := range tests {
		module := moduleWithArtifacts()
		module.ResetToPhase(test.phase)
		if module.Phase != test.phase || (module.ModuleScope != nil) != test.scope ||
			(module.Semantics != nil) != test.semantics || (module.HIR != nil) != test.hir ||
			(module.TypedASTNodes != nil) != test.astNodes ||
			(module.SemanticExportFingerprint != "") != test.exportAPI ||
			(module.CFG != nil) != test.cfg ||
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
	module.ResetToPhase(PhaseCFG)
	if module.CFG.Functions[0] != graph {
		t.Fatal("phase reset cloned immutable CFG")
	}
	if module.Ownership != nil {
		t.Fatal("phase reset retained ownership result")
	}
}

func TestModulePhaseString(t *testing.T) {
	for phase, want := range map[ModulePhase]string{
		PhaseNone: "none", PhaseParsed: "parsed", PhaseTypechecked: "typechecked",
		PhaseHIR: "HIR", PhaseCFG: "CFG", PhaseDefiniteInit: "definite-init", PhaseOwnership: "ownership",
		PhaseMIR: "MIR", PhaseBackend: "backend",
	} {
		if got := phase.String(); got != want {
			t.Fatalf("phase %d string = %q, want %q", phase, got, want)
		}
	}
	if got := ModulePhase(255).String(); got != "phase(255)" {
		t.Fatalf("unknown phase string = %q", got)
	}
}
