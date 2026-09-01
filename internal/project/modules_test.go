package project

import (
	"path/filepath"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/hir"
	"compiler/internal/ir/mir"
	"compiler/internal/moduleid"
	"compiler/internal/phase"
	"compiler/internal/semantics/flowresult"
	"compiler/internal/semantics/ownershipresult"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typecheckresult"
	"compiler/internal/semantics/typeinfo"
)

func TestCompilerContextAddModuleCanonicalizesFilePath(t *testing.T) {
	ctx := New(".", ".peep", nil)
	filePath := filepath.Join("nested", "..", "main.peep")
	want := CanonicalPath(filePath)
	id := moduleid.ID{Origin: string(ModuleOriginLocal), ImportPath: "test"}
	module := &Module{ID: id, FilePath: filePath}

	ctx.AddModule(module)

	if module.FilePath != want {
		t.Fatalf("module path = %q, want canonical %q", module.FilePath, want)
	}
	byID, foundByID := ctx.ModuleByID(id)
	byFile, foundByFile := ctx.ModuleByFile(filePath)
	if !foundByID || !foundByFile || byID != module || byFile != module {
		t.Fatalf("module lookups by ID/file = (%p, %t), (%p, %t), want %p", byID, foundByID, byFile, foundByFile, module)
	}
}

func TestCompilerContextRejectsZeroModuleID(t *testing.T) {
	ctx := New(".", ".peep", nil)
	module := &Module{FilePath: "zero.peep"}

	ctx.AddModule(module)

	if len(ctx.Modules()) != 0 {
		t.Fatalf("modules after zero-ID add = %#v", ctx.Modules())
	}
	if _, found := ctx.ModuleByID(moduleid.ID{}); found {
		t.Fatal("zero ID resolved a module")
	}
	if _, found := ctx.ModuleByFile(module.FilePath); found {
		t.Fatal("rejected zero-ID module remained in file index")
	}
}

func TestCompilerContextModuleIDsKeepComponentsCollisionSafe(t *testing.T) {
	ctx := New(".", ".peep", nil)
	firstID := moduleid.ID{Origin: "local", Namespace: "ab", Dependency: "c", ImportPath: "value"}
	secondID := moduleid.ID{Origin: "local", Namespace: "a", Dependency: "bc", ImportPath: "value"}
	first := &Module{ID: firstID}
	second := &Module{ID: secondID}

	ctx.AddModule(first)
	ctx.AddModule(second)

	if firstID.String() == secondID.String() {
		t.Fatalf("component-distinct IDs collide: %q", firstID.String())
	}
	if got, found := ctx.ModuleByID(firstID); !found || got != first {
		t.Fatalf("first module lookup = (%p, %t), want %p", got, found, first)
	}
	if got, found := ctx.ModuleByID(secondID); !found || got != second {
		t.Fatalf("second module lookup = (%p, %t), want %p", got, found, second)
	}
}

func moduleWithArtifacts() *Module {
	module := &Module{
		Phase:                     phase.Backend,
		SemanticExportFingerprint: "semantic API",
		ModuleScope:               symbols.NewScope(nil),
		TypedASTNodes:             map[ast.NodeID]ast.Node{1: &ast.BadStmt{}},
		HIR:                       &hir.Module{},
		CFG:                       &cfg.Module{Functions: []*cfg.Graph{{}}},
		Flow:                      &flowresult.Result{ExprTypes: map[ast.NodeID]typeinfo.Type{1: typeinfo.DefaultIntegerType()}},
		Ownership:                 ownershipresult.Result{1: &ownershipresult.CleanupPlan{}},
		MIR:                       &mir.Module{},
		LLVMIR:                    "stale IR",
	}
	module.ResetSemanticData()
	module.Typechecking = typecheckresult.New()
	module.Typechecking.ExprTypes[1] = typeinfo.DefaultIntegerType()
	return module
}

