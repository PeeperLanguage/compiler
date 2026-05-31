package pipeline

import (
	"errors"
	"os"
	"path"
	"sync"

	"compiler/core/diagnostics"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
)

type moduleLoader struct {
	ctx       *context.CompilerContext
	mu        sync.Mutex
	scheduled map[string]struct{}
	wg        sync.WaitGroup
}

func newModuleLoader(ctx *context.CompilerContext) *moduleLoader {
	return &moduleLoader{
		ctx:       ctx,
		scheduled: make(map[string]struct{}),
	}
}

func (l *moduleLoader) Load(entry *context.Module) error {
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

func (l *moduleLoader) enqueue(module *context.Module) {
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

func (l *moduleLoader) ensureModuleIdentity(module *context.Module) {
	if module == nil || l == nil || l.ctx == nil {
		return
	}
	if module.Key == "" && module.FilePath != "" {
		module.Key = context.ModuleKeyFor(module.Origin, module.FilePath)
	}
	if module.ImportPath == "" && module.FilePath != "" {
		if importPath, err := l.ctx.ImportPathForFile(module.Origin, module.FilePath); err == nil {
			module.ImportPath = importPath
		}
	}
}

func (l *moduleLoader) loadModule(module *context.Module) {
	defer l.wg.Done()
	if module == nil || l == nil {
		return
	}
	if module.AST != nil {
		return
	}
	if module.Content == "" && module.FilePath != "" {
		content, err := os.ReadFile(module.FilePath)
		if err != nil {
			l.addModuleError(module, diagnostics.ErrModuleNotFound, "read module: "+err.Error())
			return
		}
		module.Content = string(content)
	}
	if l.ctx != nil && l.ctx.Diagnostics != nil && module.FilePath != "" {
		l.ctx.Diagnostics.AddSourceContent(module.FilePath, module.Content)
	}
	module.Tokens = lexer.Lex(module.FilePath, module.Content, l.ctx.Diagnostics)
	module.AST = parser.ParseModule(module.FilePath, module.Tokens, l.ctx.Diagnostics)
	l.resolveImports(module)
}

func (l *moduleLoader) resolveImports(module *context.Module) {
	if module == nil || module.AST == nil {
		return
	}
	if module.Imports == nil {
		module.Imports = make(map[string]context.ResolvedImport)
	}
	for _, imp := range module.AST.Imports {
		rawPath, ok := importPathFromDecl(imp)
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
		module.Imports[alias] = *resolved
		module.Dependencies = appendUnique(module.Dependencies, resolved.Key)
		l.ctx.AddDependency(module.Key, resolved.Key)

		if existing, ok := l.ctx.ModuleByKey(resolved.Key); ok {
			l.enqueue(existing)
			continue
		}
		l.enqueue(&context.Module{
			Key:        resolved.Key,
			ImportPath: resolved.ImportPath,
			FilePath:   resolved.FilePath,
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
	if impErr, ok := err.(*context.ImportError); ok {
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
		if loc := imp.Loc(); loc != nil {
			d.WithPrimaryLabel(loc, msg)
		}
	}
	l.ctx.Diagnostics.Add(d)
}

func (l *moduleLoader) addModuleError(module *context.Module, code, msg string) {
	if l == nil || l.ctx == nil || l.ctx.Diagnostics == nil {
		return
	}
	d := diagnostics.NewError(msg).WithCode(code)
	l.ctx.Diagnostics.Add(d)
}

func importPathFromDecl(imp *ast.ImportDecl) (string, bool) {
	if imp == nil || imp.Path == nil {
		return "", false
	}
	switch node := imp.Path.(type) {
	case *ast.StringLit:
		return node.Value, true
	case *ast.Ident:
		return node.Name, true
	default:
		return "", false
	}
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

func appendUnique(list []string, value string) []string {
	for _, item := range list {
		if item == value {
			return list
		}
	}
	return append(list, value)
}
