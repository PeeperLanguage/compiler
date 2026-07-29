package project

import (
	"path/filepath"
	"strings"

	"compiler/internal/constvalue"
	"compiler/internal/frontend/ast"
	"compiler/internal/graph"
	"compiler/internal/ir/hir"
	"compiler/internal/ir/mir"
	"compiler/internal/semantics/cfg"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
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

type ModulePhase uint8

const (
	PhaseNone ModulePhase = iota
	PhaseParsed
	PhaseCollected
	PhaseBound
	PhaseResolved
	PhaseConstEval
	PhaseTypechecked
	PhaseOwnership
	PhaseUsage
	PhaseHIR
	PhaseCFG
	PhaseMIR
	PhaseBackend
)

const (
	GraphNodeModule graph.NodeKind = "module"
	GraphEdgeImport graph.EdgeKind = "import"
)

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
	// Reserved for incremental builds.
	ContentHash string
	// Stable syntax-derived import surface for invalidation.
	ImportFingerprint string
	// Stable syntax-derived export surface for invalidation.
	ExportFingerprint string
	// Last completed compiler phase for this module snapshot.
	Phase ModulePhase
	// Parsed syntax tree.
	AST *ast.Module
	// Canonical IR slots.
	HIR    *hir.Module
	CFG    []*cfg.Graph
	MIR    *mir.Module
	LLVMIR string
	// Top-level names visible in module.
	ModuleScope *table.Scope
	// Grouped semantic analysis metadata.
	Semantics *SemanticInfo
	// Import alias -> resolved module import.
	Imports map[string]ResolvedImport
}

type SemanticInfo struct {
	BlockScopes     map[ast.NodeID]*table.Scope
	ResolvedSymbols map[ast.NodeID]*symbols.Symbol
	// ExpandedDefaultBindings marks cloned NodeIDs injected by
	// call-site default expansion. These idents must resolve
	// through the declaration module's ResolvedSymbols instead of
	// caller scope. The Binding.Local gate prevents pointer-escape
	// misclassification.
	ExpandedDefaultBindings map[ast.NodeID]struct{}
	ExprTypes               map[ast.NodeID]typeinfo.Type
	ConstValues             map[symbols.SymbolID]constvalue.Value
	MethodSets              map[string][]*symbols.Symbol
	MethodSymbol            map[ast.NodeID]*symbols.Symbol
	DiscardBindingValue     map[symbols.SymbolID]struct{}
	CleanupAfterBlock       map[ast.NodeID][]*symbols.Symbol
	CleanupBeforeReturn     map[ast.NodeID][]*symbols.Symbol
	DropBeforeAssign        map[ast.NodeID]struct{}
	DropDiscardedExpr       map[ast.NodeID]struct{}
	DropProjectionBase      map[ast.NodeID]struct{}
}

func NewSemanticInfo() *SemanticInfo {
	return &SemanticInfo{
		BlockScopes:             make(map[ast.NodeID]*table.Scope),
		ResolvedSymbols:         make(map[ast.NodeID]*symbols.Symbol),
		ExpandedDefaultBindings: make(map[ast.NodeID]struct{}),
		ExprTypes:               make(map[ast.NodeID]typeinfo.Type),
		ConstValues:             make(map[symbols.SymbolID]constvalue.Value),
		MethodSets:              make(map[string][]*symbols.Symbol),
		MethodSymbol:            make(map[ast.NodeID]*symbols.Symbol),
		DiscardBindingValue:     make(map[symbols.SymbolID]struct{}),
		CleanupAfterBlock:       make(map[ast.NodeID][]*symbols.Symbol),
		CleanupBeforeReturn:     make(map[ast.NodeID][]*symbols.Symbol),
		DropBeforeAssign:        make(map[ast.NodeID]struct{}),
		DropDiscardedExpr:       make(map[ast.NodeID]struct{}),
		DropProjectionBase:      make(map[ast.NodeID]struct{}),
	}
}

func (m *Module) ResetSemanticData() {
	if m == nil {
		return
	}
	m.Semantics = NewSemanticInfo()
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
		Key:       ModuleKeyFor(origin, filePath),
		FilePath:  filePath,
		Namespace: namespace,
		Origin:    origin,
		Content:   content,
	}
	if importPath, err := ctx.ImportPathForFile(origin, namespace, filePath); err == nil {
		module.ImportPath = importPath
	}
	return module
}

// Register a module in the shared graph.
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
	if ctx.Graph != nil {
		ctx.Graph.AddNode(graph.NodeID(module.Key))
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
