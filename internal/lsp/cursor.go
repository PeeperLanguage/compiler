package lsp

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
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

func locContains(loc *source.Location, line, col int) bool {
	if loc == nil || loc.Start == nil || loc.End == nil {
		return false
	}
	if line < loc.Start.Line || (line == loc.Start.Line && col < loc.Start.Column) {
		return false
	}
	if line > loc.End.Line || (line == loc.End.Line && col >= loc.End.Column) {
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

func buildCursorContext(ctx *project.CompilerContext, module *project.Module, position source.Position) *cursorContext {
	if ctx == nil || module == nil || module.AST == nil {
		return nil
	}
	cc := &cursorContext{
		ctx:     ctx,
		module:  module,
		line:    position.Line,
		col:     position.Column,
		parents: make(map[ast.NodeID]ast.Node),
	}
	walkModuleAST(module, func(n ast.Node, parent ast.Node) bool {
		if parent != nil {
			cc.parents[n.ID()] = parent
		}
		if locContains(ast.LocOf(n), position.Line, position.Column) {
			cc.node = n
			return true
		}
		return false
	})
	return cc
}

func resolveIdentSymbol(ident *ast.Ident, parents map[ast.NodeID]ast.Node, module *project.Module, ctx *project.CompilerContext) *symbols.Symbol {
	if ident == nil {
		return nil
	}
	if module != nil && module.Semantics != nil {
		if sym := module.Semantics.ResolvedSymbols[ident.ID()]; sym != nil {
			return sym
		}
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
	var scope *symbols.Scope
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
				scope = sym.Scope
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
	for _, key := range typeinfo.GetMethodLookupKeys(baseType) {
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
	if target, ok := typeinfo.PointerTarget(baseType); ok {
		baseType = target
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
