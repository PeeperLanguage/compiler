package resolver

import (
	"fmt"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/problems"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/source"
)

type resolver struct {
	ctx    *project.CompilerContext
	module *project.Module
}

func (r *resolver) resolveModule() {
	if r == nil || r.module == nil || r.module.AST == nil {
		return
	}
	if r.module.Semantics == nil {
		r.module.Semantics = project.NewSemanticInfo()
	}
	r.markPendingTopLevelBindings()
	ast.ForEachDecl(r.module.AST, func(decl ast.Decl) bool {
		switch node := decl.(type) {
		case *ast.LetDecl:
			r.resolveTopLevelBinding(node.Name, node.Value)
		case *ast.ConstDecl:
			r.resolveTopLevelBinding(node.Name, node.Value)
		}
		return true
	})
	ast.ForEachDecl(r.module.AST, func(decl ast.Decl) bool {
		switch node := decl.(type) {
		case *ast.FnDecl:
			r.resolveFunction(node)
		}
		return true
	})
}

func (r *resolver) markPendingTopLevelBindings() {
	if r == nil || r.module == nil || r.module.ModuleScope == nil {
		return
	}
	for _, sym := range r.module.ModuleScope.Symbols() {
		if sym == nil {
			continue
		}
		switch sym.Kind {
		case symbols.SymbolVar, symbols.SymbolConst:
			sym.Initializing = true
		}
	}
}

func (r *resolver) resolveTopLevelBinding(name *ast.Ident, value ast.Expr) {
	if r == nil || r.module == nil || r.module.ModuleScope == nil || name == nil || name.Name == "" {
		return
	}
	sym, ok := r.module.ModuleScope.LookupLocal(name.Name)
	if !ok || sym == nil {
		return
	}
	if value != nil {
		r.resolveExpr(r.module.ModuleScope, value)
	}
	sym.Initializing = false
}

func (r *resolver) resolveFunction(fn *ast.FnDecl) {
	if r == nil || r.module == nil || fn == nil {
		return
	}
	var sym *symbols.Symbol
	if fn.Receiver != nil {
		sym = r.module.Semantics.MethodSymbol[fn.ID()]
	} else {
		sym, _ = r.module.ModuleScope.Lookup(fn.Name.Name)
	}
	if sym == nil || sym.Scope == nil {
		return
	}
	funcScope := sym.Scope
	params := fn.ParamsWithReceiver()
	for i, param := range params {
		if param.Name == nil || param.Name.Name == "" {
			if fn.Body != nil {
				r.ctx.Diagnostics.AddError(diagnostics.ErrMissingIdentifier, "parameter name required", param.Location, "")
				return
			}
			continue
		}
		// Resolve each default before declaring its parameter. This makes
		// receiver and earlier parameters visible while rejecting self and
		// later-parameter references at the declaration boundary.
		if param.Default != nil {
			r.resolveExpr(funcScope, param.Default)
		}
		paramSym := symbols.New(param.Name.Name, symbols.SymbolParam, param.Name, ast.LocOf(param.Name))
		paramSym.Mutable = param.IsMutable
		paramSym.IsReceiver = fn.Receiver != nil && i == 0
		if err := funcScope.Declare(paramSym); err != nil {
			problems.ReportRedeclaration(r.ctx.Diagnostics, funcScope, err.Error(), param.Name.Name, param.Name.Location)
			return
		}
	}
	if fn.ReturnOrigins != nil {
		for _, origin := range fn.ReturnOrigins.Sources {
			if origin == nil {
				continue
			}
			name := origin.Name
			if name == "self" && fn.Receiver != nil && fn.Receiver.Name != nil {
				name = fn.Receiver.Name.Name
			}
			if source, ok := funcScope.Lookup(name); ok && source != nil && source.Kind == symbols.SymbolParam {
				r.module.Semantics.ResolvedSymbols[origin.ID()] = source
				source.Used = true
			}
		}
	}
	if fn.Body != nil {
		r.resolveBlock(funcScope, fn.Body)
	}
}

