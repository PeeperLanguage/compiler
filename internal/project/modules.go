package project

import (
	"path/filepath"
	"strings"

	"compiler/internal/constvalue"
	"compiler/internal/frontend/ast"
	"compiler/internal/graph"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/hir"
	"compiler/internal/ir/mir"
	"compiler/internal/phase"
	"compiler/internal/semantics/bindingresult"
	"compiler/internal/semantics/flowresult"
	"compiler/internal/semantics/ownershipresult"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typecheckresult"
	"compiler/internal/semantics/typeinfo"
)

// Where a module was loaded from.
type ModuleOrigin string

const (
	// Project source file.
	ModuleOriginLocal ModuleOrigin = "local"
	// Packaged library source file loaded from a namespace root such as core/vendor.
	ModuleOriginStdlib ModuleOrigin = "core"
	// Package dependency source file.
	ModuleOriginDependency ModuleOrigin = "dependency"
)

const GraphEdgeImport graph.EdgeKind = "import"

// Source unit shared by every compiler phase.
type Module struct {
	// Unique graph identity.
	Key string
	// Module path used by imports.
	ImportPath string
	// Absolute slash-separated source path.
	FilePath string
	// Optional namespace for packaged libraries such as core/vendor.
	Namespace string
	// User-selected entry module.
	IsEntry bool
	// Local, stdlib, or dependency.
	Origin ModuleOrigin
	// Dependency alias, when any.
	Dependency string
	// Loaded source text.
	Content string
	// ContentProvided distinguishes an explicit empty source from a module that
	// still needs to load its source from FilePath.
	ContentProvided bool
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
	// TypedASTNodes indexes source and typechecker-generated expressions.
	TypedASTNodes map[ast.NodeID]ast.Node
	// Canonical IR slots.
	HIR       *hir.Module
	CFG       *cfg.Module
	Flow      *flowresult.Result
	Ownership ownershipresult.Result
	MIR       *mir.Module
	LLVMIR    string
	// Top-level names visible in module.
	ModuleScope *symbols.Scope
	// Generic declaration syntax and semantic shells produced by collection.
	// Fresh incremental contexts reindex this immutable phase artifact.
	namedTypeDeclarations map[string]namedTypeDeclaration
	// Staged symbol/scope graph for current semantic generation.
	Bindings *bindingresult.Result
	// Finalized module constants plus mutable constant-query cache.
	ConstValues map[symbols.SymbolID]constvalue.Value
	// Base typechecker result for current semantic generation.
	Typechecking *typecheckresult.Result
	// Import alias -> resolved module import.
	Imports map[string]ResolvedImport
}

func (m *Module) DefiningModuleKey() symbols.DefiningModuleKey {
	if m == nil {
		return symbols.DefiningModuleKey{}
	}
	return symbols.DefiningModuleKey{
		Origin:     string(m.Origin),
		Namespace:  m.Namespace,
		Dependency: m.Dependency,
		ImportPath: m.ImportPath,
	}
}

// TypeDeclarationIdentity anchors nominal type identity at its declaring module.
func (m *Module) TypeDeclarationIdentity(name string) string {
	if m == nil || m.Key == "" || name == "" {
		return name
	}
	return m.Key + "::" + name
}

// ExpandedDefaultBinding resolves declaration-module symbols paired with generated
// default-expression markers. Local remains false for caller escape analysis.
func (m *Module) ExpandedDefaultBinding(ident *ast.Ident) (place.Binding, bool) {
	if m == nil || m.Bindings == nil || m.Typechecking == nil || ident == nil {
		return place.Binding{}, false
	}
	if _, ok := m.Typechecking.ExpandedDefaultBindings[ident.ID()]; !ok {
		return place.Binding{}, false
	}
	return place.Binding{Symbol: m.Bindings.NodeSymbols[ident.ID()]}, true
}

// RebuildTypedASTIndex publishes canonical node lookup after typechecking.
func (m *Module) RebuildTypedASTIndex() {
	if m == nil {
		return
	}
	m.TypedASTNodes = ast.Index(m.AST)
	if m.Typechecking == nil {
		return
	}
	for _, args := range m.Typechecking.EffectiveCallArguments {
		for _, arg := range args {
			ast.Inspect(arg, func(node ast.Node) bool {
				if node != nil {
					m.TypedASTNodes[node.ID()] = node
				}
				return true
			})
		}
	}
}

func (m *Module) ResetSemanticData() {
	if m == nil {
		return
	}
	m.Bindings = bindingresult.New()
	m.ConstValues = make(map[symbols.SymbolID]constvalue.Value)
	m.Typechecking = nil
}

// BaseExprType returns canonical base typechecker evidence when available.
func (m *Module) BaseExprType(id ast.NodeID) typeinfo.Type {
	if m == nil || m.Typechecking == nil {
		return nil
	}
	return m.Typechecking.ExprTypes[id]
}

