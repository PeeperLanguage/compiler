package project

import (
	"testing"

	"compiler/internal/ir/hir"
	"compiler/internal/ir/mir"
	"compiler/internal/semantics/cfg"
	"compiler/internal/semantics/table"
)

func moduleWithArtifacts() *Module {
	return &Module{
		Phase:       PhaseBackend,
		ModuleScope: table.New(nil),
		Semantics:   NewSemanticInfo(),
		HIR:         &hir.Module{},
		CFG:         []*cfg.Graph{{Cleanup: &cfg.CleanupPlan{}}},
		MIR:         &mir.Module{},
		LLVMIR:      "stale IR",
	}
}

func TestModuleResetToPhaseClearsOnlyDownstreamArtifacts(t *testing.T) {
	tests := []struct {
		phase     ModulePhase
		scope     bool
		semantics bool
		hir       bool
		cfg       bool
		cleanup   bool
		mir       bool
		llvm      bool
	}{
		{phase: PhaseParsed},
		{phase: PhaseTypechecked, scope: true, semantics: true},
		{phase: PhaseCFG, scope: true, semantics: true, hir: true, cfg: true},
		{phase: PhaseOwnership, scope: true, semantics: true, hir: true, cfg: true, cleanup: true},
		{phase: PhaseMIR, scope: true, semantics: true, hir: true, cfg: true, cleanup: true, mir: true},
		{phase: PhaseBackend, scope: true, semantics: true, hir: true, cfg: true, cleanup: true, mir: true, llvm: true},
	}
	for _, test := range tests {
		module := moduleWithArtifacts()
		module.ResetToPhase(test.phase)
		if module.Phase != test.phase || (module.ModuleScope != nil) != test.scope ||
			(module.Semantics != nil) != test.semantics || (module.HIR != nil) != test.hir ||
			(module.CFG != nil) != test.cfg || (module.MIR != nil) != test.mir ||
			(module.LLVMIR != "") != test.llvm {
			t.Fatalf("phase %v reset = %#v", test.phase, module)
		}
		cleanup := module.CFG != nil && len(module.CFG) > 0 && module.CFG[0] != nil && module.CFG[0].Cleanup != nil
		if cleanup != test.cleanup {
			t.Fatalf("phase %v cleanup retained=%t, want %t", test.phase, cleanup, test.cleanup)
		}
	}
}

func TestModuleResetToPhaseDoesNotMutateSharedCFG(t *testing.T) {
	original := moduleWithArtifacts()
	cloned := *original
	cloned.ResetToPhase(PhaseCFG)
	if original.CFG[0].Cleanup == nil {
		t.Fatal("reset clone cleared original CFG cleanup")
	}
	if cloned.CFG[0].Cleanup != nil {
		t.Fatal("reset clone retained CFG cleanup")
	}
}
