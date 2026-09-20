package project

import (
	"fmt"
	"path/filepath"
	"strings"

	"compiler/internal/constvalue"
	"compiler/internal/diagnostics"
	"compiler/internal/graph"
	compilation "compiler/internal/module"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/symbols"
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
func (ctx *CompilerContext) NewModuleForFile(filePath, content string) *compilation.Module {
	if ctx == nil || filePath == "" {
		return nil
	}
	origin, namespace := ctx.ModuleOriginForFile(filePath)
	id, err := ctx.IdentityForFile(origin, namespace, filePath)
	if err != nil {
		return nil
	}
	return &compilation.Module{
		ID:              id,
		FilePath:        filePath,
		Content:         content,
		ContentProvided: true,
	}
}

// AddModule registers a module in shared compiler state. Identity conflict
// validation, file indexing, and retained type-declaration reindexing are one
// atomic operation so rejected or replaced modules cannot leave stale indexes.
func (ctx *CompilerContext) AddModule(module *compilation.Module) *diagnostics.Diagnostic {
	if ctx == nil || module == nil || !module.ID.Valid() {
		return nil
	}
	module.FilePath = CanonicalPath(module.FilePath)
	ctx.mu.Lock()

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
		if ctx.Diagnostics == nil {
			return nil
		}
		return ctx.Diagnostics.AddError(diagnostics.ErrAmbiguousImport, conflict, nil, "")
	}

	previous := ctx.modules[module.ID]
	ctx.modules[module.ID] = module
	if module.FilePath != "" {
		ctx.fileIndex[module.FilePath] = module.ID
	} else if previous != nil && previous.FilePath != "" && ctx.fileIndex[previous.FilePath] == module.ID {
		delete(ctx.fileIndex, previous.FilePath)
	}

	// Fresh LSP/compiler contexts re-register retained module snapshots. Their
	// collected declarations are authoritative artifacts; this derived index must
	// therefore be rebuilt even when collection does not run again.
	if ctx.TypeResolver != nil {
		ctx.TypeResolver.RegisterModule(module)
	}
	ctx.mu.Unlock()
	return nil
}

// PublishedConstant returns the authoritative value of a constant symbol,
// resolving symbols owned by another module through their defining identity.
// Query-cache entries are excluded because only published module values are
// stable enough for cross-module reads and export fingerprints.
func (ctx *CompilerContext) PublishedConstant(module *compilation.Module, sym *symbols.Symbol) constvalue.Value {
	if sym == nil {
		return nil
	}
	owner := module
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
	return owner.Constants.Published(sym.ID)
}

// ModuleByID resolves canonical module identity.
func (ctx *CompilerContext) ModuleByID(id moduleid.ID) (*compilation.Module, bool) {
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

// ModuleByFile resolves a module by canonical source path.
func (ctx *CompilerContext) ModuleByFile(filePath string) (*compilation.Module, bool) {
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

// Modules returns a snapshot of registered modules.
func (ctx *CompilerContext) Modules() []*compilation.Module {
	if ctx == nil {
		return nil
	}
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()
	modules := make([]*compilation.Module, 0, len(ctx.modules))
	for _, module := range ctx.modules {
		modules = append(modules, module)
	}
	return modules
}
