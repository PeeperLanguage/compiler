package project

import (
	"compiler/internal/constvalue"
	"compiler/internal/source"
	"path/filepath"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/mir"
	"compiler/internal/ir/thir"
	"compiler/internal/module"
	"compiler/internal/moduleid"
	"compiler/internal/phase"
	"compiler/internal/semantics/analysis"
	"compiler/internal/semantics/symbols"
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
	integer := typeinfo.DefaultIntegerType()
	functionID := moduleid.FunctionID("test::main")
	typed := thir.NewModule("test", "test.peep", []*thir.Function{{
		Identity: functionID,
		Name:     "main",
		Source:   ir.SourceInfo{NodeID: source.ParsedNodeID(2)},
		Body: &thir.Block{
			StmtInfo: thir.StmtInfo{Source: ir.SourceInfo{NodeID: source.ParsedNodeID(3)}},
			Stmts: []thir.Stmt{&thir.ExprStmt{
				StmtInfo: thir.StmtInfo{Source: ir.SourceInfo{NodeID: source.ParsedNodeID(4)}},
				Value:    &thir.NumberLiteral{ExprInfo: thir.ExprInfo{Source: ir.SourceInfo{NodeID: source.ParsedNodeID(1)}, Type: integer}, Value: "1"},
			}},
		},
	}})
	module := &module.Module{
		Phase:                     phase.Backend,
		SemanticExportFingerprint: "semantic API",
		ModuleScope:               symbols.NewScope(nil),
		THIR:                      typed,
		CFG:                       &cfg.Module{Functions: []*cfg.ControlFlowGraph{{FunctionID: functionID}}},
		Analysis:                  &analysis.Module{},
		MIR:                       &mir.Module{},
		LLVMIR:                    "stale IR",
	}
	module.ResetSemanticData()
	return module
}

func TestModuleResetToPhaseClearsOnlyDownstreamArtifacts(t *testing.T) {
	tests := []struct {
		phase     phase.Phase
		scope     bool
		bindings  bool
		exportAPI bool
		thir      bool
		cfg       bool
		analysis  bool
		mir       bool
		llvm      bool
	}{
		{phase: phase.Parsed},
		{phase: phase.Typechecked, scope: true, bindings: true, exportAPI: true, thir: true},
		{phase: phase.CFG, scope: true, bindings: true, exportAPI: true, thir: true, cfg: true},
		{phase: phase.Analyzed, scope: true, bindings: true, exportAPI: true, thir: true, cfg: true, analysis: true},
		{phase: phase.Usage, scope: true, bindings: true, exportAPI: true, thir: true, cfg: true, analysis: true},
		{phase: phase.MIR, scope: true, bindings: true, exportAPI: true, thir: true, cfg: true, analysis: true, mir: true},
		{phase: phase.Backend, scope: true, bindings: true, exportAPI: true, thir: true, cfg: true, analysis: true, mir: true, llvm: true},
	}
	for _, test := range tests {
		mod := moduleWithArtifacts()
		mod.ResetToPhase(test.phase)
		if mod.Phase != test.phase || (mod.ModuleScope != nil) != test.scope ||
			(mod.SymbolIndex != nil) != test.bindings ||
			(mod.THIR != nil) != test.thir ||
			(mod.SemanticExportFingerprint != "") != test.exportAPI ||
			(mod.CFG != nil) != test.cfg ||
			(mod.Analysis != nil) != test.analysis ||
			(mod.MIR != nil) != test.mir ||
			(mod.LLVMIR != "") != test.llvm {
			t.Fatalf("phase %v reset = %#v", test.phase, mod)
		}
	}
}

