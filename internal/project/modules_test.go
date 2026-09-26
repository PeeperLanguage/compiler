package project

import (
	"path/filepath"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/mir"
	"compiler/internal/ir/thir"
	"compiler/internal/module"
	"compiler/internal/moduleid"
	"compiler/internal/phase"
	"compiler/internal/semantics/effect"
	"compiler/internal/semantics/flowresult"
	"compiler/internal/semantics/ownershipresult"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typecheckresult"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/semantics/typeresolution"
)

func TestCompilerContextAddModuleCanonicalizesFilePath(t *testing.T) {
	ctx := New(".", ".peep", nil)
	filePath := filepath.Join("nested", "..", "main.peep")
	want := CanonicalPath(filePath)
	id := moduleid.ID{Origin: string(ModuleOriginLocal), ImportPath: "test"}
	module := &module.Module{ID: id, FilePath: filePath}

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
	module := &module.Module{FilePath: "zero.peep"}

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

func TestCompilerContextReportsConflictingFileIdentity(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	ctx := New(".", ".peep", diag)
	const shared = "shared.peep"
	firstID := moduleid.ID{Origin: "stdlib", Namespace: "core", ImportPath: "global"}
	secondID := moduleid.ID{Origin: "stdlib", Namespace: "core", ImportPath: "prelude/global"}
	first := &module.Module{ID: firstID, FilePath: shared}

	if reported := ctx.AddModule(first); reported != nil {
		t.Fatalf("clean registration returned a conflict: %#v", reported)
	}
	conflict := ctx.AddModule(&module.Module{ID: secondID, FilePath: shared})
	if conflict == nil {
		t.Fatal("conflicting registration returned no diagnostic for the caller to label")
	}

	// Two identities for one file is reachable from imports and library-root
	// configuration, so it must diagnose rather than abort the compiler.
	found := false
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == diagnostics.ErrAmbiguousImport {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("conflicting file identity produced no ambiguous-import diagnostic: %s", diag.EmitAllToString())
	}
	if got, ok := ctx.ModuleByFile(first.FilePath); !ok || got != first {
		t.Fatal("first registration was not retained after identity conflict")
	}
	if _, ok := ctx.ModuleByID(secondID); ok {
		t.Fatal("conflicting identity was registered")
	}
}

func TestCompilerContextRejectsIdentityRelocationWithoutCorruptingIndexes(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	ctx := New(".", ".peep", diag)
	idA := moduleid.ID{Origin: "local", ImportPath: "a"}
	idB := moduleid.ID{Origin: "local", ImportPath: "b"}
	first := &module.Module{ID: idA, FilePath: "a.peep"}
	ctx.AddModule(first)
	ctx.AddModule(&module.Module{ID: idB, FilePath: "b.peep"})

	// Moving A onto B's file must be rejected, and rejection must not disturb
	// the indexes A already owns.
	if conflict := ctx.AddModule(&module.Module{ID: idA, FilePath: "b.peep"}); conflict == nil {
		t.Fatal("rejected relocation returned no diagnostic for the caller to label")
	}

	if got, ok := ctx.ModuleByID(idA); !ok || got != first {
		t.Fatal("rejected relocation lost the original module registration")
	}
	if got, ok := ctx.ModuleByFile(first.FilePath); !ok || got != first {
		t.Fatalf("rejected relocation removed the original file index entry: %#v", ctx.Modules())
	}
	found := false
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == diagnostics.ErrAmbiguousImport {
			found = true
		}
	}
	if !found {
		t.Fatalf("relocation conflict produced no diagnostic: %s", diag.EmitAllToString())
	}
}

func TestCompilerContextRejectsSecondFileForSameIdentity(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	ctx := New(".", ".peep", diag)
	id := moduleid.ID{Origin: "local", ImportPath: "foo"}
	first := &module.Module{ID: id, FilePath: "foo.peep"}
	ctx.AddModule(first)

	// Case-differing extensions reduce to one logical identity; the second file
	// must not silently take over the identity.
	ctx.AddModule(&module.Module{ID: id, FilePath: "foo.PEEP"})

	if got, ok := ctx.ModuleByID(id); !ok || got != first {
		t.Fatal("second file for one identity replaced the first registration")
	}
	if _, ok := ctx.ModuleByFile("foo.PEEP"); ok {
		t.Fatal("rejected file was indexed")
	}
}