// EffectiveExprType returns per-use flow refinement when available and falls
// back to the canonical base typechecker result.
func (m *Module) EffectiveExprType(id ast.NodeID) typeinfo.Type {
	if m == nil {
		return nil
	}
	if m.Flow != nil {
		if typ := m.Flow.ExprTypes[id]; typ != nil {
			return typ
		}
	}
	return m.BaseExprType(id)
}

// resetToPhase retains artifacts through phase and invalidates downstream data.
func (m *Module) resetToPhase(retained phase.Phase) {
	if m == nil {
		return
	}
	m.Phase = retained
	if retained <= phase.Parsed {
		m.ModuleScope = nil
		m.Bindings = nil
		m.ConstValues = nil
	}
	if retained < phase.Collected {
		m.namedTypeDeclarations = nil
	}
	if retained < phase.Typechecked {
		m.Typechecking = nil
		m.SemanticExportFingerprint = ""
		m.TypedASTNodes = nil
	}
	if retained < phase.CFG {
		m.CFG = nil
	}
	if retained < phase.FlowTyped {
		m.Flow = nil
	}
	if retained < phase.Ownership {
		m.Ownership = nil
	}
	if retained < phase.HIR {
		m.HIR = nil
	}
	if retained < phase.MIR {
		m.MIR = nil
	}
	if retained < phase.Backend {
		m.LLVMIR = ""
	}
}

// CanonicalPath returns absolute slash-separated path for stable map keys.
func CanonicalPath(path string) string {
	if path == "" {
		return ""
	}
	clean := filepath.Clean(path)
	if abs, err := filepath.Abs(clean); err == nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(clean)
}

func PathWithinRoot(rootPath, path string) bool {
	rootPath = CanonicalPath(rootPath)
	path = CanonicalPath(path)
	if rootPath == "" || path == "" {
		return false
	}
	if rootPath == path {
		return true
	}
	return strings.HasPrefix(path, rootPath+"/")
}

// NewModuleForFile builds one file-backed module with canonical origin,
// namespace, key, and import path derived from compiler config.
func (ctx *CompilerContext) NewModuleForFile(filePath, content string) *Module {
	if ctx == nil || filePath == "" {
		return nil
	}
	origin, namespace := ctx.ModuleOriginForFile(filePath)
	module := &Module{
		Key:             ModuleKeyFor(origin, filePath),
		FilePath:        filePath,
		Namespace:       namespace,
		Origin:          origin,
		Content:         content,
		ContentProvided: true,
	}
	if importPath, err := ctx.ImportPathForFile(origin, namespace, filePath); err == nil {
		module.ImportPath = importPath
	}
	return module
}

// Register a module in shared compiler state.
func (ctx *CompilerContext) AddModule(module *Module) {
	if ctx == nil || module == nil || module.Key == "" {
		return
	}
	module.FilePath = CanonicalPath(module.FilePath)
	ctx.mu.Lock()
	defer ctx.mu.Unlock()

	ctx.modules[module.Key] = module
	if module.FilePath != "" {
		ctx.fileIndex[module.FilePath] = module.Key
	}
	if module.Phase >= phase.Collected {
		for identity := range module.namedTypeDeclarations {
			ctx.typeDeclarations[identity] = module
		}
	}
}

// Lookup by graph identity.
func (ctx *CompilerContext) ModuleByKey(key string) (*Module, bool) {
	if ctx == nil || key == "" {
		return nil, false
	}
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()
	module, ok := ctx.modules[key]
	return module, ok
}

// SetSemanticExportBaseline records prior semantic API state for incremental comparison.
func (ctx *CompilerContext) SetSemanticExportBaseline(key, fingerprint string) {
	if ctx == nil || key == "" || fingerprint == "" {
		return
	}
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	ctx.semanticExportBaselines[key] = fingerprint
}

// SemanticExportBaseline returns prior semantic API state when supplied by a client.
func (ctx *CompilerContext) SemanticExportBaseline(key string) (string, bool) {
	if ctx == nil || key == "" {
		return "", false
	}
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()
	fingerprint, ok := ctx.semanticExportBaselines[key]
	return fingerprint, ok
}

// Lookup by source path.
func (ctx *CompilerContext) ModuleByFile(filePath string) (*Module, bool) {
	if ctx == nil || filePath == "" {
		return nil, false
	}
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()
	key, ok := ctx.fileIndex[CanonicalPath(filePath)]
	if !ok {
		return nil, false
	}
	module, ok := ctx.modules[key]
	return module, ok
}

// Snapshot of known modules.
func (ctx *CompilerContext) Modules() []*Module {
	if ctx == nil {
		return nil
	}
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()
	modules := make([]*Module, 0, len(ctx.modules))
	for _, module := range ctx.modules {
		modules = append(modules, module)
	}
	return modules
}