func (r *resolver) resolveBlock(scope *symbols.Scope, block *ast.BlockStmt) {
	if block == nil {
		return
	}
	r.module.Semantics.BlockScopes[block.ID()] = scope
	for _, stmt := range block.Stmts {
		r.resolveStmt(scope, stmt)
	}
}

func (r *resolver) resolveStmt(scope *symbols.Scope, stmt ast.Stmt) {
	if stmt == nil {
		return
	}
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		r.resolveBlock(symbols.NewScope(scope), node)
	case *ast.LetDecl:
		r.resolveLocalBinding(scope, node.Name, symbols.SymbolVar, node.Value, node, node.Location)
	case *ast.ConstDecl:
		r.resolveLocalBinding(scope, node.Name, symbols.SymbolConst, node.Value, node, node.Location)
	case *ast.ReturnStmt:
		if node.Value != nil {
			r.resolveExpr(scope, node.Value)
		}
	case *ast.IfStmt:
		if node.Cond == nil {
			r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidStatement, "if condition required", ast.LocOf(node), "")
			return
		}
		r.resolveExpr(scope, node.Cond)
		r.resolveBlock(symbols.NewScope(scope), node.Then)
		if elseBlock, ok := node.Else.(*ast.BlockStmt); ok {
			r.resolveBlock(symbols.NewScope(scope), elseBlock)
			return
		}
		if node.Else != nil {
			r.resolveStmt(scope, node.Else)
		}
	case *ast.ForStmt:
		if node.Cond != nil {
			r.resolveExpr(scope, node.Cond)
		}
		r.resolveBlock(symbols.NewScope(scope), node.Body)
	case *ast.ExprStmt:
		r.resolveExpr(scope, node.Expr)
	case *ast.AssignStmt:
		r.resolveAssignTarget(scope, node.Target)
		r.resolveExpr(scope, node.Value)
	case *ast.BadStmt, *ast.BadDecl, *ast.ImportDecl, *ast.FnDecl,
		*ast.TypeAliasDecl, *ast.StructDecl, *ast.InterfaceDecl, *ast.EnumDecl:
		r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidStatement, "unsupported statement", ast.LocOf(node), "")
	default:
		panic(fmt.Sprintf("resolver: unhandled statement %T", stmt))
	}
}

func (r *resolver) resolveLocalBinding(scope *symbols.Scope, name *ast.Ident, kind symbols.Kind, value ast.Expr, node ast.Node, loc *source.Location) {
	sym := symbols.New(name.Name, kind, node, ast.LocOf(name))
	sym.Initializing = true
	if err := scope.Declare(sym); err != nil {
		problems.ReportRedeclaration(r.ctx.Diagnostics, scope, err.Error(), name.Name, loc)
		return
	}
	if value != nil {
		r.resolveExpr(scope, value)
	}
	sym.Initializing = false
}

