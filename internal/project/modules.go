package project

import (
	"fmt"
	"path/filepath"
	"strings"

	"compiler/internal/constvalue"
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/graph"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/hir"
	"compiler/internal/ir/mir"
	"compiler/internal/moduleid"
	"compiler/internal/phase"
	"compiler/internal/semantics/bindingresult"
	"compiler/internal/semantics/constantresult"
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
	// Canonical semantic, import, graph, and ownership identity.
	ID moduleid.ID
	// Absolute slash-separated source path.
	FilePath string
	// User-selected entry module.
	IsEntry bool
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
	// Constant-evaluation artifacts for current semantic generation.
	Constants *constantresult.Result
	// Base typechecker result for current semantic generation.
	Typechecking *typecheckresult.Result
	// Import alias -> resolved module import.
	Imports map[string]ResolvedImport
}

// TypeDeclarationIdentity anchors nominal type identity at its declaring module.
func (m *Module) TypeDeclarationIdentity(name string) string {
	if m == nil || !m.ID.Valid() || name == "" {
		return name
	}
	return m.ID.String() + "::" + name
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
	m.Constants = constantresult.New()
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
		m.Constants = nil
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

// IdentityForFile assembles canonical module identity for a file. Every producer
// of a moduleid.ID goes through here so origin, namespace and derived import path
// cannot drift apart. Assembling identity separately is what let the prelude
// register under an import path no import could reproduce.
func (ctx *CompilerContext) IdentityForFile(origin ModuleOrigin, namespace, filePath string) (moduleid.ID, error) {
	importPath, err := ctx.ImportPathForFile(origin, namespace, filePath)
	if err != nil {
		return moduleid.ID{}, err
	}
	return moduleid.ID{
		Origin:     string(origin),
		Namespace:  namespace,
		ImportPath: importPath,
	}, nil
}

// NewModuleForFile builds one file-backed module with canonical identity derived from compiler config.
func (ctx *CompilerContext) NewModuleForFile(filePath, content string) *Module {
	if ctx == nil || filePath == "" {
		return nil
	}
	origin, namespace := ctx.ModuleOriginForFile(filePath)
	id, err := ctx.IdentityForFile(origin, namespace, filePath)
	if err != nil {
		return nil
	}
	return &Module{
		ID:              id,
		FilePath:        filePath,
		Content:         content,
		ContentProvided: true,
	}
}

// Register a module in shared compiler state.
func (ctx *CompilerContext) AddModule(module *Module) {
	if ctx == nil || module == nil || !module.ID.Valid() {
		return
	}
	module.FilePath = CanonicalPath(module.FilePath)
	ctx.mu.Lock()

	// Identity and file must agree in both directions, and both checks run
	// before any index is touched so a rejected registration cannot leave the
	// registry half-updated. Import paths and library-root configuration can
	// both reach these, so they are user-facing diagnostics, not compiler bugs.
	conflict := ""
	if previousID, found := ctx.fileIndex[module.FilePath]; module.FilePath != "" && found && previousID != module.ID {
		conflict = fmt.Sprintf("module file %s is already registered as %s and cannot also be %s",
			module.FilePath, previousID.ImportPath, module.ID.ImportPath)
	} else if previous := ctx.modules[module.ID]; previous != nil && module.FilePath != "" &&
		previous.FilePath != "" && previous.FilePath != module.FilePath {
		conflict = fmt.Sprintf("module identity %s is already registered for file %s and cannot also name %s",
			module.ID.ImportPath, previous.FilePath, module.FilePath)
	}
	if conflict != "" {
		ctx.mu.Unlock()
		if ctx.Diagnostics != nil {
			ctx.Diagnostics.AddError(diagnostics.ErrAmbiguousImport, conflict, nil, "")
		}
		return
	}
	ctx.modules[module.ID] = module
	if module.FilePath != "" {
		ctx.fileIndex[module.FilePath] = module.ID
	}
	if module.Phase >= phase.Collected {
		for identity := range module.namedTypeDeclarations {
			ctx.typeDeclarations[identity] = module
		}
	}
	ctx.mu.Unlock()
}

// PublishedConstant returns the authoritative value of a constant symbol,
// resolving symbols owned by another module through their defining identity, and
// nil when no value is published. Constant evaluation never publishes a nil value,
// so nil is the absent case and no separate found flag is needed.
// Query-cache entries are excluded on purpose: only published module values are
// stable enough for cross-module reads and export fingerprints.
func (ctx *CompilerContext) PublishedConstant(module *Module, sym *symbols.Symbol) constvalue.Value {
	if sym == nil {
		return nil
	}
	owner := module
	// Only the cross-module hop needs a context; a local value stays readable
	// from the module alone so callers without a registry still fingerprint.
	if ownerID := sym.DefiningModule; ownerID.Valid() && (module == nil || ownerID != module.ID) {
		if ctx == nil {
			return nil
		}
		found := false
		if owner, found = ctx.ModuleByID(ownerID); !found {
			return nil
		}
	}
	if owner == nil || owner.Constants == nil {
		return nil
	}
	return owner.Constants.ModuleValues[sym.ID]
}

// ModuleByID resolves canonical module identity.
func (ctx *CompilerContext) ModuleByID(id moduleid.ID) (*Module, bool) {
	if ctx == nil || !id.Valid() {
		return nil, false
	}
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()
	module, ok := ctx.modules[id]
	return module, ok
}

// SetSemanticExportBaseline records prior semantic API state for incremental comparison.
func (ctx *CompilerContext) SetSemanticExportBaseline(id moduleid.ID, fingerprint string) {
	if ctx == nil || !id.Valid() || fingerprint == "" {
		return
	}
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	ctx.semanticExportBaselines[id] = fingerprint
}

// SemanticExportBaseline returns prior semantic API state when supplied by a client.
func (ctx *CompilerContext) SemanticExportBaseline(id moduleid.ID) (string, bool) {
	if ctx == nil || !id.Valid() {
		return "", false
	}
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()
	fingerprint, ok := ctx.semanticExportBaselines[id]
	return fingerprint, ok
}

// Lookup by source path.
func (ctx *CompilerContext) ModuleByFile(filePath string) (*Module, bool) {
	if ctx == nil || filePath == "" {
		return nil, false
	}
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()
	id, ok := ctx.fileIndex[CanonicalPath(filePath)]
	if !ok {
		return nil, false
	}
	module, ok := ctx.modules[id]
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
