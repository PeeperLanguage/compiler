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
	"compiler/internal/semantics/flowresult"
	"compiler/internal/semantics/intrinsics"
	"compiler/internal/semantics/ownershipresult"
	"compiler/internal/semantics/symbols"
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
	// Absolute source path.
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
	// TypedASTNodes indexes final AST after semantic expansion.
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
	// Grouped semantic analysis metadata.
	Semantics *SemanticInfo
	// Import alias -> resolved module import.
	Imports map[string]ResolvedImport
}

type SemanticInfo struct {
	BlockScopes     map[ast.NodeID]*symbols.Scope
	ResolvedSymbols map[ast.NodeID]*symbols.Symbol
	// ExpandedDefaultBindings marks cloned NodeIDs injected by
	// call-site default expansion. These idents must resolve
	// through the declaration module's ResolvedSymbols instead of
	// caller scope. The Binding.Local gate prevents pointer-escape
	// misclassification.
	ExpandedDefaultBindings  map[ast.NodeID]struct{}
	ExprTypes                map[ast.NodeID]typeinfo.Type
	CaseTests                map[ast.NodeID]flowresult.CaseTest
	ConstValues              map[symbols.SymbolID]constvalue.Value
	MethodSets               map[string][]*symbols.Symbol
	MethodSymbol             map[ast.NodeID]*symbols.Symbol
	InterfaceImplementations map[ast.NodeID][]InterfaceImplementation
	ImplicitCallArguments    map[ast.NodeID]typeinfo.Type
	CompilerCalls            map[ast.NodeID]CompilerCall
	VariantConstructions     map[ast.NodeID]VariantConstruction
	OperationFunctions       []*symbols.Symbol
}

// VariantConstruction is typechecker proof consumed by HIR without resolving
// source paths or revalidating constructor fields.
type VariantConstruction struct {
	EnumType typeinfo.Type
	Case     int
	Payload  *typeinfo.StructType
	Fields   []ast.Expr
}

// CompilerCall is typechecker-owned dispatch evidence consumed by HIR.
type CompilerCall struct {
	Operation symbols.CompilerOp
	Kind      intrinsics.FunctionKind
}

// InterfaceImplementation is typechecker proof that one declared method can
// materialize an interface slot. HIR consumes this proof without resolving the
// concrete method set again.
type InterfaceImplementation struct {
	MethodName   string
	Symbol       *symbols.Symbol
	CallableType *typeinfo.FuncType
	OwnerKey     string
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

func NewSemanticInfo() *SemanticInfo {
	return &SemanticInfo{
		BlockScopes:              make(map[ast.NodeID]*symbols.Scope),
		ResolvedSymbols:          make(map[ast.NodeID]*symbols.Symbol),
		ExpandedDefaultBindings:  make(map[ast.NodeID]struct{}),
		ExprTypes:                make(map[ast.NodeID]typeinfo.Type),
		CaseTests:                make(map[ast.NodeID]flowresult.CaseTest),
		ConstValues:              make(map[symbols.SymbolID]constvalue.Value),
		MethodSets:               make(map[string][]*symbols.Symbol),
		MethodSymbol:             make(map[ast.NodeID]*symbols.Symbol),
		InterfaceImplementations: make(map[ast.NodeID][]InterfaceImplementation),
		ImplicitCallArguments:    make(map[ast.NodeID]typeinfo.Type),
		CompilerCalls:            make(map[ast.NodeID]CompilerCall),
		VariantConstructions:     make(map[ast.NodeID]VariantConstruction),
		OperationFunctions:       make([]*symbols.Symbol, 0),
	}
}

func (m *Module) ResetSemanticData() {
	if m == nil {
		return
	}
	m.Semantics = NewSemanticInfo()
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
	if m.Semantics == nil {
		return nil
	}
	return m.Semantics.ExprTypes[id]
}

// resetToPhase retains artifacts through phase and invalidates downstream data.
func (m *Module) resetToPhase(retained phase.Phase) {
	if m == nil {
		return
	}
	m.Phase = retained
	if retained <= phase.Parsed {
		m.ModuleScope = nil
		m.Semantics = nil
	}
	if retained < phase.Typechecked {
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
	ctx.mu.Lock()
	defer ctx.mu.Unlock()

	ctx.modules[module.Key] = module
	if module.FilePath != "" {
		ctx.fileIndex[CanonicalPath(module.FilePath)] = module.Key
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
