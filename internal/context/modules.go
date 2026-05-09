package context

import "path/filepath"

func canonicalPath(path string) string {
	if path == "" {
		return ""
	}
	clean := filepath.Clean(path)
	if abs, err := filepath.Abs(clean); err == nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(clean)
}

func (ctx *CompilerContext) UpsertModule(module *Module) {
	if ctx == nil || module == nil || module.Key == "" {
		return
	}
	ctx.mu.Lock()
	defer ctx.mu.Unlock()

	ctx.modules[module.Key] = module
	if module.FilePath != "" {
		ctx.fileIndex[canonicalPath(module.FilePath)] = module.Key
	}
}

func (ctx *CompilerContext) ModuleByKey(key string) (*Module, bool) {
	if ctx == nil || key == "" {
		return nil, false
	}
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()
	module, ok := ctx.modules[key]
	return module, ok
}

func (ctx *CompilerContext) ModuleByFile(filePath string) (*Module, bool) {
	if ctx == nil || filePath == "" {
		return nil, false
	}
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()
	key, ok := ctx.fileIndex[canonicalPath(filePath)]
	if !ok {
		return nil, false
	}
	module, ok := ctx.modules[key]
	return module, ok
}

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

func (ctx *CompilerContext) AddDependency(fromKey, toKey string) {
	if ctx == nil || fromKey == "" || toKey == "" {
		return
	}
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	edges, ok := ctx.dependencies[fromKey]
	if !ok {
		edges = make(map[string]struct{})
		ctx.dependencies[fromKey] = edges
	}
	edges[toKey] = struct{}{}
}

func (ctx *CompilerContext) DependenciesOf(moduleKey string) []string {
	if ctx == nil || moduleKey == "" {
		return nil
	}
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()
	edges, ok := ctx.dependencies[moduleKey]
	if !ok {
		return nil
	}
	deps := make([]string, 0, len(edges))
	for key := range edges {
		deps = append(deps, key)
	}
	return deps
}
