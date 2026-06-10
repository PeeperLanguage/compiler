package project

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir/hir"
	"compiler/internal/ir/mir"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
)

// Development-time standard library directory.
const STD_LIB_DEV = "_builtin_library"

// Where a module was loaded from.
type ModuleOrigin string

const (
	// Project source file.
	ModuleOriginLocal ModuleOrigin = "local"
	// Standard library source file.
	ModuleOriginStdlib ModuleOrigin = "core"
	// Package dependency source file.
	ModuleOriginDependency ModuleOrigin = "dependency"
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

	// Outgoing module graph keys.
	Dependencies []string
}

// Shared state for one compilation.
type CompilerContext struct {
	// Normalized compiler options.
	Config Config
	// Shared diagnostic stream.
	Diagnostics *diagnostics.DiagnosticBag
	// Predeclared symbols visible before user/prelude code.
	GlobalScope *table.Scope

	// Module key -> module.
	modules map[string]*Module
	// Canonical file path -> module key.
	fileIndex map[string]string
	// Module graph edges.
	dependencies map[string]map[string]struct{}

	// Guards module and dependency indexes.
	mu sync.RWMutex
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
	// Source file extension.
	Extension string
	// Standard library root.
	StdlibRoot string
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
		diag = diagnostics.NewDiagnosticBag("")
	}
	if cfg.Extension == "" {
		cfg.Extension = ".em"
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
	if cfg.StdlibRoot == "" {
		if cwd, err := os.Getwd(); err == nil {
			candidate := filepath.Join(cwd, STD_LIB_DEV)
			if _, err := os.Stat(candidate); err == nil {
				cfg.StdlibRoot = candidate
			}
		}
		if cfg.StdlibRoot == "" {
			cfg.StdlibRoot = filepath.Join(cfg.RootDir, STD_LIB_DEV)
		}
	}
	cfg.StdlibRoot = filepath.Clean(cfg.StdlibRoot)
	if !filepath.IsAbs(cfg.StdlibRoot) {
		if abs, err := filepath.Abs(cfg.StdlibRoot); err == nil {
			cfg.StdlibRoot = abs
		}
	}
	if _, err := os.Stat(cfg.StdlibRoot); err != nil && !os.IsNotExist(err) {
		diag.Add(diagnostics.NewWarning("failed to access stdlib root: " + err.Error()))
	}
	if cfg.DependencyRoots == nil {
		cfg.DependencyRoots = make(map[string]string)
	}
	globalScope := predeclaredScope()
	return &CompilerContext{
		Config:      cfg,
		Diagnostics: diag,
		GlobalScope: globalScope,

		modules:      make(map[string]*Module),
		fileIndex:    make(map[string]string),
		dependencies: make(map[string]map[string]struct{}),
	}
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
	sym := symbols.New(name, symbols.SymbolConst, nil)
	switch name {
	case "true", "false":
		sym.Type = &typeinfo.BoolType{}
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
