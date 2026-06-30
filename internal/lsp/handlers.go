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
	"compiler/pkg/manifest"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// cursorContext keeps one cursor lookup result: deepest AST node, parent links,
// and the compiled module snapshot it came from. That lets hover/definition/
// rename share one AST walk instead of rebuilding parent maps separately.
type cursorContext struct {
	ctx     *project.CompilerContext
	module  *project.Module
	node    ast.Node
	line    int
	col     int
	parents map[ast.NodeID]ast.Node
}

type hoverSubjectKind int

const (
	hoverSubjectSymbol hoverSubjectKind = iota
	hoverSubjectExpr
	hoverSubjectType
	hoverSubjectDecl
	hoverSubjectImport
	hoverSubjectAttribute
)

// hoverSubject is the normalized cursor target after resolution. It hides how
// the cursor was found so the renderer can stay flat and data-driven.
type hoverSubject struct {
	Kind           hoverSubjectKind
	Node           ast.Node
	Range          Range
	Symbol         *symbols.Symbol
	ExprType       typeinfo.Type
	ResolvedType   typeinfo.Type
	Decl           ast.Node
	ResolvedImport *project.ResolvedImport
	Attribute      *ast.Attribute
	MethodSymbols  []*symbols.Symbol
}

type ServerState struct {
	mu          sync.Mutex
	diagWG      sync.WaitGroup
	RootDir     string
	Cache       map[string]string
	LastCtx     *project.CompilerContext
	LastMetrics project.CompileMetrics
	workspace   *workspaceIndex
	modules     map[string]*project.Module
	diagVersion map[string]uint64
}

func NewServerState() *ServerState {
	return &ServerState{
		Cache:       make(map[string]string),
		modules:     make(map[string]*project.Module),
		diagVersion: make(map[string]uint64),
	}
}

func (s *ServerState) recompile(entryFile string) (*project.CompilerContext, *project.Module) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recompileLocked(entryFile)
}