func TestCompilerContextModuleIDsKeepComponentsCollisionSafe(t *testing.T) {
	ctx := New(".", ".peep", nil)
	firstID := moduleid.ID{Origin: "local", Namespace: "ab", Dependency: "c", ImportPath: "value"}
	secondID := moduleid.ID{Origin: "local", Namespace: "a", Dependency: "bc", ImportPath: "value"}
	first := &module.Module{ID: firstID}
	second := &module.Module{ID: secondID}

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

func moduleWithArtifacts() *module.Module {
	module := &module.Module{
		Phase:                     phase.Backend,
		SemanticExportFingerprint: "semantic API",
		ModuleScope:               symbols.NewScope(nil),
		THIR:                      &thir.Module{},
		CFG:                       &cfg.Module{Functions: []*cfg.ControlFlowGraph{{}}},
		Flow:                      flowresult.New(),
		Effects:                   effect.Result{moduleid.FunctionID("test"): {cfg.SiteID{}: {effect.Use{}}}},
		Ownership:                 ownershipresult.Result{moduleid.FunctionID("test"): &ownershipresult.CleanupPlan{}},
		MIR:                       &mir.Module{},
		LLVMIR:                    "stale IR",
	}
	module.ResetSemanticData()
	module.Typechecking = typecheckresult.New()
	module.Typechecking.RecordExprType(1, typeinfo.DefaultIntegerType())
	module.Flow.RecordExprType(1, &typeinfo.IntegerType{IsSigned: true, Bits: 64})
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
		thir         bool
		cfg          bool
		flow         bool
		effects      bool
		ownership    bool
		mir          bool
		llvm         bool
	}{
		{phase: phase.Parsed},
		{phase: phase.Typechecked, scope: true, bindings: true, constants: true, typechecking: true, exportAPI: true, thir: true},
		{phase: phase.CFG, scope: true, bindings: true, constants: true, typechecking: true, exportAPI: true, thir: true, cfg: true},
		{phase: phase.FlowTyped, scope: true, bindings: true, constants: true, typechecking: true, exportAPI: true, thir: true, cfg: true, flow: true},
		{phase: phase.DefiniteInit, scope: true, bindings: true, constants: true, typechecking: true, exportAPI: true, thir: true, cfg: true, flow: true, effects: true},
		{phase: phase.Ownership, scope: true, bindings: true, constants: true, typechecking: true, exportAPI: true, thir: true, cfg: true, flow: true, effects: true, ownership: true},
		{phase: phase.Usage, scope: true, bindings: true, constants: true, typechecking: true, exportAPI: true, thir: true, cfg: true, flow: true, effects: true, ownership: true},
		{phase: phase.MIR, scope: true, bindings: true, constants: true, typechecking: true, exportAPI: true, thir: true, cfg: true, flow: true, effects: true, ownership: true, mir: true},
		{phase: phase.Backend, scope: true, bindings: true, constants: true, typechecking: true, exportAPI: true, thir: true, cfg: true, flow: true, effects: true, ownership: true, mir: true, llvm: true},
	}
	for _, test := range tests {
		module := moduleWithArtifacts()
		module.ResetToPhase(test.phase)
		if module.Phase != test.phase || (module.ModuleScope != nil) != test.scope ||
			(module.Bindings != nil) != test.bindings || (module.Constants != nil) != test.constants ||
			(module.Typechecking != nil) != test.typechecking ||
			(module.THIR != nil) != test.thir ||
			(module.SemanticExportFingerprint != "") != test.exportAPI ||
			(module.CFG != nil) != test.cfg ||
			(module.Flow != nil) != test.flow ||
			(module.Effects != nil) != test.effects ||
			(module.Ownership != nil) != test.ownership ||
			(module.MIR != nil) != test.mir ||
			(module.LLVMIR != "") != test.llvm {
			t.Fatalf("phase %v reset = %#v", test.phase, module)
		}
	}
}

func TestModuleResetSemanticDataInitializesCurrentResults(t *testing.T) {
	module := &module.Module{Typechecking: typecheckresult.New()}
	module.ResetSemanticData()
	if module.Bindings == nil || module.Bindings.OperationFunctions() == nil || module.Constants == nil || module.Typechecking != nil {
		t.Fatalf("semantic reset = %#v", module)
	}
	if module.Constants.Published(0) != nil {
		t.Fatal("semantic reset retained a published constant")
	}
	if _, ok := module.Constants.Cached(0); ok {
		t.Fatal("semantic reset retained a cached constant")
	}
}

