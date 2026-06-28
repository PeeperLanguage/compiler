package project

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/graph"
	"compiler/internal/ir/hir"
	"compiler/internal/ir/mir"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
	"compiler/pkg/manifest"
	"compiler/pkg/peeper"
)

// Bundled libraries base directory relative to the installed compiler binary.
const PACKAGED_LIBS_DIR = "../libs"

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
	PhaseTypechecked
	PhaseOwnership
	PhaseUsage
	PhaseHIR
	PhaseMIR
	PhaseBackend
)

// Canonical file-backed import after resolver lookup.
type ResolvedImport struct {
	// Stable graph identity.
	Key string
	// Module path as written in source.
	ImportPath string
	// Source import declaration, when resolved from parsed syntax.
	Decl *ast.ImportDecl
	// Absolute source path.
	FilePath string
	// Local, stdlib, or dependency.
	Origin ModuleOrigin
	// Optional namespace for packaged libraries such as core/vendor.
	Namespace string
	// Manifest alias for dependency imports.
	DependencyAlias string
}

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
	MIR    *mir.Module
	LLVMIR string
	// Top-level names visible in module.
	ModuleScope *table.Scope
	// Grouped semantic analysis metadata.
	Semantics *SemanticInfo
	// Import alias -> resolved module import.
	Imports map[string]ResolvedImport
}

// Shared state for one compilation.
type CompilerContext struct {
	// Normalized compiler options.
	Config Config
	// Shared diagnostic stream.
	Diagnostics *diagnostics.DiagnosticBag
	// Optional per-run metrics for benchmarks and incremental validation.
	Metrics *CompileMetrics
	// Predeclared symbols visible before user/prelude code.
	GlobalScope *table.Scope

	// Module key -> module.
	modules map[string]*Module
	// Canonical file path -> module key.
	fileIndex map[string]string
	// Shared compiler dependency graph.
	Graph *graph.Graph

	// Guards module indexes.
	mu sync.RWMutex
}

type CompileMetrics struct {
	mu sync.Mutex

	WorkspaceFiles      int
	WorkspaceModules    int
	WorkspaceComponents int
	DirtyFiles          int
	ModulesParsed       int
	ModulesReused       int
	ModulesDowngraded   int
	PhaseAdvances       int
}

func (m *CompileMetrics) AddWorkspaceSnapshot(files, modules, components int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.WorkspaceFiles = files
	m.WorkspaceModules = modules
	m.WorkspaceComponents = components
}

func (m *CompileMetrics) AddDirtyFiles(count int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DirtyFiles += count
}

func (m *CompileMetrics) AddParsedModule() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ModulesParsed++
}

func (m *CompileMetrics) AddReusedModule() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ModulesReused++
}

func (m *CompileMetrics) AddDowngradedModule() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ModulesDowngraded++
}

func (m *CompileMetrics) AddPhaseAdvance() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PhaseAdvances++
}

