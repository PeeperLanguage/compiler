package project

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/graph"
	"compiler/internal/ir"
	"compiler/internal/phase"
	"compiler/internal/semantics/intrinsics"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/target"
	"compiler/pkg/manifest"
	"compiler/pkg/peeper"
)

// Bundled libraries base directory relative to the installed compiler binary.
const PACKAGED_LIBS_DIR = "../libs"

// Shared state for one compilation.
type CompilerContext struct {
	// Normalized compiler options.
	Config Config
	// Immutable target metadata shared by semantic and backend phases.
	Target target.Info
	// Canonical runtime types shared by HIR, MIR, and backend lowering.
	Types *ir.TypeTable
	// Shared diagnostic stream.
	Diagnostics *diagnostics.DiagnosticBag
	// Highest project-wide checkpoint completed by the current pipeline run.
	// Module.Phase separately records reusable per-module artifact availability.
	CompletedProjectPhase phase.Phase
	// Optional per-run metrics for benchmarks and incremental validation.
	Metrics *CompileMetrics
	// Predeclared symbols visible before user/prelude code.
	GlobalScope *table.Scope

	// Module key -> module.
	modules map[string]*Module
	// Canonical file path -> module key.
	fileIndex map[string]string
	// Prior semantic API fingerprints supplied by incremental clients.
	semanticExportBaselines map[string]string
	// Shared compiler dependency graph.
	Graph *graph.Graph

	// Guards module indexes.
	mu *sync.RWMutex
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
	setupDiag := diag.BeginPhase(phase.Setup, "")
	if cfg.Extension == "" {
		cfg.Extension = peeper.SourceExt
	}
	if cfg.RootDir == "" {
		cfg.RootDir = "."
	}
	cfg.TargetOS = target.NormalizeOS(cfg.TargetOS)
	cfg.TargetArch = target.NormalizeArch(cfg.TargetArch)
	compilerTarget, err := target.New(cfg.TargetOS, cfg.TargetArch)
	if err != nil {
		setupDiag.Add(diagnostics.NewError("resolve compiler target: " + err.Error()))
		compilerTarget = target.Host()
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
			setupDiag.Add(diagnostics.NewWarning("failed to access packaged libraries root: " + err.Error()))
		}
	}
	for namespace, root := range cfg.LibraryRoots {
		if root == "" {
			continue
		}
		if _, err := os.Stat(root); err != nil && !os.IsNotExist(err) {
			setupDiag.Add(diagnostics.NewWarning("failed to access library root for " + namespace + ": " + err.Error()))
		}
	}
	if cfg.DependencyRoots == nil {
		cfg.DependencyRoots = make(map[string]string)
	}
	globalScope := predeclaredScope(compilerTarget)
	types := ir.NewTypeTable()
	types.SetIndexType(types.Intern(ir.Type{Kind: ir.TypeInteger, Bits: compilerTarget.IndexBits}))
	return &CompilerContext{
		Config:                cfg,
		Target:                compilerTarget,
		Types:                 types,
		Diagnostics:           diag,
		CompletedProjectPhase: phase.Setup,
		GlobalScope:           globalScope,
		Graph:                 graph.New(GraphNodeModule, GraphEdgeImport),
		mu:                    &sync.RWMutex{},

		modules:                 make(map[string]*Module),
		fileIndex:               make(map[string]string),
		semanticExportBaselines: make(map[string]string),
	}
}

// WithDiagnostics creates a phase-scoped context view sharing compiler state.
func (ctx *CompilerContext) WithDiagnostics(diag *diagnostics.DiagnosticBag) *CompilerContext {
	if ctx == nil {
		return nil
	}
	scoped := *ctx
	scoped.Diagnostics = diag
	return &scoped
}

// ResetModule invalidates module artifacts and downstream diagnostics together.
func (ctx *CompilerContext) ResetModule(module *Module, retained phase.Phase) {
	if ctx == nil || module == nil {
		return
	}
	module.resetToPhase(retained)
	if ctx.Diagnostics != nil && module.Key != "" {
		ctx.Diagnostics.DiscardModuleAfter(module.Key, retained)
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
func predeclaredScope(compilerTarget target.Info) *table.Scope {
	scope := table.New(nil)
	declarePredeclaredConst(scope, "true")
	declarePredeclaredConst(scope, "false")
	declarePredeclaredConst(scope, "none")
	declarePredeclaredType(scope, "Allocator", &typeinfo.AllocatorType{})

	for _, sym := range intrinsics.PredeclaredSymbols(compilerTarget) {
		if err := scope.Declare(sym); err != nil {
			panic(err)
		}
	}
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

func declarePredeclaredType(scope *table.Scope, name string, typ typeinfo.Type) {
	if scope == nil || name == "" || typ == nil {
		return
	}
	sym := symbols.New(name, symbols.SymbolType, nil, ast.LocOf(nil))
	sym.Type = typ
	sym.IsPub = true
	if err := scope.Declare(sym); err != nil {
		panic(err)
	}
}
