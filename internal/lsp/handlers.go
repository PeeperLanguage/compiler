package lsp

import (
	"compiler/internal/diagnostics"
	driver "compiler/internal/driver"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
	"fmt"
	"path/filepath"
	"strings"
)

type ServerState struct {
	RootDir   string
	Cache     map[string]string
	LastCtx   *project.CompilerContext
	workspace *workspaceIndex
	modules   map[string]*project.Module
}

func NewServerState() *ServerState {
	return &ServerState{
		Cache:   make(map[string]string),
		modules: make(map[string]*project.Module),
	}
}

func (s *ServerState) recompile(entryFile string) (*project.CompilerContext, *project.Module) {
	diagBag := diagnostics.NewDiagnosticBag()
	cfg := project.Config{
		RootDir: s.RootDir,
	}
	ctx := driver.NewContext(cfg, diagBag)

	rootDir := project.CanonicalPath(s.RootDir)
	if rootDir != "" {
		s.workspace = newWorkspaceIndex(rootDir)
		if err := s.workspace.rebuild(s.Cache); err == nil {
			dirtyFiles := s.workspace.dirtyFiles(entryFile, s.modules)
			s.seedReusableModules(ctx, dirtyFiles)
			for cachedPath, cachedContent := range s.Cache {
				driver.AddOverlay(ctx, cachedPath, cachedContent)
			}
			if virtualPath, content, ok := s.workspace.syntheticEntry(entryFile); ok {
				if driver.ParseFileWithOverlay(ctx, virtualPath, content) != nil {
					s.LastCtx = ctx
					s.captureModules(ctx)
					if mod, ok := ctx.ModuleByFile(entryFile); ok {
						return ctx, mod
					}
				}
			}
		}
	}

	absEntry, err := filepath.Abs(entryFile)
	for cachedPath, cachedContent := range s.Cache {
		absCached, err2 := filepath.Abs(cachedPath)
		if err2 != nil || (err == nil && absCached == absEntry) {
			continue
		}
		driver.AddOverlay(ctx, cachedPath, cachedContent)
	}

	content := s.Cache[entryFile]
	mod := driver.ParseFileWithOverlay(ctx, entryFile, content)
	s.LastCtx = ctx
	s.captureModules(ctx)
	return ctx, mod
}

func (s *ServerState) seedReusableModules(ctx *project.CompilerContext, dirtyFiles map[string]struct{}) {
	if s == nil || ctx == nil || len(s.modules) == 0 {
		return
	}
	reusePhases := map[string]project.ModulePhase{}
	if s.workspace != nil {
		reusePhases = s.workspace.reusePhases(firstDirtyFile(dirtyFiles), s.modules)
	}
	for filePath, module := range s.modules {
		if module == nil || module.FilePath == "" {
			continue
		}
		phase, ok := reusePhases[filePath]
		if !ok {
			continue
		}
		if strings.Contains(filePath, "/.peeper-lsp/") {
			continue
		}
		if phase == module.Phase {
			ctx.AddModule(module)
			continue
		}
		cloned := *module
		cloned.Phase = phase
		if phase <= project.PhaseParsed {
			cloned.ModuleScope = nil
			cloned.Semantics = nil
			cloned.HIR = nil
			cloned.MIR = nil
			cloned.LLVMIR = ""
		}
		ctx.AddModule(&cloned)
	}
}

func firstDirtyFile(dirtyFiles map[string]struct{}) string {
	for filePath := range dirtyFiles {
		return filePath
	}
	return ""
}

func (s *ServerState) captureModules(ctx *project.CompilerContext) {
	if s == nil || ctx == nil {
		return
	}
	if s.modules == nil {
		s.modules = make(map[string]*project.Module)
	}
	for _, module := range ctx.Modules() {
		if module == nil || module.FilePath == "" {
			continue
		}
		if strings.Contains(module.FilePath, "/.peeper-lsp/") {
			continue
		}
		if s.workspace != nil {
			if current := s.workspace.modules[module.FilePath]; current != nil {
				module.ContentHash = current.contentHash
				module.ImportFingerprint = current.importFingerprint
				module.ExportFingerprint = current.exportFingerprint
			}
		}
		s.modules[module.FilePath] = module
	}
}

func locContains(loc *source.Location, line, col int) bool {
	if loc == nil || loc.Start == nil || loc.End == nil {
		return false
	}
	if line < loc.Start.Line || (line == loc.Start.Line && col < loc.Start.Column) {
		return false
	}
	if line > loc.End.Line || (line == loc.End.Line && col > loc.End.Column) {
		return false
	}
	return true
}

func findNodeAt(module *project.Module, line, col int) ast.Node {
	var deepest ast.Node
	inspect := func(n ast.Node) bool {
		if n == nil {
			return true
		}
		if locContains(ast.LocOf(n), line, col) {
			deepest = n
			return true
		}
		return false
	}
	for _, imp := range module.AST.Imports {
		ast.Inspect(imp, inspect)
	}
	for _, stmt := range module.AST.Stmts {
		ast.Inspect(stmt, inspect)
	}
	return deepest
}