func TestModuleResetToPhaseClearsOnlyDownstreamArtifacts(t *testing.T) {
	tests := []struct {
		phase        phase.Phase
		scope        bool
		bindings     bool
		constants    bool
		typechecking bool
		exportAPI    bool
		astNodes     bool
		hir          bool
		cfg          bool
		flow         bool
		ownership    bool
		mir          bool
		llvm         bool
	}{
		{phase: phase.Parsed},
		{phase: phase.Typechecked, scope: true, bindings: true, constants: true, typechecking: true, exportAPI: true, astNodes: true},
		{phase: phase.CFG, scope: true, bindings: true, constants: true, typechecking: true, exportAPI: true, astNodes: true, cfg: true},
		{phase: phase.FlowTyped, scope: true, bindings: true, constants: true, typechecking: true, exportAPI: true, astNodes: true, cfg: true, flow: true},
		{phase: phase.DefiniteInit, scope: true, bindings: true, constants: true, typechecking: true, exportAPI: true, astNodes: true, cfg: true, flow: true},
		{phase: phase.Ownership, scope: true, bindings: true, constants: true, typechecking: true, exportAPI: true, astNodes: true, cfg: true, flow: true, ownership: true},
		{phase: phase.Usage, scope: true, bindings: true, constants: true, typechecking: true, exportAPI: true, astNodes: true, cfg: true, flow: true, ownership: true},
		{phase: phase.HIR, scope: true, bindings: true, constants: true, typechecking: true, exportAPI: true, astNodes: true, hir: true, cfg: true, flow: true, ownership: true},
		{phase: phase.MIR, scope: true, bindings: true, constants: true, typechecking: true, exportAPI: true, astNodes: true, hir: true, cfg: true, flow: true, ownership: true, mir: true},
		{phase: phase.Backend, scope: true, bindings: true, constants: true, typechecking: true, exportAPI: true, astNodes: true, hir: true, cfg: true, flow: true, ownership: true, mir: true, llvm: true},
	}
	for _, test := range tests {
		module := moduleWithArtifacts()
		module.resetToPhase(test.phase)
		if module.Phase != test.phase || (module.ModuleScope != nil) != test.scope ||
			(module.Bindings != nil) != test.bindings || (module.Constants != nil) != test.constants ||
			(module.Typechecking != nil) != test.typechecking ||
			(module.HIR != nil) != test.hir ||
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

func TestModuleResetSemanticDataInitializesCurrentResults(t *testing.T) {
	module := &Module{Typechecking: typecheckresult.New()}
	module.ResetSemanticData()
	if module.Bindings == nil || module.Bindings.BlockScopes == nil || module.Bindings.NodeSymbols == nil ||
		module.Bindings.MethodsByReceiver == nil || module.Bindings.MethodsByDecl == nil ||
		module.Bindings.OperationFunctions == nil || module.Constants == nil || module.Constants.ModuleValues == nil ||
		module.Constants.QueryCache == nil || module.Typechecking != nil {
		t.Fatalf("semantic reset = %#v", module)
	}
}

func TestModuleExprTypeEvidenceFollowsPhaseLifecycle(t *testing.T) {
	module := moduleWithArtifacts()
	base := module.BaseExprType(1)
	if base == nil {
		t.Fatal("typechecked module has no base expression type")
	}
	if got := module.EffectiveExprType(1); got != module.Flow.ExprTypes[1] {
		t.Fatalf("effective type = %#v, want flow refinement", got)
	}

	module.Flow = nil
	if got := module.EffectiveExprType(1); got != base {
		t.Fatalf("effective type without flow = %#v, want base type %#v", got, base)
	}
	module.resetToPhase(phase.Typechecked)
	if module.BaseExprType(1) != base {
		t.Fatal("typechecked reset discarded base expression type")
	}
	module.resetToPhase(phase.Parsed)
	if module.BaseExprType(1) != nil || module.EffectiveExprType(1) != nil {
		t.Fatal("parsed reset retained expression type evidence")
	}
}

func TestModuleExprTypeEvidenceHandlesMissingTypecheckResult(t *testing.T) {
	var module *Module
	if module.BaseExprType(1) != nil || module.EffectiveExprType(1) != nil {
		t.Fatal("nil module returned expression type evidence")
	}
	module = &Module{}
	if module.BaseExprType(1) != nil || module.EffectiveExprType(1) != nil {
		t.Fatal("module without typecheck result returned expression type evidence")
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
	aID := moduleid.ID{Origin: string(ModuleOriginLocal), ImportPath: "a"}
	bID := moduleid.ID{Origin: string(ModuleOriginLocal), ImportPath: "b"}
	bag.BeginPhase(phase.Parsed, aID.String()).Add(diagnostics.NewWarning("a parse"))
	bag.BeginPhase(phase.Typechecked, aID.String()).Add(diagnostics.NewError("a type"))
	bag.BeginPhase(phase.Typechecked, bID.String()).Add(diagnostics.NewError("b type"))
	ctx := New(".", ".peep", bag)
	module := moduleWithArtifacts()
	module.ID = aID

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
	ownerID := moduleid.ID{Origin: string(ModuleOriginLocal), ImportPath: "owner"}
	otherID := moduleid.ID{Origin: string(ModuleOriginLocal), ImportPath: "other"}
	module := &Module{ID: ownerID}
	ctx.typeInstances["owner::Box<i32>"] = namedTypeInstance{
		ownerModuleID: ownerID,
		typ:           &typeinfo.DefinedType{Name: "Box", Identity: "owner::Box<i32>"},
	}
	ctx.typeInstances["other::Box<i32>"] = namedTypeInstance{
		ownerModuleID: otherID,
		typ:           &typeinfo.DefinedType{Name: "Box", Identity: "other::Box<i32>"},
	}

	ctx.ResetModule(&Module{ID: module.ID}, phase.Parsed)

	if _, found := ctx.typeInstances["owner::Box<i32>"]; found {
		t.Fatal("reset retained instance owned by reset module")
	}
	if _, found := ctx.typeInstances["other::Box<i32>"]; !found {
		t.Fatal("reset removed instance owned by another module")
	}
}

func TestCompilerContextReindexesCollectedTypeDeclarations(t *testing.T) {
	module := &Module{
		ID:    moduleid.ID{Origin: string(ModuleOriginLocal), ImportPath: "owner"},
		Phase: phase.Collected,
	}
	base := &typeinfo.DefinedType{Name: "Box", Identity: "owner::Box", Kind: typeinfo.DefinedKindStruct}
	declaration := &ast.StructDecl{Name: &ast.Ident{Name: "Box"}}
	original := New(".", ".peep", nil)
	original.RegisterTypeDeclaration(module, declaration, base)

	fresh := New(".", ".peep", nil)
	fresh.AddModule(module)
	registeredModule, found := fresh.typeDeclarations[base.Identity]
	registered := module.namedTypeDeclarations[base.Identity]
	if !found || registeredModule != module || registered.base != base || registered.syntax != declaration {
		t.Fatalf("reindexed declaration module = %#v, artifact = %#v", registeredModule, registered)
	}

	fresh.ResetModule(module, phase.Parsed)
	if module.namedTypeDeclarations != nil {
		t.Fatal("reset below collection retained module declaration artifact")
	}
	if _, found := fresh.typeDeclarations[base.Identity]; found {
		t.Fatal("reset below collection retained context declaration index")
	}
}
