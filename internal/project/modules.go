package project

import (
	"path/filepath"
	"strings"

	"compiler/internal/graph"
)

const (
	GraphNodeModule graph.NodeKind = "module"
	GraphEdgeImport graph.EdgeKind = "import"
)

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