func buildParentMap(module *project.Module) map[ast.NodeID]ast.Node {
	parents := make(map[ast.NodeID]ast.Node)
	var stack []ast.Node
	inspect := func(n ast.Node) bool {
		if n == nil {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			return true
		}
		if len(stack) > 0 {
			parents[n.ID()] = stack[len(stack)-1]
		}
		stack = append(stack, n)
		return true
	}
	for _, imp := range module.AST.Imports {
		ast.Inspect(imp, inspect)
	}
	for _, stmt := range module.AST.Stmts {
		ast.Inspect(stmt, inspect)
	}
	return parents
}

func (s *ServerState) resolveSymbolAt(filePath string, line, col int) (*symbols.Symbol, ast.Node, *project.Module, map[ast.NodeID]ast.Node) {
	ctx, mod := s.recompile(filePath)
	if mod == nil || mod.AST == nil {
		return nil, nil, nil, nil
	}

	node := findNodeAt(mod, line, col)
	if node == nil {
		return nil, nil, nil, nil
	}

	parents := buildParentMap(mod)

	ident, ok := node.(*ast.Ident)
	if !ok || ident == nil {
		return nil, node, mod, parents
	}

	sym := resolveIdentSymbol(ident, parents, mod, ctx)
	return sym, node, mod, parents
}

func resolveIdentSymbol(ident *ast.Ident, parents map[ast.NodeID]ast.Node, module *project.Module, ctx *project.CompilerContext) *symbols.Symbol {
	if ident == nil {
		return nil
	}
	parent := parents[ident.ID()]
	if parent == nil {
		if sym, ok := module.ModuleScope.Lookup(ident.Name); ok {
			return sym
		}
		return nil
	}

	// 1. Check if it's a struct field or method selector
	if sel, ok := parent.(*ast.SelectorExpr); ok && sel.Name == ident {
		baseType := module.Semantics.ExprTypes[sel.Expr.ID()]
		if baseType != nil {
			if ptr, ok := baseType.(*typeinfo.RawPtrType); ok && ptr.Target != nil {
				baseType = ptr.Target
			}
			if defined, ok := baseType.(*typeinfo.DefinedType); ok && defined.Underlying != nil {
				baseType = defined.Underlying
			}
			baseTypeName := baseType.Text()

			// Try struct field first
			var structDecl *ast.StructDecl
			for _, m := range ctx.Modules() {
				if m.AST == nil {
					continue
				}
				for _, d := range m.AST.Stmts {
					if sd, ok := d.(*ast.StructDecl); ok && sd != nil && sd.Name != nil && sd.Name.Name == baseTypeName {
						structDecl = sd
						break
					}
				}
				if structDecl != nil {
					break
				}
			}
			if structDecl != nil {
				structType, ok := structDecl.Type.(*ast.StructType)
				if !ok || structType == nil {
					return nil
				}
				for _, f := range structType.Fields {
					if f.Name != nil && f.Name.Name == ident.Name {
						fieldSym := symbols.New(ident.Name, symbols.SymbolField, f.Name, ast.LocOf(f.Name))
						return fieldSym
					}
				}
			}

			// Try method set
			keys := []string{typeinfo.TypeText(baseType)}
			if ptr, ok := baseType.(*typeinfo.RawPtrType); ok && ptr.Target != nil {
				keys = append(keys, typeinfo.TypeText(ptr.Target))
			}
			for _, key := range keys {
				if methods, ok := module.Semantics.MethodSets[key]; ok {
					for _, m := range methods {
						if m != nil && m.Name == ident.Name {
							return m
						}
					}
				}
			}
		}
		return nil
	}

	// 2. Check if it's a scope resolution member (M::x)
	if sr, ok := parent.(*ast.ScopeResolution); ok && sr.Name == ident {
		qualifier := sr.Module.Name
		if imp, ok := module.Imports[qualifier]; ok {
			if mod, ok := ctx.ModuleByKey(imp.Key); ok && mod.ModuleScope != nil {
				if sym, ok := mod.ModuleScope.LookupLocal(ident.Name); ok {
					return sym
				}
			}
		}
		return nil
	}

	// 3. Check if it's a scope resolution qualifier (M::x)
	if sr, ok := parent.(*ast.ScopeResolution); ok && sr.Module == ident {
		qualifier := ident.Name
		if imp, ok := module.Imports[qualifier]; ok {
			sym := symbols.New(ident.Name, symbols.SymbolImport, parent, ast.LocOf(ident))
			sym.Location = &source.Location{
				Filename: &imp.FilePath,
			}
			return sym
		}
		return nil
	}

	// 4. Resolve in local block/function scopes
	var scope *table.Scope
	curr := parent
	for curr != nil {
		if block, ok := curr.(*ast.BlockStmt); ok {
			if s, ok := module.Semantics.BlockScopes[block.ID()]; ok && s != nil {
				scope = s
				break
			}
		}
		curr = parents[curr.ID()]
	}
	if scope == nil {
		curr = parent
		var containingFn *ast.FnDecl
		for curr != nil {
			if fn, ok := curr.(*ast.FnDecl); ok {
				containingFn = fn
				break
			}
			curr = parents[curr.ID()]
		}
		if containingFn != nil {
			if sym, ok := module.ModuleScope.Lookup(containingFn.Name.Name); ok && sym != nil && sym.Scope != nil {
				if fs, ok := sym.Scope.(*table.Scope); ok {
					scope = fs
				}
			}
		}
	}
	if scope == nil {
		scope = module.ModuleScope
	}
	if scope != nil {
		if sym, ok := scope.Lookup(ident.Name); ok && sym != nil {
			return sym
		}
	}
	return nil
}