func TestModuleExprTypeEvidenceFollowsPhaseLifecycle(t *testing.T) {
	module := moduleWithArtifacts()
	base := module.BaseExprType(1)
	if base == nil {
		t.Fatal("typechecked module has no base expression type")
	}
	if got := module.EffectiveExprType(1); got != module.Flow.ExprType(1) {
		t.Fatalf("effective type = %#v, want flow refinement", got)
	}

	module.Flow = nil
	if got := module.EffectiveExprType(1); got != base {
		t.Fatalf("effective type without flow = %#v, want base type %#v", got, base)
	}
	module.ResetToPhase(phase.Typechecked)
	if module.BaseExprType(1) != base {
		t.Fatal("typechecked reset discarded base expression type")
	}
	module.ResetToPhase(phase.Parsed)
	if module.BaseExprType(1) != nil || module.EffectiveExprType(1) != nil {
		t.Fatal("parsed reset retained expression type evidence")
	}
}

func TestModuleExprTypeEvidenceHandlesMissingTypecheckResult(t *testing.T) {
	var mod *module.Module
	if mod.BaseExprType(1) != nil || mod.EffectiveExprType(1) != nil {
		t.Fatal("nil module returned expression type evidence")
	}
	mod = &module.Module{}
	if mod.BaseExprType(1) != nil || mod.EffectiveExprType(1) != nil {
		t.Fatal("module without typecheck result returned expression type evidence")
	}
}

func TestModuleResetToPhaseRetainsCFGIdentity(t *testing.T) {
	module := moduleWithArtifacts()
	graph := module.CFG.Functions[0]
	module.ResetToPhase(phase.CFG)
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
	if module.Phase != phase.Parsed || module.CFG != nil || module.MIR != nil {
		t.Fatalf("module artifacts after reset = %#v", module)
	}
}

func TestCompilerContextReindexesCollectedTypeDeclarations(t *testing.T) {
	mod := &module.Module{
		ID:          moduleid.ID{Origin: string(ModuleOriginLocal), ImportPath: "owner"},
		Phase:       phase.Collected,
		ModuleScope: symbols.NewScope(nil),
	}
	base := &typeinfo.DefinedType{
		Name: "Box", Identity: "owner::Box", Kind: typeinfo.DefinedKindStruct,
		TypeParameters: []*typeinfo.TypeParameterType{{Name: "T", OwnerIdentity: "owner::Box", Index: 0}},
	}
	declaration := &ast.StructDecl{Name: &ast.Ident{Name: "Box"}, Type: &ast.StructType{}}
	symbol := symbols.New("Box", symbols.SymbolType, declaration, nil)
	symbol.BindType(base)
	if err := mod.ModuleScope.Declare(symbol); err != nil {
		t.Fatalf("declare retained generic type: %v", err)
	}
	original := New(".", ".peep", nil)
	original.TypeResolver.RegisterTypeDeclaration(mod, declaration, base)

	fresh := New(".", ".peep", nil)
	fresh.AddModule(mod)
	resolved := fresh.TypeResolver.Resolve(fresh.Diagnostics, mod, &ast.AppliedType{
		Name:     &ast.Ident{Name: "Box"},
		TypeArgs: []ast.TypeExpr{&ast.NamedType{Name: "i32"}},
	}, typeresolution.Context{})
	if typeinfo.IsInvalid(resolved) {
		t.Fatalf("retained generic declaration did not reindex: %#v", resolved)
	}

	fresh.ResetModule(mod, phase.Parsed)
	if _, retained := mod.TypeDeclaration(base.Identity); retained {
		t.Fatal("reset below collection retained module declaration artifact")
	}
}

func TestCompilerContextPathlessReplacementClearsFileIndex(t *testing.T) {
	ctx := New(".", ".peep", nil)
	id := moduleid.ID{Origin: string(ModuleOriginLocal), ImportPath: "x"}

	ctx.AddModule(&module.Module{ID: id, FilePath: "x.peep"})
	ctx.AddModule(&module.Module{ID: id})

	if _, found := ctx.ModuleByFile("x.peep"); found {
		t.Fatal("stale file index survived pathless replacement")
	}
	module, found := ctx.ModuleByID(id)
	if !found || module == nil || module.FilePath != "" {
		t.Fatalf("ModuleByID = %#v, want pathless replacement module", module)
	}
}
