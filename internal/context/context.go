package context

import (
	"path/filepath"
	"runtime"
	"sync"

	"compiler/core/diagnostics"
	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/analysis/semantics/table"
)

// Full compiler context.
// Stores the state of the compiler, including configuration, modules, and dependencies.
// All data is shared across the compiler's various phases like parsing, type checking, and code generation.

const STD_LIB_DEV = "ferret_libs_dev"

type ModuleOrigin string

const (
	ModuleOriginLocal      ModuleOrigin = "local"
	ModuleOriginStdlib     ModuleOrigin = "stdlib"
	ModuleOriginDependency ModuleOrigin = "dependency"
)

type ResolvedImport struct {
	Key             string
	ImportPath      string
	FilePath        string
	Origin          ModuleOrigin
	DependencyAlias string
}

type Module struct {
	Key          string
	ImportPath   string
	FilePath     string
	IsEntry      bool
	Origin       ModuleOrigin
	Dependency   string
	Content      string
	ContentHash  string

	Dependencies []string
}

type CompilerContext struct {
	Config      Config
	Diagnostics *diagnostics.DiagnosticBag
	GlobalScope *table.Scope
	
	modules      map[string]*Module
	fileIndex    map[string]string
	dependencies map[string]map[string]struct{}
	
	mu           sync.RWMutex
}

func New(rootDir, extension string, diag *diagnostics.DiagnosticBag) *CompilerContext {
	return nil
}


type Config struct {
	RootDir         string
	Extension       string
	StdlibRoot      string
	DependencyRoots map[string]string
	TargetOS        string
	TargetArch      string
	TargetBackend   string
	BuildDebug      bool
	TestMode        bool
	TestName        string
}

func NewWithConfig(cfg Config, diag *diagnostics.DiagnosticBag) *CompilerContext {
	if diag == nil {
		diag = diagnostics.NewDiagnosticBag("")
	}
	if cfg.Extension == "" {
		cfg.Extension = ".fer"
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
	if cfg.DependencyRoots == nil {
		cfg.DependencyRoots = make(map[string]string)
	}
	globalScope := predeclaredScope()
	return &CompilerContext{
		Config:       cfg,
		Diagnostics:  diag,
		GlobalScope:  globalScope,
		
		modules:      make(map[string]*Module),
		fileIndex:    make(map[string]string),
		dependencies: make(map[string]map[string]struct{}),
	}
}


func predeclaredScope() *table.Scope {
	scope := table.New(nil)
	declarePredeclaredConst(scope, "true")
	declarePredeclaredConst(scope, "false")
	declarePredeclaredConst(scope, "none")
	return scope
}

func declarePredeclaredConst(scope *table.Scope, name string) {
	if scope == nil || name == "" {
		return
	}
	sym := symbols.New(name, symbols.SymbolConst, nil)
	sym.IsPub = true
	_ = scope.Declare(sym)
}