func (r *resolver) resolveExpr(scope *symbols.Scope, expr ast.Expr) {
	if expr == nil {
		return
	}
	switch node := expr.(type) {
	case *ast.NumberLit:
		return
	case *ast.StringLit:
		return
	case *ast.ByteLit:
		return
	case *ast.CharLit:
		return
	case *ast.BoolLit:
		return
	case *ast.NoneLit:
		return
	case *ast.Ident:
		sym, ok := scope.Lookup(node.Name)
		if ok && sym != nil {
			r.module.Semantics.ResolvedSymbols[node.ID()] = sym
			sym.Used = true
			if sym.Kind == symbols.SymbolImport {
				r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression, "import alias must be qualified with `::`", ast.LocOf(node), "")
				return
			}
			if sym.Initializing {
				msg := "symbol `" + node.Name + "` used before it's defined"
				r.ctx.Diagnostics.Add(
					diagnostics.NewError(msg).
						WithCode(diagnostics.ErrUseBeforeDecl).
						WithPrimaryLabel(ast.LocOf(node), msg).
						WithHelp("rename binding or use earlier value"),
				)
				return
			}
			return
		}
		reportUnresolved(r.module, scope, node, r.ctx.Diagnostics)
	case *ast.ScopeResolution:
		if r.resolveVariantPath(scope, node) {
			return
		}
		if r.resolveScopeResolution(node, false) {
			return
		}
	case *ast.SelectorExpr:
		r.resolveExpr(scope, node.Expr)
	case *ast.IndexExpr:
		r.resolveExpr(scope, node.Expr)
		r.resolveExpr(scope, node.Index)
	case *ast.RangeExpr:
		r.resolveExpr(scope, node.Start)
		r.resolveExpr(scope, node.End)
	case *ast.StructLit:
		if scopedType, ok := node.Type.(*ast.ScopeResolution); ok {
			r.resolveScopeResolution(scopedType, true)
		}
		for _, field := range node.Fields {
			r.resolveExpr(scope, field.Value)
		}
	case *ast.VariantLit:
		if !r.resolveVariantPath(scope, node.Case) {
			r.resolveScopeResolution(node.Case, false)
		}
		for _, field := range node.Fields {
			r.resolveExpr(scope, field.Value)
		}
	case *ast.ArrayLit:
		if scopedType, ok := node.Type.(*ast.ScopeResolution); ok {
			r.resolveScopeResolution(scopedType, true)
		}
		for _, value := range node.Values {
			r.resolveExpr(scope, value)
		}
	case *ast.UnaryExpr:
		r.resolveExpr(scope, node.Expr)
	case *ast.AddressExpr:
		r.resolveExpr(scope, node.Expr)
	case *ast.BinaryExpr:
		r.resolveExpr(scope, node.Left)
		r.resolveExpr(scope, node.Right)
	case *ast.CallExpr:
		r.resolveExpr(scope, node.Callee)
		for _, arg := range node.Args {
			r.resolveExpr(scope, arg)
		}
	case *ast.FreeExpr:
		r.resolveExpr(scope, node.Expr)
	case *ast.PrintExpr:
		r.resolveExpr(scope, node.Expr)
	case *ast.AsExpr:
		r.resolveExpr(scope, node.Expr)
	case *ast.BadExpr:
		r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression, "unsupported expression type", ast.LocOf(node), "")
	default:
		panic(fmt.Sprintf("resolver: unhandled expression %T", expr))
	}
}

func Resolve(ctx *project.CompilerContext, module *project.Module) {
	if module == nil || ctx == nil {
		return
	}
	r := &resolver{module: module, ctx: ctx}
	r.resolveModule()
}

func (r *resolver) resolveAssignTarget(scope *symbols.Scope, expr ast.Expr) {
	switch node := expr.(type) {
	case *ast.Ident:
		sym, ok := scope.Lookup(node.Name)
		if ok && sym != nil {
			sym.Used = true
			return
		}
		reportUnresolved(r.module, scope, node, r.ctx.Diagnostics)
	case *ast.SelectorExpr:
		r.resolveExpr(scope, node.Expr)
	default:
		r.resolveExpr(scope, expr)
	}
}

func (r *resolver) resolveScopeResolution(node *ast.ScopeResolution, allowTypeArguments bool) bool {
	if r == nil || r.module == nil || node == nil {
		return false
	}
	qualifierNode, memberNode, imported := node.ImportMember()
	if !imported {
		r.ctx.Diagnostics.AddError(diagnostics.ErrUndefinedSymbol, "unsupported qualified path `"+node.TypeText()+"`", ast.LocOf(node), "qualified values currently use `module::member`")
		return false
	}
	if !allowTypeArguments && len(node.Segments[1].TypeArgs) != 0 {
		r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidType, "type arguments are not allowed on value paths", ast.LocOf(node), "generic functions and values are not supported")
		return false
	}
	resolved, ok := r.lookupImportedMember(qualifierNode, memberNode, node)
	if !ok {
		return false
	}
	r.module.Semantics.ResolvedSymbols[node.ID()] = resolved
	return true
}

