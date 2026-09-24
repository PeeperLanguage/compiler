package module

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/mir"
	"compiler/internal/ir/thir"
	"compiler/internal/moduleid"
	"compiler/internal/phase"
	"compiler/internal/semantics/bindingresult"
	"compiler/internal/semantics/constantresult"
	"compiler/internal/semantics/effect"
	"compiler/internal/semantics/flowresult"
	"compiler/internal/semantics/ownershipresult"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typecheckresult"
	"compiler/internal/semantics/typeinfo"
)

// ResolvedImport identifies one file-backed import after project resolution.
type ResolvedImport struct {
	ID       moduleid.ID
	Decl     *ast.ImportDecl
	FilePath string
}

// TypeDeclaration is collection's reusable syntax and semantic shell for one
// named type. Retained modules keep this artifact so fresh incremental compiler
// contexts can rebuild derived cross-module indexes without recollecting syntax.
type TypeDeclaration struct {
	Syntax ast.TypeDecl
	Base   *typeinfo.DefinedType
}

// Module is one source unit and its reusable compiler artifacts.
type Module struct {
	// Canonical semantic, import, graph, and ownership identity.
	ID moduleid.ID
	// Absolute slash-separated source path.
	FilePath string
	// User-selected entry module.
	IsEntry bool
	// Loaded source text.
	Content string
	// HasProvidedContent distinguishes an explicit empty source from a module that
	// still needs to load its source from FilePath.
	HasProvidedContent bool
	// Reserved for incremental builds.
	ContentHash string
	// Stable syntax-derived import surface for invalidation.
	ImportFingerprint string
	// Stable syntax-derived export surface for invalidation.
	ExportFingerprint string
	// Stable compiler-visible export surface finalized after semantic typing.
	SemanticExportFingerprint string
	// Last completed compiler phase for this module snapshot.
	Phase phase.Phase
	// Parsed syntax tree.
	AST *ast.Module
	// Canonical IR slots.
	THIR *thir.Module
	CFG  *cfg.Module
	Flow *flowresult.Result
	// Effects is the published semantic meaning of each CFG site, produced once
	// and consumed by the dataflow analyses.
	Effects   effect.Result
	Ownership ownershipresult.Result
	MIR       *mir.Module
	LLVMIR    string
	// Top-level names visible in module.
	ModuleScope *symbols.Scope
	// Generic declaration syntax and semantic shells produced by collection.
	typeDeclarations map[string]TypeDeclaration
	// Staged symbol/scope graph for current semantic generation.
	Bindings *bindingresult.Result
	// Constant-evaluation artifacts for current semantic generation.
	Constants *constantresult.Result
	// Base typechecker result for current semantic generation.
	Typechecking *typecheckresult.Result
	// Import alias -> resolved module import.
	Imports map[string]ResolvedImport
}

// TypeDeclarationIdentity anchors nominal type identity at its declaring module.
func (m *Module) TypeDeclarationIdentity(name string) string {
	if m == nil || !m.ID.IsValid() || name == "" {
		return name
	}
	return m.ID.String() + "::" + name
}

// RecordTypeDeclaration retains collection evidence needed to instantiate and
// reindex named types across incremental compiler contexts.
func (m *Module) RecordTypeDeclaration(identity string, declaration TypeDeclaration) {
	if m == nil || identity == "" || declaration.Syntax == nil || declaration.Base == nil {
		return
	}
	if m.typeDeclarations == nil {
		m.typeDeclarations = make(map[string]TypeDeclaration)
	}
	m.typeDeclarations[identity] = declaration
}

// TypeDeclaration returns retained collection evidence for identity.
func (m *Module) TypeDeclaration(identity string) (TypeDeclaration, bool) {
	if m == nil {
		return TypeDeclaration{}, false
	}
	declaration, ok := m.typeDeclarations[identity]
	return declaration, ok
}

// TypeDeclarationIdentities returns retained declaration identities without
// exposing the module's artifact storage.
func (m *Module) TypeDeclarationIdentities() []string {
	if m == nil {
		return nil
	}
	identities := make([]string, 0, len(m.typeDeclarations))
	for identity := range m.typeDeclarations {
		identities = append(identities, identity)
	}
	return identities
}

// RecordImportedUse publishes usage only after a source semantic phase has
// resolved alias::member successfully. Query and tooling paths must not call it.
func (m *Module) RecordImportedUse(alias string, target *symbols.Symbol) {
	if m == nil || m.ModuleScope == nil || target == nil {
		return
	}
	if _, imported := m.Imports[alias]; !imported {
		return
	}
	aliasSymbol, found := m.ModuleScope.LookupLocal(alias)
	if !found || aliasSymbol == nil || aliasSymbol.Kind != symbols.SymbolImport {
		return
	}
	aliasSymbol.MarkUsed()
	target.MarkUsed()
}

// ExpandedDefaultBinding resolves declaration-module symbols paired with generated
// default-expression markers. Local remains false for caller escape analysis.
func (m *Module) ExpandedDefaultBinding(ident *ast.Ident) (place.Binding, bool) {
	if m == nil || m.Bindings == nil || m.Typechecking == nil || ident == nil {
		return place.Binding{}, false
	}
	if !m.Typechecking.ExpandedDefaultBinding(ident.ID()) {
		return place.Binding{}, false
	}
	return place.Binding{Symbol: m.Bindings.Symbol(ident)}, true
}

func (m *Module) ResetSemanticData() {
	if m == nil {
		return
	}
	m.Bindings = bindingresult.New()
	m.Constants = constantresult.New()
	m.Typechecking = nil
}

// BaseExprType returns canonical base typechecker evidence when available.
func (m *Module) BaseExprType(id ast.NodeID) typeinfo.Type {
	if m == nil || id == 0 {
		return nil
	}
	if m.THIR != nil {
		if expr, ok := m.THIR.Node(ir.NodeID(id)).(thir.Expr); ok && expr != nil {
			return expr.ExprType()
		}
	}
	if m.Typechecking == nil {
		return nil
	}
	return m.Typechecking.ExprType(id)
}

// EffectiveExprType returns per-use flow refinement when available and falls
// back to the canonical base typechecker result.
func (m *Module) EffectiveExprType(id ast.NodeID) typeinfo.Type {
	if m == nil {
		return nil
	}
	if m.Flow != nil {
		if typ := m.Flow.ExprType(id); typ != nil {
			return typ
		}
	}
	return m.BaseExprType(id)
}

// ResetToPhase retains artifacts through retained and invalidates later data.
func (m *Module) ResetToPhase(retained phase.Phase) {
	if m == nil {
		return
	}
	m.Phase = retained
	if retained <= phase.Parsed {
		m.ModuleScope = nil
		m.Bindings = nil
		m.Constants = nil
	}
	if retained < phase.Collected {
		m.typeDeclarations = nil
	}
	if retained < phase.Typechecked {
		m.Typechecking = nil
		m.THIR = nil
		m.SemanticExportFingerprint = ""
	}
	if retained < phase.CFG {
		m.CFG = nil
	}
	if retained < phase.FlowTyped {
		m.Flow = nil
	}
	if retained < phase.Effects {
		m.Effects = nil
	}
	if retained < phase.Ownership {
		m.Ownership = nil
	}
	if retained < phase.MIR {
		m.MIR = nil
	}
	if retained < phase.Backend {
		m.LLVMIR = ""
	}
}