func (s *ServerState) recompileLocked(entryFile string) (*project.CompilerContext, *project.Module) {
	diagBag := diagnostics.NewDiagnosticBag()
	rootDir := filepath.Dir(entryFile)
	projectName := ""
	if loadedProject, err := manifest.LoadProject(entryFile); err == nil {
		rootDir = loadedProject.RootDir
		projectName = loadedProject.File.Package.Name
	}
	cfg := project.Config{
		RootDir:     rootDir,
		ProjectName: projectName,
	}
	ctx := driver.NewContext(cfg, diagBag)
	ctx.Metrics = &project.CompileMetrics{}
	if projectName != "" && !manifest.PathWithinSourceDir(rootDir, entryFile) {
		ctx.Diagnostics.Add(diagnostics.NewError(
			fmt.Sprintf("project source files must stay under %s", manifest.SourceDir(rootDir)),
		))
		s.LastCtx = ctx
		s.LastMetrics = ctx.Metrics.Snapshot()
		return ctx, nil
	}

	rootDir = project.CanonicalPath(s.RootDir)
	if rootDir != "" {
		s.workspace = newWorkspaceIndex(rootDir)
		if err := s.workspace.rebuild(s.Cache); err == nil {
			dirtyFiles := s.workspace.dirtyFiles(entryFile, s.modules)
			ctx.Metrics.AddDirtyFiles(len(dirtyFiles))
			s.seedReusableModules(ctx, dirtyFiles)
			for cachedPath, cachedContent := range s.Cache {
				driver.AddOverlay(ctx, cachedPath, cachedContent)
			}
			if virtualPath, content, ok := s.workspace.syntheticEntry(entryFile); ok {
				if driver.ParseFileWithOverlay(ctx, virtualPath, content) != nil {
					s.LastCtx = ctx
					s.LastMetrics = ctx.Metrics.Snapshot()
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
	s.LastMetrics = ctx.Metrics.Snapshot()
	s.captureModules(ctx)
	return ctx, mod
}

func (s *ServerState) currentCompiledModule(filePath string) (*project.CompilerContext, *project.Module) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.LastCtx != nil {
		canonical := project.CanonicalPath(filePath)
		if canonical != "" {
			if mod, ok := s.LastCtx.ModuleByFile(canonical); ok && mod != nil {
				// Overlays for other open files register placeholder modules in the
				// context before they are parsed. Hover/definition/rename must not
				// reuse those stubs even if their content hash matches the buffer.
				if mod.AST == nil || mod.Phase < project.PhaseParsed {
					return s.recompileLocked(filePath)
				}
				// Reuse the last compiled snapshot only when the current buffer text
				// still matches it. Otherwise hover/definition/rename would keep
				// reading a frozen AST after edits until some later path recompiles.
				if content, err := workspaceContent(canonical, s.Cache); err == nil && mod.ContentHash == ast.HashText(content) {
					return s.LastCtx, mod
				}
			}
		}
	}
	return s.recompileLocked(filePath)
}

func (s *ServerState) scheduleDiagnosticRefresh(filePath string, delay time.Duration, publish func()) {
	if s == nil || publish == nil {
		return
	}
	filePath = project.CanonicalPath(filePath)
	s.mu.Lock()
	s.diagVersion[filePath]++
	version := s.diagVersion[filePath]
	s.mu.Unlock()

	s.diagWG.Go(func() {
		// Full-sync edits arrive as whole-file snapshots. Delay diagnostics so a
		// burst of keystrokes collapses into one recompile instead of one per edit.
		time.Sleep(delay)
		s.mu.Lock()
		if s.diagVersion[filePath] != version {
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		publish()
	})
}

func (s *ServerState) waitForScheduledDiagnostics() {
	if s == nil {
		return
	}
	s.diagWG.Wait()
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
			ctx.Metrics.AddReusedModule()
			ctx.AddModule(module)
			continue
		}
		cloned := *module
		cloned.Phase = phase
		ctx.Metrics.AddReusedModule()
		if phase < module.Phase {
			ctx.Metrics.AddDowngradedModule()
		}
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
		if module == nil || module.FilePath == "" || module.AST == nil || module.Phase < project.PhaseParsed {
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
		if existing := s.modules[module.FilePath]; existing != nil &&
			existing.Origin == project.ModuleOriginStdlib &&
			module.Origin == project.ModuleOriginLocal {
			continue
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

func walkModuleAST(module *project.Module, visit func(ast.Node, ast.Node) bool) {
	if module == nil || module.AST == nil || visit == nil {
		return
	}
	var stack []ast.Node
	inspect := func(n ast.Node) bool {
		if n == nil {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			return true
		}
		var parent ast.Node
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		if !visit(n, parent) {
			return false
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
}

func buildCursorContext(ctx *project.CompilerContext, module *project.Module, line, col int) *cursorContext {
	if ctx == nil || module == nil || module.AST == nil {
		return nil
	}
	cc := &cursorContext{
		ctx:     ctx,
		module:  module,
		line:    line,
		col:     col,
		parents: make(map[ast.NodeID]ast.Node),
	}
	walkModuleAST(module, func(n ast.Node, parent ast.Node) bool {
		if parent != nil {
			cc.parents[n.ID()] = parent
		}
		if locContains(ast.LocOf(n), line, col) {
			cc.node = n
			return true
		}
		return false
	})
	return cc
}

func buildParentMap(module *project.Module) map[ast.NodeID]ast.Node {
	parents := make(map[ast.NodeID]ast.Node)
	walkModuleAST(module, func(n ast.Node, parent ast.Node) bool {
		if parent != nil {
			parents[n.ID()] = parent
		}
		return true
	})
	return parents
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
		if sym := resolveSelectorMemberSymbol(sel, ident, parents, module, ctx); sym != nil {
			return sym
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

func resolveSelectorMemberSymbol(sel *ast.SelectorExpr, ident *ast.Ident, parents map[ast.NodeID]ast.Node, module *project.Module, ctx *project.CompilerContext) *symbols.Symbol {
	if sel == nil || ident == nil || module == nil || ctx == nil || module.Semantics == nil {
		return nil
	}
	baseType, ok := selectorBaseType(sel.Expr, parents, module, ctx)
	if !ok || baseType == nil {
		return nil
	}
	if fieldSym := lookupStructFieldSymbol(baseType, ident.Name, ctx); fieldSym != nil {
		return fieldSym
	}
	for _, key := range selectorMethodKeys(baseType) {
		if methods, ok := module.Semantics.MethodSets[key]; ok {
			for _, method := range methods {
				if method != nil && method.Name == ident.Name {
					return method
				}
			}
		}
	}
	return nil
}

func selectorBaseType(expr ast.Expr, parents map[ast.NodeID]ast.Node, module *project.Module, ctx *project.CompilerContext) (typeinfo.Type, bool) {
	if expr == nil || module == nil || module.Semantics == nil {
		return nil, false
	}
	baseType, ok := normalizedSelectorBaseType(module.Semantics.ExprTypes[expr.ID()])
	if ok {
		return baseType, true
	}
	ident, ok := expr.(*ast.Ident)
	if !ok || ident == nil {
		return nil, false
	}
	sym := resolveIdentSymbol(ident, parents, module, ctx)
	if sym == nil {
		return nil, false
	}
	symType, ok := symbols.GetSymbolType(sym)
	if !ok || symType == nil {
		return nil, false
	}
	return normalizedSelectorBaseType(symType)
}

func normalizedSelectorBaseType(baseType typeinfo.Type) (typeinfo.Type, bool) {
	if baseType == nil {
		return nil, false
	}
	if typeinfo.IsInvalidOrUnknown(baseType) {
		return nil, false
	}
	if ptr, ok := baseType.(*typeinfo.RawPtrType); ok && ptr.Target != nil {
		baseType = ptr.Target
	}
	return baseType, true
}

func lookupStructFieldSymbol(baseType typeinfo.Type, fieldName string, ctx *project.CompilerContext) *symbols.Symbol {
	if baseType == nil || fieldName == "" || ctx == nil {
		return nil
	}
	field, _, ok := typeinfo.LookupStructField(baseType, fieldName)
	if !ok {
		return nil
	}
	var fieldNode ast.Node
	var location *source.Location
	for _, module := range ctx.Modules() {
		if module == nil || module.AST == nil {
			continue
		}
		for _, stmt := range module.AST.Stmts {
			structDecl, ok := stmt.(*ast.StructDecl)
			if !ok || structDecl == nil || structDecl.Name == nil || structDecl.Name.Name != typeinfo.TypeText(baseType) {
				continue
			}
			structType, ok := structDecl.Type.(*ast.StructType)
			if !ok || structType == nil {
				break
			}
			for _, field := range structType.Fields {
				if field.Name != nil && field.Name.Name == fieldName {
					fieldNode = field.Name
					location = ast.LocOf(field.Name)
					break
				}
			}
			break
		}
	}
	fieldSym := symbols.New(fieldName, symbols.SymbolField, fieldNode, location)
	fieldSym.Type = field.Type
	return fieldSym
}

func selectorMethodKeys(baseType typeinfo.Type) []string {
	if baseType == nil {
		return nil
	}
	keys := []string{typeinfo.TypeText(baseType)}
	if defined, ok := baseType.(*typeinfo.DefinedType); ok && defined.Underlying != nil {
		keys = append(keys, typeinfo.TypeText(defined.Underlying))
	}
	return keys
}

func hoverRange(node ast.Node) Range {
	loc := ast.LocOf(node)
	return locationRange(loc)
}

func locationRange(loc *source.Location) Range {
	if loc == nil || loc.Start == nil || loc.End == nil {
		return Range{}
	}
	return Range{
		Start: Position{Line: loc.Start.Line - 1, Character: loc.Start.Column - 1},
		End:   Position{Line: loc.End.Line - 1, Character: loc.End.Column - 1},
	}
}

func (s *ServerState) resolveHoverSubject(filePath string, line, col int) *hoverSubject {
	ctx, mod := s.currentCompiledModule(filePath)
	cc := buildCursorContext(ctx, mod, line, col)
	if cc == nil {
		return nil
	}
	// Priority order: import > type > decl > selector > symbol > expr.
	for _, resolve := range []func(*cursorContext) *hoverSubject{
		resolveAttributeHoverSubject,
		resolveImportHoverSubject,
		resolveTypeHoverSubject,
		resolveDeclHoverSubject,
		resolveSelectorHoverSubject,
		resolveSymbolHoverSubject,
		resolveExprHoverSubject,
	} {
		if subject := resolve(cc); subject != nil {
			return subject
		}
	}
	return nil
}

func resolveAttributeHoverSubject(cc *cursorContext) *hoverSubject {
	if cc == nil || cc.module == nil || cc.module.AST == nil {
		return nil
	}
	for _, stmt := range cc.module.AST.Stmts {
		if subject := attributeHoverSubject(stmt, cc); subject != nil {
			return subject
		}
		if impl, ok := stmt.(*ast.ImplDecl); ok && impl != nil {
			for _, method := range impl.Methods {
				if subject := attributeHoverSubject(method, cc); subject != nil {
					return subject
				}
			}
		}
	}
	return nil
}

func attributeHoverSubject(node ast.Node, cc *cursorContext) *hoverSubject {
	attributed, ok := node.(ast.AttributedNode)
	if !ok || attributed == nil {
		return nil
	}
	for _, attr := range attributed.GetAttributes() {
		if !locContains(attr.Location, cc.line, cc.col) {
			continue
		}
		hoverAttr := attr
		return &hoverSubject{
			Kind:      hoverSubjectAttribute,
			Node:      node,
			Range:     locationRange(attr.Location),
			Attribute: &hoverAttr,
		}
	}
	return nil
}

func resolveImportHoverSubject(cc *cursorContext) *hoverSubject {
	ident, ok := cc.node.(*ast.Ident)
	if !ok || ident == nil {
		return nil
	}
	parent := cc.parents[ident.ID()]
	if sr, ok := parent.(*ast.ScopeResolution); ok && sr.Module == ident {
		imp, ok := cc.module.Imports[ident.Name]
		if !ok {
			return nil
		}
		hoverImp := imp
		return &hoverSubject{
			Kind:           hoverSubjectImport,
			Node:           ident,
			Range:          hoverRange(ident),
			ResolvedImport: &hoverImp,
		}
	}
	return nil
}

func resolveTypeHoverSubject(cc *cursorContext) *hoverSubject {
	typeNode, ok := hoverTypeNode(cc.node, cc.parents)
	if !ok || typeNode == nil {
		return nil
	}
	selfType, allowAbstractSelf := hoverTypeSyntaxContext(typeNode, cc.parents, cc.ctx, cc.module)
	resolved := typeinfo.ASTTypeWithOptions(typeNode, project.TypeSyntaxOptions(cc.ctx, cc.module, selfType, allowAbstractSelf))
	if resolved == nil {
		return nil
	}
	return &hoverSubject{
		Kind:          hoverSubjectType,
		Node:          cc.node,
		Range:         hoverRange(cc.node),
		ResolvedType:  resolved,
		MethodSymbols: lookupHoverMethodSet(cc.ctx, hoverMethodKeysForTypeNode(typeNode, cc.parents, resolved)),
	}
}

func hoverMethodKeysForTypeNode(typeNode ast.TypeExpr, parents map[ast.NodeID]ast.Node, resolved typeinfo.Type) []string {
	keys := []string{typeinfo.TypeText(resolved)}
	for curr := ast.Node(typeNode); curr != nil; curr = parents[curr.ID()] {
		decl, ok := curr.(ast.TypeDecl)
		if !ok {
			continue
		}
		if name := decl.DeclName(); name != nil && name.Name != "" && name.Name != keys[0] {
			keys = append(keys, name.Name)
		}
		break
	}
	return keys
}

func hoverTypeNode(node ast.Node, parents map[ast.NodeID]ast.Node) (ast.TypeExpr, bool) {
	if node == nil {
		return nil, false
	}
	if typeNode, ok := node.(ast.TypeExpr); ok {
		if isTypeExprPosition(typeNode, parents[typeNode.ID()]) {
			return typeNode, true
		}
	}
	top := node
	for top != nil {
		switch top.(type) {
		case *ast.Ident, *ast.ScopeResolution:
			parent := parents[top.ID()]
			if parent == nil {
				top = nil
				break
			}
			if _, ok := parent.(*ast.ScopeResolution); ok {
				top = parent
				continue
			}
			typeNode, ok := top.(ast.TypeExpr)
			if ok && isTypeExprPosition(typeNode, parent) {
				return typeNode, true
			}
			return nil, false
		default:
			return nil, false
		}
	}
	return nil, false
}

func hoverTypeSyntaxContext(typeNode ast.TypeExpr, parents map[ast.NodeID]ast.Node, ctx *project.CompilerContext, module *project.Module) (typeinfo.Type, bool) {
	for curr := ast.Node(typeNode); curr != nil; curr = parents[curr.ID()] {
		switch node := curr.(type) {
		case *ast.InterfaceDecl:
			return nil, true
		case *ast.ImplDecl:
			if ctx == nil || module == nil || node.Target == nil {
				return nil, false
			}
			return typeinfo.ASTTypeWithOptions(node.Target, project.TypeSyntaxOptions(ctx, module, nil, false)), false
		}
	}
	return nil, false
}

func isTypeExprPosition(typeNode ast.TypeExpr, parent ast.Node) bool {
	if typeNode == nil || parent == nil {
		return false
	}
	switch p := parent.(type) {
	case *ast.LetDecl:
		return p.Type == typeNode
	case *ast.ConstDecl:
		return p.Type == typeNode
	case *ast.TypeAliasDecl:
		return p.Type == typeNode
	case *ast.StructDecl:
		return p.Type == typeNode
	case *ast.InterfaceDecl:
		return p.Type == typeNode
	case *ast.EnumDecl:
		return p.Type == typeNode
	case *ast.ImplDecl:
		return p.Target == typeNode
	case *ast.AsExpr:
		return p.TypeExpr == typeNode
	case *ast.StructLit:
		return p.Type == typeNode
	case *ast.RawPtrType:
		return p.Target == typeNode
	case *ast.OptionalType:
		return p.Inner == typeNode
	case *ast.ArrayType:
		return p.Elem == typeNode
	case *ast.SliceType:
		return p.Elem == typeNode
	case *ast.FuncType:
		if p.Return == typeNode {
			return true
		}
		if slices.Contains(p.Params, typeNode) {
			return true
		}
	case *ast.StructType:
		for _, field := range p.Fields {
			if field.Type == typeNode {
				return true
			}
		}
	case *ast.InterfaceType:
		for _, method := range p.Methods {
			if method.ReturnType == typeNode {
				return true
			}
			for _, param := range method.Params {
				if param.Type == typeNode {
					return true
				}
			}
		}
	case *ast.FnDecl:
		if p.ReturnType == typeNode {
			return true
		}
		for _, param := range p.Params {
			if param.Type == typeNode {
				return true
			}
		}
	}
	return false
}

func resolveDeclHoverSubject(cc *cursorContext) *hoverSubject {
	if cc == nil || cc.node == nil {
		return nil
	}
	switch decl := cc.node.(type) {
	case *ast.FnDecl:
		return declHoverSubject(cc, decl, decl.Name)
	case *ast.LetDecl:
		return declHoverSubject(cc, decl, decl.Name)
	case *ast.ConstDecl:
		return declHoverSubject(cc, decl, decl.Name)
	case ast.TypeDecl:
		return declHoverSubject(cc, decl, decl.DeclName())
	default:
		return nil
	}
}

func declHoverSubject(cc *cursorContext, decl ast.Node, name *ast.Ident) *hoverSubject {
	subject := &hoverSubject{
		Kind:  hoverSubjectDecl,
		Node:  decl,
		Decl:  decl,
		Range: hoverRange(decl),
	}
	if name != nil {
		subject.Symbol = resolveIdentSymbol(name, cc.parents, cc.module, cc.ctx)
		if subject.Symbol != nil && subject.Symbol.Kind == symbols.SymbolType {
			subject.MethodSymbols = lookupHoverMethodSet(cc.ctx, []string{subject.Symbol.Name})
		}
	}
	return subject
}

func resolveSelectorHoverSubject(cc *cursorContext) *hoverSubject {
	ident, ok := cc.node.(*ast.Ident)
	if !ok || ident == nil {
		return nil
	}
	parent := cc.parents[ident.ID()]
	sel, ok := parent.(*ast.SelectorExpr)
	if !ok || sel == nil || sel.Name != ident {
		return nil
	}
	if sym := resolveSelectorMemberSymbol(sel, ident, cc.parents, cc.module, cc.ctx); sym != nil {
		return &hoverSubject{
			Kind:   hoverSubjectSymbol,
			Node:   ident,
			Decl:   documentedDeclAncestor(ident, cc.parents),
			Range:  hoverRange(ident),
			Symbol: sym,
		}
	}
	if exprType, ok := cc.module.Semantics.ExprTypes[sel.ID()]; ok {
		return &hoverSubject{
			Kind:     hoverSubjectExpr,
			Node:     ident,
			Range:    hoverRange(ident),
			ExprType: exprType,
		}
	}
	return nil
}

func resolveSymbolHoverSubject(cc *cursorContext) *hoverSubject {
	ident, ok := cc.node.(*ast.Ident)
	if !ok || ident == nil {
		return nil
	}
	sym := resolveDeclNameSymbol(ident, cc.parents, cc.module)
	if sym == nil {
		sym = resolveInterfaceMethodNameSymbol(ident, cc.parents, cc.ctx, cc.module)
	}
	if sym == nil {
		sym = resolveIdentSymbol(ident, cc.parents, cc.module, cc.ctx)
	}
	if sym == nil {
		return nil
	}
	subject := &hoverSubject{
		Kind:   hoverSubjectSymbol,
		Node:   ident,
		Decl:   documentedDeclAncestor(ident, cc.parents),
		Range:  hoverRange(ident),
		Symbol: sym,
	}
	if sym.Kind == symbols.SymbolType {
		subject.MethodSymbols = lookupHoverMethodSet(cc.ctx, []string{sym.Name})
	}
	return subject
}

func documentedDeclAncestor(node ast.Node, parents map[ast.NodeID]ast.Node) ast.Node {
	for current := node; current != nil; current = parents[current.ID()] {
		if decl, ok := current.(ast.Decl); ok {
			return decl
		}
	}
	return nil
}

func resolveDeclNameSymbol(ident *ast.Ident, parents map[ast.NodeID]ast.Node, module *project.Module) *symbols.Symbol {
	if ident == nil || module == nil || module.Semantics == nil {
		return nil
	}
	parent := parents[ident.ID()]
	if fn, ok := parent.(*ast.FnDecl); ok && fn != nil && fn.Name == ident {
		owner := parents[fn.ID()]
		if _, ok := owner.(*ast.ImplDecl); ok {
			if sym, ok := module.Semantics.MethodSymbol[fn.ID()]; ok && sym != nil {
				return sym
			}
		}
	}
	return nil
}

func resolveInterfaceMethodNameSymbol(ident *ast.Ident, parents map[ast.NodeID]ast.Node, ctx *project.CompilerContext, module *project.Module) *symbols.Symbol {
	if ident == nil || ctx == nil || module == nil {
		return nil
	}
	iface, ok := parents[ident.ID()].(*ast.InterfaceType)
	if !ok || iface == nil {
		return nil
	}
	opts := project.TypeSyntaxOptions(ctx, module, nil, true)
	for _, method := range iface.Methods {
		if method.Name != ident {
			continue
		}
		params := make([]typeinfo.Type, 0, len(method.Params))
		for _, param := range method.Params {
			params = append(params, typeinfo.ASTTypeWithOptions(param.Type, opts))
		}
		sym := symbols.New(ident.Name, symbols.SymbolMethod, ident, ast.LocOf(ident))
		sym.Type = &typeinfo.FuncType{
			Params: params,
			Return: typeinfo.ASTTypeWithOptions(method.ReturnType, opts),
		}
		return sym
	}
	return nil
}

func lookupHoverMethodSet(ctx *project.CompilerContext, keys []string) []*symbols.Symbol {
	if ctx == nil || len(keys) == 0 {
		return nil
	}
	keySet := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		keySet[key] = struct{}{}
	}
	if len(keySet) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var methods []*symbols.Symbol
	for _, module := range ctx.Modules() {
		if module == nil || module.Semantics == nil {
			continue
		}
		for key := range keySet {
			for _, sym := range module.Semantics.MethodSets[key] {
				if sym == nil {
					continue
				}
				signature := sym.Name
				if typ, ok := symbols.GetSymbolType(sym); ok && typ != nil {
					signature += "|" + formatHoverTypeInline(typ)
				}
				if sym.Location != nil && sym.Location.Filename != nil && sym.Location.Start != nil {
					signature += fmt.Sprintf("|%s:%d:%d", *sym.Location.Filename, sym.Location.Start.Line, sym.Location.Start.Column)
				}
				if _, ok := seen[signature]; ok {
					continue
				}
				seen[signature] = struct{}{}
				methods = append(methods, sym)
			}
		}
	}
	return methods
}

func resolveExprHoverSubject(cc *cursorContext) *hoverSubject {
	if cc == nil || cc.node == nil || cc.module == nil || cc.module.Semantics == nil {
		return nil
	}
	if _, ok := cc.node.(ast.Expr); !ok {
		return nil
	}
	exprType, ok := cc.module.Semantics.ExprTypes[cc.node.ID()]
	if !ok {
		return nil
	}
	return &hoverSubject{
		Kind:     hoverSubjectExpr,
		Node:     cc.node,
		Range:    hoverRange(cc.node),
		ExprType: exprType,
	}
}

func renderHoverSubject(subject *hoverSubject) string {
	if subject == nil {
		return ""
	}
	var text string
	switch subject.Kind {
	case hoverSubjectSymbol, hoverSubjectDecl:
		// Decl subjects always set Symbol. Nil guard only covers symbol-only paths.
		if subject.Symbol == nil {
			return ""
		}
		text = fmt.Sprintf("(%s) %s", subject.Symbol.Kind, subject.Symbol.Name)
		if typ, ok := symbols.GetSymbolType(subject.Symbol); ok && typ != nil {
			if subject.Symbol.Kind == symbols.SymbolType {
				text += renderTypeDetails(typ, subject.MethodSymbols)
			} else {
				text += ": " + formatHoverTypeInline(typ)
			}
		}
	case hoverSubjectExpr:
		if subject.ExprType == nil {
			return ""
		}
		text = fmt.Sprintf("(expr): %s", formatHoverTypeInline(subject.ExprType))
	case hoverSubjectType:
		if subject.ResolvedType == nil {
			return ""
		}
		text = "(type)"
		if inline, ok := hoverTypeLabel(subject.ResolvedType); ok && inline != "" {
			text += " " + inline
		}
		text += renderTypeDetails(subject.ResolvedType, subject.MethodSymbols)
	case hoverSubjectImport:
		if subject.ResolvedImport == nil {
			return ""
		}
		name := subject.ResolvedImport.ImportPath
		if ident, ok := subject.Node.(*ast.Ident); ok && ident != nil && ident.Name != "" {
			name = ident.Name
		}
		text = fmt.Sprintf("(import) %s -> %s", name, subject.ResolvedImport.ImportPath)
	case hoverSubjectAttribute:
		if subject.Attribute == nil {
			return ""
		}
		text = "(attribute) #[" + subject.Attribute.Name + "]"
	default:
		return ""
	}
	if doc := hoverDocComment(subject); doc != "" {
		// Keep docs outside code fence so markdown renders them as normal prose
		// with the separator bar users expect from other LSPs. Preserve source
		// line breaks explicitly so markdown does not collapse adjacent doc lines.
		doc = strings.ReplaceAll(doc, "\n", "  \n")
		return fmt.Sprintf("```peeper\n%s\n```\n\n---\n\n%s", text, doc)
	}
	return fmt.Sprintf("```peeper\n%s\n```", text)
}

func renderTypeDetails(typ typeinfo.Type, methods []*symbols.Symbol) string {
	body := formatHoverTypeBody(typ)
	methodText := formatHoverMethods(methods)
	switch {
	case body == "" && methodText == "":
		return ""
	case body == "" && methodText != "":
		return "\n\n// methods\n" + methodText
	case body != "" && methodText == "":
		return "\n\n// inner type\n" + body
	default:
		return "\n\n// inner type\n" + body + "\n\n// methods\n" + methodText
	}
}

func formatHoverTypeBody(typ typeinfo.Type) string {
	switch t := typ.(type) {
	case *typeinfo.DefinedType:
		if t == nil || t.Underlying == nil {
			return ""
		}
		return formatHoverTypeBody(t.Underlying)
	case *typeinfo.StructType:
		if t == nil {
			return ""
		}
		var b strings.Builder
		b.WriteString("struct{\n")
		for _, field := range t.Fields {
			b.WriteString("  ")
			b.WriteString(field.Name)
			b.WriteString(": ")
			b.WriteString(formatHoverTypeInline(field.Type))
			b.WriteString("\n")
		}
		b.WriteString("}")
		return b.String()
	case *typeinfo.OptionalType:
		if t == nil {
			return ""
		}
		return formatHoverTypeInline(t)
	case *typeinfo.ArrayType:
		if t == nil {
			return ""
		}
		return formatHoverTypeInline(t)
	case *typeinfo.SliceType:
		if t == nil {
			return ""
		}
		return formatHoverTypeInline(t)
	case *typeinfo.InterfaceType:
		if t == nil {
			return ""
		}
		var b strings.Builder
		b.WriteString("interface{\n")
		for _, method := range t.Methods {
			b.WriteString("  ")
			b.WriteString(method.Name)
			b.WriteString("(")
			for i, param := range method.Params {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(formatHoverTypeInline(param.Type))
			}
			b.WriteString(")")
			if ret := formatHoverTypeInline(method.Return); ret != "" {
				b.WriteString(" -> ")
				b.WriteString(ret)
			}
			b.WriteString("\n")
		}
		b.WriteString("}")
		return b.String()
	default:
		return ""
	}
}

func formatHoverMethods(methods []*symbols.Symbol) string {
	if len(methods) == 0 {
		return ""
	}
	var b strings.Builder
	for _, method := range methods {
		if method == nil {
			continue
		}
		b.WriteString("  ")
		b.WriteString(method.Name)
		if typ, ok := symbols.GetSymbolType(method); ok && typ != nil {
			b.WriteString(": ")
			b.WriteString(formatHoverTypeInline(typ))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func hoverTypeLabel(typ typeinfo.Type) (string, bool) {
	switch t := typ.(type) {
	case *typeinfo.NamedType:
		if t == nil {
			return "", false
		}
		return t.Name, true
	case *typeinfo.DefinedType:
		if t == nil {
			return "", false
		}
		return t.Name, true
	default:
		return "", false
	}
}

func formatHoverTypeInline(typ typeinfo.Type) string {
	switch t := typ.(type) {
	case nil:
		return ""
	case *typeinfo.FuncType:
		var b strings.Builder
		b.WriteString("fn(")
		for i, param := range t.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(formatHoverTypeInline(param))
		}
		b.WriteString(")")
		if ret := formatHoverTypeInline(t.Return); ret != "" {
			b.WriteString(" -> ")
			b.WriteString(ret)
		}
		return b.String()
	case *typeinfo.DefinedType:
		if t == nil {
			return ""
		}
		return t.Name
	case *typeinfo.OptionalType:
		if t == nil {
			return ""
		}
		return "?" + formatHoverTypeInline(t.Inner)
	case *typeinfo.ArrayType:
		if t == nil {
			return ""
		}
		return "[" + t.Len + "]" + formatHoverTypeInline(t.Elem)
	case *typeinfo.SliceType:
		if t == nil {
			return ""
		}
		return "[]" + formatHoverTypeInline(t.Elem)
	default:
		return typ.Text()
	}
}

func hoverDocComment(subject *hoverSubject) string {
	if subject == nil {
		return ""
	}
	if subject.Attribute != nil {
		if def, ok := ast.AttributeDefinitions[subject.Attribute.Name]; ok && def.Doc != "" {
			return def.Doc
		}
	}
	var symNode ast.Node
	if subject.Symbol != nil {
		symNode = subject.Symbol.ASTNode
	}
	for _, node := range [3]ast.Node{subject.Decl, symNode, subject.Node} {
		docNode, ok := node.(ast.DocumentedNode)
		if !ok || docNode == nil {
			continue
		}
		doc := docNode.GetDocComment()
		if doc != nil && strings.TrimSpace(doc.Text) != "" {
			return strings.TrimSpace(doc.Text)
		}
	}
	return ""
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
	subject := s.resolveHoverSubject(path, params.Position.Line+1, params.Position.Character+1)
	if subject == nil {
		return nil, nil
	}
	value := renderHoverSubject(subject)
	if value == "" {
		return nil, nil
	}

	return &Hover{
		Contents: MarkupContent{
			Kind:  "markdown",
			Value: value,
		},
		Range: &subject.Range,
	}, nil
}

func (s *ServerState) HandleDefinition(params DefinitionParams) ([]Location, error) {
	path := uriToPath(string(params.TextDocument.URI))
	ctx, mod := s.currentCompiledModule(path)
	cc := buildCursorContext(ctx, mod, params.Position.Line+1, params.Position.Character+1)
	if cc == nil {
		return nil, nil
	}
	ident, _ := cc.node.(*ast.Ident)
	if ident == nil {
		return nil, nil
	}
	sym := resolveIdentSymbol(ident, cc.parents, cc.module, cc.ctx)
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
	ctx, mod := s.currentCompiledModule(path)
	cc := buildCursorContext(ctx, mod, params.Position.Line+1, params.Position.Character+1)
	if cc == nil {
		return nil, nil
	}
	ident, _ := cc.node.(*ast.Ident)
	if ident == nil {
		return nil, nil
	}
	targetSym := resolveIdentSymbol(ident, cc.parents, cc.module, cc.ctx)
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