func (r *resolver) resolveVariantPath(scope *symbols.Scope, path *ast.ScopeResolution) bool {
	typePath, caseName, variantPath := path.EnumVariantMember()
	if !variantPath {
		return false
	}

	var enumSymbol *symbols.Symbol
	var enumName *ast.Ident
	switch enumPath := typePath.(type) {
	case *ast.NamedType:
		enumName = path.Segments[0].Name
		resolved, ok := scope.Lookup(enumPath.Name)
		if !ok || resolved == nil || resolved.Kind != symbols.SymbolType {
			return false
		}
		enumSymbol = resolved
	case *ast.AppliedType:
		enumName = enumPath.Name
		resolved, ok := scope.Lookup(enumPath.Name.Name)
		if !ok || resolved == nil || resolved.Kind != symbols.SymbolType {
			return false
		}
		enumSymbol = resolved
	case *ast.ScopeResolution:
		qualifier, member, ok := enumPath.ImportMember()
		if !ok {
			return false
		}
		enumName = member
		resolved, found := r.lookupImportedMember(qualifier, member, path)
		if !found {
			return true
		}
		enumSymbol = resolved
	default:
		return false
	}

	if _, declaredEnum := enumSymbol.ASTNode.(*ast.EnumDecl); !declaredEnum || enumSymbol.Scope == nil {
		r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression, "only enum declarations can qualify variants", ast.LocOf(enumName), "use the enum declaration name directly")
		return true
	}
	variant, ok := enumSymbol.Scope.LookupLocal(caseName.Name)
	if !ok || variant == nil || variant.Kind != symbols.SymbolVariant {
		r.ctx.Diagnostics.AddError(diagnostics.ErrUndefinedSymbol, "unknown variant `"+caseName.Name+"` in enum `"+enumSymbol.Name+"`", ast.LocOf(caseName), "")
		return true
	}
	enumSymbol.Used = true
	variant.Used = true
	r.module.Semantics.ResolvedSymbols[enumName.ID()] = enumSymbol
	r.module.Semantics.ResolvedSymbols[path.ID()] = variant
	r.module.Semantics.ResolvedSymbols[caseName.ID()] = variant
	return true
}

func (r *resolver) lookupImportedMember(qualifierNode, memberNode *ast.Ident, site ast.Node) (*symbols.Symbol, bool) {
	qualifier := qualifierNode.Name
	member := memberNode.Name
	resolved, ok := project.LookupImportedSymbol(r.ctx, r.module, qualifier, member)
	if !ok || resolved.Symbol == nil {
		if _, exists := r.module.Imports[qualifier]; !exists {
			r.ctx.Diagnostics.AddError(diagnostics.ErrModuleNotFound, "unknown import alias `"+qualifier+"`", ast.LocOf(site), "")
		} else if resolved.Module == nil || resolved.Module.ModuleScope == nil {
			r.ctx.Diagnostics.AddError(diagnostics.ErrModuleNotFound, "imported module not loaded for `"+qualifier+"`", ast.LocOf(site), "")
		} else {
			r.ctx.Diagnostics.AddError(diagnostics.ErrUndefinedSymbol, "unknown identifier `"+member+"` in module `"+qualifier+"`", ast.LocOf(site), "")
		}
		return nil, false
	}
	if !resolved.Symbol.IsPub {
		r.ctx.Diagnostics.AddError(diagnostics.ErrSymbolNotExported, "`"+member+"` is not exported from `"+qualifier+"`", ast.LocOf(site), "use of unexported symbol").
			WithSecondaryLabel(resolved.Symbol.Location, "defined here").
			WithNote("symbols with uppercase are exported otherwise private")
		return nil, false
	}
	return resolved.Symbol, true
}
