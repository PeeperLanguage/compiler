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
	"compiler/internal/moduleid"
	"compiler/internal/phase"
	"compiler/internal/project"
)

type moduleLoader struct {
	ctx       *project.CompilerContext
	mu        sync.Mutex
	scheduled map[moduleid.ID]struct{}
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
	if !module.ID.Valid() {
		return
	}

	l.mu.Lock()
	if _, ok := l.scheduled[module.ID]; ok {
		l.mu.Unlock()
		return
	}
	l.scheduled[module.ID] = struct{}{}
	l.mu.Unlock()

	if existing, ok := l.ctx.ModuleByID(module.ID); ok {
		module = existing
	} else {
		l.ctx.AddModule(module)
	}

	l.wg.Add(1)
	go l.loadModule(module)
}

func (l *moduleLoader) loadModule(module *project.Module) {
	defer l.wg.Done()
	if module == nil || l == nil {
		return
	}
	loadDiag := l.ctx.Diagnostics.BeginPhase(phase.Load, module.ID.String())
	if module.AST != nil {
		if module.ImportFingerprint == "" {
			module.ImportFingerprint = module.AST.ImportFingerprint
		}
		if module.ExportFingerprint == "" {
			module.ExportFingerprint = module.AST.ExportFingerprint
		}
		if module.Phase < phase.Parsed {
			l.ctx.ResetModule(module, phase.Parsed)
		}
		l.resolveImports(module, loadDiag)
		return
	}
	if !module.ContentProvided && module.Content == "" && module.FilePath != "" {
		content, err := os.ReadFile(module.FilePath)
		if err != nil {
			l.addImportError(loadDiag, nil, diagnostics.ErrModuleNotFound, "read module: "+err.Error())
			return
		}
		module.Content = string(content)
		module.ContentProvided = true
	}
	if l.ctx != nil && l.ctx.Diagnostics != nil && module.FilePath != "" {
		l.ctx.Diagnostics.AddSourceContent(module.FilePath, module.Content)
	}
	module.ContentHash = ast.HashText(module.Content)
	parseDiag := l.ctx.Diagnostics.BeginPhase(phase.Parsed, module.ID.String())
	toks := lexer.New(module.FilePath, module.Content, parseDiag).Tokenize()
	// Content is no longer needed after lexing; free the string.
	module.Content = ""
	module.AST = parser.New(module.FilePath, toks, parseDiag).ParseModule()
	l.ctx.Metrics.AddParsedModule()
	module.ImportFingerprint = module.AST.ImportFingerprint
	module.ExportFingerprint = module.AST.ExportFingerprint
	l.ctx.ResetModule(module, phase.Parsed)
	l.resolveImports(module, loadDiag)
}

func (l *moduleLoader) resolveImports(module *project.Module, diag *diagnostics.DiagnosticBag) {
	if module == nil || module.AST == nil {
		return
	}
	if module.Imports == nil {
		module.Imports = make(map[string]project.ResolvedImport)
	}
	for _, imp := range module.AST.Imports {
		rawPath, ok := ast.ImportPathFromDecl(imp)
		if !ok {
			l.addImportError(diag, imp, diagnostics.ErrInvalidImportPath, "invalid import path")
			continue
		}
		resolved, err := l.ctx.ResolveImportPath(rawPath)
		if err != nil {
			l.addImportResolveError(diag, imp, err)
			continue
		}
		alias := importAlias(imp, resolved.ID.ImportPath)
		if alias == "" {
			l.addImportError(diag, imp, diagnostics.ErrInvalidImportPath, "missing import alias")
			continue
		}
		if existing, ok := module.Imports[alias]; ok && existing.ID != resolved.ID {
			l.addImportError(diag, imp, diagnostics.ErrAmbiguousImport, "import alias already in use")
			continue
		}
		resolvedImport := *resolved
		resolvedImport.Decl = imp
		module.Imports[alias] = resolvedImport
		if l.ctx.Graph != nil {
			l.ctx.Graph.AddEdge(graph.NodeID(module.ID.String()), graph.NodeID(resolved.ID.String()))
		}

		if existing, ok := l.ctx.ModuleByID(resolved.ID); ok {
			l.enqueue(existing)
			continue
		}
		l.enqueue(&project.Module{ID: resolved.ID, FilePath: resolved.FilePath})
	}
}

func (l *moduleLoader) addImportResolveError(diag *diagnostics.DiagnosticBag, imp *ast.ImportDecl, err error) {
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
	l.addImportError(diag, imp, code, msg)
}

func (l *moduleLoader) addImportError(diag *diagnostics.DiagnosticBag, imp *ast.ImportDecl, code, msg string) {
	if l == nil || diag == nil {
		return
	}
	d := diagnostics.NewError(msg).WithCode(code)
	if imp != nil {
		if loc := ast.LocOf(imp); loc != nil {
			d.WithPrimaryLabel(loc, msg)
		}
	}
	diag.Add(d)
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