func symLocationsMatch(l1, l2 *source.Location) bool {
	if l1 == nil || l2 == nil {
		return l1 == l2
	}
	if l1.Filename == nil || l2.Filename == nil {
		return l1.Filename == l2.Filename
	}
	if *l1.Filename != *l2.Filename {
		return false
	}
	if l1.Start == nil || l2.Start == nil || l1.End == nil || l2.End == nil {
		return false
	}
	return l1.Start.Line == l2.Start.Line && l1.Start.Column == l2.Start.Column
}

func (s *ServerState) HandleHover(params HoverParams) (*Hover, error) {
	path := uriToPath(string(params.TextDocument.URI))
	sym, node, _, _ := s.resolveSymbolAt(path, params.Position.Line+1, params.Position.Character+1)
	if sym == nil || node == nil {
		return nil, nil
	}

	text := fmt.Sprintf("(%s) %s", sym.Kind, sym.Name)
	if sym.Type != nil {
		text += ": " + sym.Type.Text()
	}

	loc := ast.LocOf(node)
	var hoverRange *Range
	if loc != nil && loc.Start != nil && loc.End != nil {
		hoverRange = &Range{
			Start: Position{Line: loc.Start.Line - 1, Character: loc.Start.Column - 1},
			End:   Position{Line: loc.End.Line - 1, Character: loc.End.Column - 1},
		}
	}

	return &Hover{
		Contents: MarkupContent{
			Kind:  "markdown",
			Value: fmt.Sprintf("```peeper\n%s\n```", text),
		},
		Range: hoverRange,
	}, nil
}

func (s *ServerState) HandleDefinition(params DefinitionParams) ([]Location, error) {
	path := uriToPath(string(params.TextDocument.URI))
	sym, _, _, _ := s.resolveSymbolAt(path, params.Position.Line+1, params.Position.Character+1)
	if sym == nil || sym.Location == nil || sym.Location.Start == nil || sym.Location.End == nil || sym.Location.Filename == nil {
		return nil, nil
	}

	return []Location{
		{
			URI: DocumentURI(pathToURI(*sym.Location.Filename)),
			Range: Range{
				Start: Position{Line: sym.Location.Start.Line - 1, Character: sym.Location.Start.Column - 1},
				End:   Position{Line: sym.Location.End.Line - 1, Character: sym.Location.End.Column - 1},
			},
		},
	}, nil
}

func (s *ServerState) HandleRename(params RenameParams) (*WorkspaceEdit, error) {
	path := uriToPath(string(params.TextDocument.URI))
	targetSym, _, _, _ := s.resolveSymbolAt(path, params.Position.Line+1, params.Position.Character+1)
	if targetSym == nil || targetSym.Location == nil {
		return nil, nil
	}

	changes := make(map[DocumentURI][]TextEdit)

	if s.LastCtx == nil {
		return nil, nil
	}

	for _, mod := range s.LastCtx.Modules() {
		if mod.AST == nil {
			continue
		}
		parents := buildParentMap(mod)

		inspect := func(n ast.Node) bool {
			if n == nil {
				return true
			}
			if ident, ok := n.(*ast.Ident); ok && ident.Name == targetSym.Name {
				resolved := resolveIdentSymbol(ident, parents, mod, s.LastCtx)
				if resolved != nil && symLocationsMatch(resolved.Location, targetSym.Location) {
					uri := DocumentURI(pathToURI(mod.FilePath))
					loc := ast.LocOf(ident)
					if loc != nil && loc.Start != nil && loc.End != nil {
						changes[uri] = append(changes[uri], TextEdit{
							Range: Range{
								Start: Position{Line: loc.Start.Line - 1, Character: loc.Start.Column - 1},
								End:   Position{Line: loc.End.Line - 1, Character: loc.End.Column - 1},
							},
							NewText: params.NewName,
						})
					}
				}
			}
			return true
		}

		for _, imp := range mod.AST.Imports {
			ast.Inspect(imp, inspect)
		}
		for _, stmt := range mod.AST.Stmts {
			ast.Inspect(stmt, inspect)
		}
	}

	return &WorkspaceEdit{Changes: changes}, nil
}