func TestModuleResetSemanticDataInitializesCurrentResults(t *testing.T) {
	module := &module.Module{SymbolIndex: symbols.NewIndex()}
	id := symbols.ProjectedSymbolID(symbols.SymbolConst, "Value")
	value, ok := constvalue.NewIntText("1", "i32")
	if !ok {
		t.Fatal("failed to construct constant")
	}
	module.SymbolIndex.PublishConstant(id, value)
	previous := module.SymbolIndex

	module.ResetSemanticData()
	if module.SymbolIndex == nil || module.SymbolIndex.OperationFunctions() == nil {
		t.Fatalf("semantic reset = %#v", module)
	}
	if module.SymbolIndex == previous {
		t.Fatal("semantic reset retained previous generation symbol index")
	}
	if got := module.SymbolIndex.ConstantValue(id); got != nil {
		t.Fatalf("semantic reset retained published constant: %#v", got)
	}
}

func TestModuleExprTypeEvidenceFollowsPhaseLifecycle(t *testing.T) {
	module := moduleWithArtifacts()
	base := module.BaseExprType(source.ParsedNodeID(1))
	if base == nil {
		t.Fatal("typechecked module has no base expression type")
	}
	if got := module.EffectiveExprType(source.ParsedNodeID(1)); got != base {
		t.Fatalf("effective type with empty analysis = %#v, want base type %#v", got, base)
	}

	module.Analysis = nil
	if got := module.EffectiveExprType(source.ParsedNodeID(1)); got != base {
		t.Fatalf("effective type without flow = %#v, want base type %#v", got, base)
	}
	module.ResetToPhase(phase.Typechecked)
	if module.BaseExprType(source.ParsedNodeID(1)) != base {
		t.Fatal("typechecked reset discarded base expression type")
	}
	module.ResetToPhase(phase.Parsed)
	if module.BaseExprType(source.ParsedNodeID(1)) != nil || module.EffectiveExprType(source.ParsedNodeID(1)) != nil {
		t.Fatal("parsed reset retained expression type evidence")
	}
}

func TestModuleExprTypeEvidenceHandlesMissingTHIR(t *testing.T) {
	var mod *module.Module
	if mod.BaseExprType(source.ParsedNodeID(1)) != nil || mod.EffectiveExprType(source.ParsedNodeID(1)) != nil {
		t.Fatal("nil module returned expression type evidence")
	}
	mod = &module.Module{}
	if mod.BaseExprType(source.ParsedNodeID(1)) != nil || mod.EffectiveExprType(source.ParsedNodeID(1)) != nil {
		t.Fatal("module without THIR returned expression type evidence")
	}
}

func TestModuleResetToPhaseRetainsCFGIdentity(t *testing.T) {
	module := moduleWithArtifacts()
	graph := module.CFG.Functions[0]
	module.ResetToPhase(phase.CFG)
	if module.CFG.Functions[0] != graph {
		t.Fatal("phase reset cloned immutable CFG")
	}
	if module.Analysis != nil {
		t.Fatal("CFG reset retained analysis artifact")
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
	symbol := symbols.New(symbols.ProjectedSymbolID(symbols.SymbolType, "Box"), "Box", symbols.SymbolType, declaration, nil)
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

func TestPublishedConstantReadsOwnerSymbolState(t *testing.T) {
	ctx := New(".", ".peep", diagnostics.NewDiagnosticBag())
	ownerID := moduleid.ID{Origin: string(ModuleOriginLocal), ImportPath: "lib"}
	owner := &module.Module{ID: ownerID, SymbolIndex: symbols.NewIndex()}
	sym := symbols.New(symbols.ProjectedSymbolID(symbols.SymbolConst, "Value"), "Value", symbols.SymbolConst, nil, nil)
	sym.DefiningModule = ownerID
	value, ok := constvalue.NewIntText("9", "i32")
	if !ok {
		t.Fatal("failed to construct constant")
	}
	owner.SymbolIndex.PublishConstant(sym.ID, value)
	if err := ctx.AddModule(owner); err != nil {
		t.Fatalf("add owner module: %v", err)
	}
	consumer := &module.Module{ID: moduleid.ID{Origin: string(ModuleOriginLocal), ImportPath: "app"}}
	if got := ctx.PublishedConstant(consumer, sym); got != value {
		t.Fatalf("published constant = %#v, want %#v", got, value)
	}
}