func (m *CompileMetrics) Snapshot() CompileMetrics {
	if m == nil {
		return CompileMetrics{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return CompileMetrics{
		WorkspaceFiles:      m.WorkspaceFiles,
		WorkspaceModules:    m.WorkspaceModules,
		WorkspaceComponents: m.WorkspaceComponents,
		DirtyFiles:          m.DirtyFiles,
		ModulesParsed:       m.ModulesParsed,
		ModulesReused:       m.ModulesReused,
		ModulesDowngraded:   m.ModulesDowngraded,
		PhaseAdvances:       m.PhaseAdvances,
	}
}

// Context constructor for simple root/extension call sites.
func New(rootDir, extension string, diag *diagnostics.DiagnosticBag) *CompilerContext {
	cfg := Config{
		RootDir:   rootDir,
		Extension: extension,
	}
	return NewWithConfig(cfg, diag)
}

// Options that affect loading, analysis, lowering, or emission.
type Config struct {
	// Project/workspace root.
	RootDir string
	// Required local import prefix for config-backed projects.
	ProjectName string
	// Source file extension.
	Extension string
	// Packaged libraries base directory. Namespace imports map to subdirectories here.
	LibraryBaseDir string
	// Optional explicit namespace -> root overrides.
	LibraryRoots map[string]string
	// Manifest alias -> dependency root.
	DependencyRoots map[string]string
	// Target operating system.
	TargetOS string
	// Target architecture.
	TargetArch string
	// Final backend.
	TargetBackend string
	// Emit debug-friendly artifacts.
	BuildDebug bool
	// Compile test entry points.
	TestMode bool
	// Optional single test name.
	TestName string
}

// Normalize options and create shared compiler state.
func NewWithConfig(cfg Config, diag *diagnostics.DiagnosticBag) *CompilerContext {
	if diag == nil {
		diag = diagnostics.NewDiagnosticBag()
	}
	if cfg.Extension == "" {
		cfg.Extension = peeper.SourceExt
	}
	if cfg.RootDir == "" {
		cfg.RootDir = "."
	}
	if cfg.TargetOS == "" {
		cfg.TargetOS = runtime.GOOS
	}
	if cfg.TargetArch == "" {
		cfg.TargetArch = runtime.GOARCH
	}
	if cfg.TargetBackend == "" {
		cfg.TargetBackend = "llvm"
	}
	cfg.RootDir = filepath.Clean(cfg.RootDir)
	if !filepath.IsAbs(cfg.RootDir) {
		if abs, err := filepath.Abs(cfg.RootDir); err == nil {
			cfg.RootDir = abs
		}
	}
	if cfg.LibraryBaseDir == "" {
		cfg.LibraryBaseDir, _ = libraryBaseDirFromExecutable()
	}
	cfg.LibraryBaseDir = filepath.Clean(cfg.LibraryBaseDir)
	if cfg.LibraryBaseDir != "" && !filepath.IsAbs(cfg.LibraryBaseDir) {
		if abs, err := filepath.Abs(cfg.LibraryBaseDir); err == nil {
			cfg.LibraryBaseDir = abs
		}
	}
	if cfg.LibraryRoots == nil {
		cfg.LibraryRoots = make(map[string]string)
	}
	for namespace, root := range cfg.LibraryRoots {
		root = filepath.Clean(root)
		if root != "" && !filepath.IsAbs(root) {
			if abs, err := filepath.Abs(root); err == nil {
				root = abs
			}
		}
		cfg.LibraryRoots[namespace] = root
	}
	if cfg.LibraryBaseDir != "" {
		if _, err := os.Stat(cfg.LibraryBaseDir); err != nil && !os.IsNotExist(err) {
			diag.Add(diagnostics.NewWarning("failed to access packaged libraries root: " + err.Error()))
		}
	}
	for namespace, root := range cfg.LibraryRoots {
		if root == "" {
			continue
		}
		if _, err := os.Stat(root); err != nil && !os.IsNotExist(err) {
			diag.Add(diagnostics.NewWarning("failed to access library root for " + namespace + ": " + err.Error()))
		}
	}
	if cfg.DependencyRoots == nil {
		cfg.DependencyRoots = make(map[string]string)
	}
	globalScope := predeclaredScope()
	return &CompilerContext{
		Config:      cfg,
		Diagnostics: diag,
		GlobalScope: globalScope,
		Graph:       graph.New(GraphNodeModule, GraphEdgeImport),

		modules:   make(map[string]*Module),
		fileIndex: make(map[string]string),
	}
}

func libraryBaseDirFromExecutable() (string, bool) {
	exePath, err := os.Executable()
	if err != nil || exePath == "" {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil && resolved != "" {
		exePath = resolved
	}
	return packagedLibraryBaseForExecutable(exePath), true
}

func packagedLibraryBaseForExecutable(exePath string) string {
	if exePath == "" {
		return ""
	}
	return filepath.Clean(filepath.Join(filepath.Dir(exePath), PACKAGED_LIBS_DIR))
}

func (ctx *CompilerContext) LibraryRoot(namespace string) (string, bool) {
	if ctx == nil {
		return "", false
	}
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return "", false
	}
	if root, ok := ctx.Config.LibraryRoots[namespace]; ok && root != "" {
		return root, true
	}
	if ctx.Config.LibraryBaseDir == "" {
		return "", false
	}
	return filepath.Join(ctx.Config.LibraryBaseDir, filepath.FromSlash(namespace)), true
}

// ModuleOriginForFile classifies a source path against configured library roots.
// Bundled library files must keep stdlib identity even when opened directly,
// otherwise LSP can create a second local identity for the same physical file.
func (ctx *CompilerContext) ModuleOriginForFile(filePath string) (ModuleOrigin, string) {
	if ctx == nil {
		return ModuleOriginLocal, ""
	}
	canonical := CanonicalPath(filePath)
	if canonical == "" {
		return ModuleOriginLocal, ""
	}
	namespaces := make([]string, 0, len(ctx.Config.LibraryRoots))
	for namespace := range ctx.Config.LibraryRoots {
		namespaces = append(namespaces, namespace)
	}
	slices.Sort(namespaces)
	for _, namespace := range namespaces {
		root, ok := ctx.LibraryRoot(namespace)
		if !ok {
			continue
		}
		if PathWithinRoot(manifest.SourceDir(root), canonical) {
			return ModuleOriginStdlib, namespace
		}
	}
	if ctx.Config.LibraryBaseDir != "" {
		rel, err := filepath.Rel(CanonicalPath(ctx.Config.LibraryBaseDir), canonical)
		if err == nil {
			rel = filepath.ToSlash(rel)
			if rel == ".." || strings.HasPrefix(rel, "../") {
				return ModuleOriginLocal, ""
			}
			namespace, rest, ok := strings.Cut(rel, "/")
			if ok && namespace != "" && strings.HasPrefix(rest, peeper.SourceDirName+"/") {
				return ModuleOriginStdlib, namespace
			}
		}
	}
	return ModuleOriginLocal, ""
}

// Compiler-owned names available before prelude parsing.
func predeclaredScope() *table.Scope {
	scope := table.New(nil)
	declarePredeclaredConst(scope, "true")
	declarePredeclaredConst(scope, "false")
	declarePredeclaredConst(scope, "none")
	return scope
}

// Add one compiler-defined constant to the root scope.
func declarePredeclaredConst(scope *table.Scope, name string) {
	if scope == nil || name == "" {
		return
	}
	sym := symbols.New(name, symbols.SymbolConst, nil, ast.LocOf(nil))
	switch name {
	case "true", "false":
		sym.Type = &typeinfo.BoolType{}
	case "none":
		sym.Type = &typeinfo.NoneType{}
	default:
		sym.Type = &typeinfo.UnknownType{}
	}
	sym.IsPub = true
	if err := scope.Declare(sym); err != nil {
		// Predeclared constants should never fail to declare
		panic(err)
	}
}

type SemanticInfo struct {
	BlockScopes         map[ast.NodeID]*table.Scope
	ExprTypes           map[ast.NodeID]typeinfo.Type
	MethodSets          map[string][]*symbols.Symbol
	MethodSymbol        map[ast.NodeID]*symbols.Symbol
	DiscardBindingValue map[symbols.SymbolID]struct{}
}

func NewSemanticInfo() *SemanticInfo {
	return &SemanticInfo{
		BlockScopes:         make(map[ast.NodeID]*table.Scope),
		ExprTypes:           make(map[ast.NodeID]typeinfo.Type),
		MethodSets:          make(map[string][]*symbols.Symbol),
		MethodSymbol:        make(map[ast.NodeID]*symbols.Symbol),
		DiscardBindingValue: make(map[symbols.SymbolID]struct{}),
	}
}

func (m *Module) ResetSemanticData() {
	if m == nil {
		return
	}
	m.Semantics = NewSemanticInfo()
}
