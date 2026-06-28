package pipeline

import (
	"errors"
	"os"
	"path"
	"sync"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/graph"
	"compiler/internal/project"
)

type moduleLoader struct {
	ctx       *project.CompilerContext
	mu        sync.Mutex
	scheduled map[string]struct{}
	wg        sync.WaitGroup
}

func (l *moduleLoader) Load(entry *project.Module) error {
	if l == nil || l.ctx == nil {
		return errors.New("nil module loader")
	}
	if entry == nil {
		return errors.New("nil entry module")
	}
	l.enqueue(entry)
	l.wg.Wait()
	return nil
}

func (l *moduleLoader) enqueue(module *project.Module) {
	if l == nil || l.ctx == nil || module == nil {
		return
	}
	l.ensureModuleIdentity(module)
	if module.Key == "" {
		return
	}

	l.mu.Lock()
	if _, ok := l.scheduled[module.Key]; ok {
		l.mu.Unlock()
		return
	}
	l.scheduled[module.Key] = struct{}{}
	l.mu.Unlock()

	if existing, ok := l.ctx.ModuleByKey(module.Key); ok && existing != module {
		module = existing
	} else {
		l.ctx.AddModule(module)
	}

	l.wg.Add(1)
	go l.loadModule(module)
}

func (l *moduleLoader) ensureModuleIdentity(module *project.Module) {
	if module == nil || l == nil || l.ctx == nil {
		return
	}
	if module.Key == "" && module.FilePath != "" {
		module.Key = project.ModuleKeyFor(module.Origin, module.FilePath)
	}
	if module.ImportPath == "" && module.FilePath != "" {
		if importPath, err := l.ctx.ImportPathForFile(module.Origin, module.Namespace, module.FilePath); err == nil {
			module.ImportPath = importPath
		}
	}
}

func (l *moduleLoader) loadModule(module *project.Module) {
	defer l.wg.Done()
	if module == nil || l == nil {
		return
	}
	if module.AST != nil {
		if module.ImportFingerprint == "" {
			module.ImportFingerprint = module.AST.ImportFingerprint
		}
		if module.ExportFingerprint == "" {
			module.ExportFingerprint = module.AST.ExportFingerprint
		}
		if module.Phase < project.PhaseParsed {
			module.Phase = project.PhaseParsed
		}
		return
	}
	if module.Content == "" && module.FilePath != "" {
		content, err := os.ReadFile(module.FilePath)
		if err != nil {
			l.addImportError(nil, diagnostics.ErrModuleNotFound, "read module: "+err.Error())
			return
		}
		module.Content = string(content)
	}
	if l.ctx != nil && l.ctx.Diagnostics != nil && module.FilePath != "" {
		l.ctx.Diagnostics.AddSourceContent(module.FilePath, module.Content)
	}
	module.ContentHash = ast.HashText(module.Content)
	toks := lexer.New(module.FilePath, module.Content, l.ctx.Diagnostics).Tokenize()
	// Content is no longer needed after lexing; free the string.
	module.Content = ""
	module.AST = parser.New(module.FilePath, toks, l.ctx.Diagnostics).ParseModule()
	l.ctx.Metrics.AddParsedModule()
	module.ImportFingerprint = module.AST.ImportFingerprint
	module.ExportFingerprint = module.AST.ExportFingerprint
	module.Phase = project.PhaseParsed
	l.resolveImports(module)
}

func (l *moduleLoader) resolveImports(module *project.Module) {
	if module == nil || module.AST == nil {
		return
	}
	if module.Imports == nil {
		module.Imports = make(map[string]project.ResolvedImport)
	}
	for _, imp := range module.AST.Imports {
		rawPath, ok := ast.ImportPathFromDecl(imp)
		if !ok {
			l.addImportError(imp, diagnostics.ErrInvalidImportPath, "invalid import path")
			continue
		}
		resolved, err := l.ctx.ResolveImportPath(module, rawPath)
		if err != nil {
			l.addImportResolveError(imp, err)
			continue
		}
		alias := importAlias(imp, resolved.ImportPath)
		if alias == "" {
			l.addImportError(imp, diagnostics.ErrInvalidImportPath, "missing import alias")
			continue
		}
		if existing, ok := module.Imports[alias]; ok && existing.Key != resolved.Key {
			l.addImportError(imp, diagnostics.ErrAmbiguousImport, "import alias already in use")
			continue
		}
		resolvedImport := *resolved
		resolvedImport.Decl = imp
		module.Imports[alias] = resolvedImport
		if l.ctx.Graph != nil {
			l.ctx.Graph.AddEdge(graph.NodeID(module.Key), graph.NodeID(resolved.Key))
		}

		if existing, ok := l.ctx.ModuleByKey(resolved.Key); ok {
			l.enqueue(existing)
			continue
		}
		l.enqueue(&project.Module{
			Key:        resolved.Key,
			ImportPath: resolved.ImportPath,
			FilePath:   resolved.FilePath,
			Namespace:  resolved.Namespace,
			Origin:     resolved.Origin,
		})
	}
}

func (l *moduleLoader) addImportResolveError(imp *ast.ImportDecl, err error) {
	if l == nil {
		return
	}
	code := diagnostics.ErrInvalidImportPath
	msg := "invalid import path"
	if err != nil {
		msg = err.Error()
	}
	if impErr, ok := err.(*project.ImportError); ok {
		code = impErr.Code
		if impErr.Msg != "" {
			msg = impErr.Msg
		}
	}
	l.addImportError(imp, code, msg)
}

func (l *moduleLoader) addImportError(imp *ast.ImportDecl, code, msg string) {
	if l == nil || l.ctx == nil || l.ctx.Diagnostics == nil {
		return
	}
	d := diagnostics.NewError(msg).WithCode(code)
	if imp != nil {
		if loc := ast.LocOf(imp); loc != nil {
			d.WithPrimaryLabel(loc, msg)
		}
	}
	l.ctx.Diagnostics.Add(d)
}

func importAlias(imp *ast.ImportDecl, importPath string) string {
	if imp != nil && imp.Alias != nil && imp.Alias.Name != "" {
		return imp.Alias.Name
	}
	clean := path.Clean(importPath)
	base := path.Base(clean)
	if base == "." || base == "/" {
		return ""
	}
	return base
}
