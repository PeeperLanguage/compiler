package lower

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"compiler/internal/constvalue"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/project"
	"compiler/internal/semantics/consteval"
	"compiler/internal/semantics/intrinsics"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
	"compiler/pkg/numeric"
	"unicode/utf8"
)

func GenerateHIR(ctx *project.CompilerContext, module *project.Module) *hir.Module {
	if module == nil {
		return nil
	}
	out := &hir.Module{
		Name:     module.ImportPath,
		FilePath: module.FilePath,
		Types:    ctx.Types,
		Externs:  make([]hir.Extern, 0),
		Funcs:    make([]*hir.Function, 0),
	}
	ast.ForEachDecl(module.AST, func(decl ast.Decl) bool {
		fn, ok := decl.(*ast.FnDecl)
		if !ok || fn == nil || fn.Name == nil {
			return true
		}
		var sym *symbols.Symbol
		if fn.Receiver != nil {
			sym = module.Semantics.MethodSymbol[fn.ID()]
		} else {
			sym, _ = module.ModuleScope.Lookup(fn.Name.Name)
		}
		if sym == nil {
			return true
		}
		fnType, _ := symbols.GetSymbolType(sym)
		resolvedFnType, _ := fnType.(*typeinfo.FuncType)
		emittedName, _ := callableName(module, sym)
		if fn.Body == nil {
			params, returnType := lowerExternSignature(ctx, module, sym.Scope, fn.ParamsWithReceiver(), fn.ReturnType, resolvedFnType)
			out.Externs = append(out.Externs, hir.Extern{
				Name:       emittedName,
				Params:     params,
				ReturnType: returnType,
				NodeID:     hir.NodeID(fn.ID()),
				SymbolID:   sym.ID,
				Location:   ast.LocOf(fn.Name),
			})
		} else {
			hirFn := lowerASTFunctionNamed(ctx, module, sym, fn, emittedName)
			if hirFn != nil {
				out.Funcs = append(out.Funcs, hirFn)
			}
		}
		return true
	})
	return out
}

func lowerExternSignature(ctx *project.CompilerContext, module *project.Module, scope *symbols.Scope, params []ast.Param, fallbackReturnType ast.TypeExpr, resolvedFnType *typeinfo.FuncType) ([]ir.Param, ir.TypeID) {
	loweredParams := make([]ir.Param, 0, len(params))
	for i, param := range params {
		name := ""
		if param.Name != nil {
			name = param.Name.Name
		}
		paramType := typeinfo.TypeFromSyntax(param.Type, typeinfo.SyntaxOptions{Target: ctx.Target, AllowAbstractSelf: true})
		if resolvedFnType != nil && i < len(resolvedFnType.Params) && resolvedFnType.Params[i] != nil {
			paramType = resolvedFnType.Params[i]
		}
		var symbolID symbols.SymbolID
		if param.Name != nil {
			if sym, ok := scope.LookupNode(param.Name); ok && sym != nil {
				symbolID = sym.ID
			}
		}
		loweredParams = append(loweredParams, ir.Param{Name: name, Type: loweredTypeID(ctx, module, paramType), SymbolID: symbolID})
	}

	returnType := typeinfo.TypeFromSyntax(fallbackReturnType, typeinfo.SyntaxOptions{Target: ctx.Target, AllowAbstractSelf: true})
	if resolvedFnType != nil && resolvedFnType.Return != nil {
		returnType = resolvedFnType.Return
	}
	return loweredParams, loweredReturnTypeID(ctx, module, returnType)
}

func lowerASTFunctionNamed(ctx *project.CompilerContext, module *project.Module, sym *symbols.Symbol, fn *ast.FnDecl, emittedName string) *hir.Function {
	if sym == nil || fn == nil || fn.Body == nil || sym.Scope == nil {
		return nil
	}
	funcScope := sym.Scope
	retType, ok := symbols.GetSymbolType(sym)
	if ok {
		if fnType, ok := retType.(*typeinfo.FuncType); ok && fnType != nil {
			retType = fnType.Return
		}
	}
	if !ok || retType == nil {
		retType = typeinfo.TypeFromSyntax(fn.ReturnType, typeinfo.SyntaxOptions{Target: ctx.Target, AllowAbstractSelf: true})
	}
	retTypeStr := loweredReturnTypeID(ctx, module, retType)
	hirFn := &hir.Function{
		Name:       emittedName,
		Params:     make([]ir.Param, 0, len(fn.ParamsWithReceiver())),
		ReturnType: retTypeStr,
		Body:       &hir.Block{Stmts: make([]hir.Stmt, 0), NodeID: hir.NodeID(fn.Body.ID()), Location: ast.LocOf(fn.Body)},
		NodeID:     hir.NodeID(fn.ID()),
		SymbolID:   sym.ID,
		Location:   ast.LocOf(fn),
	}
	for _, param := range fn.ParamsWithReceiver() {
		name := ""
		var symbolID symbols.SymbolID
		paramType := typeinfo.TypeFromSyntax(param.Type, typeinfo.SyntaxOptions{Target: ctx.Target, AllowAbstractSelf: true})
		if param.Name != nil {
			sym, ok := funcScope.LookupNode(param.Name)
			if ok && sym != nil {
				name = symbolName(module, sym)
				symbolID = sym.ID
				if t, ok := symbols.GetSymbolType(sym); ok {
					paramType = t
				}
			} else {
				name = param.Name.Name
			}
		}
		hirFn.Params = append(hirFn.Params, ir.Param{Name: name, Type: loweredTypeID(ctx, module, paramType), SymbolID: symbolID})
	}
	appendBlock(module, funcScope, hirFn.Body, fn.Body, retType, ctx)
	return hirFn
}

func appendBlock(module *project.Module, parentScope *symbols.Scope, out *hir.Block, block *ast.BlockStmt, returnType typeinfo.Type, ctx *project.CompilerContext) {
	if out == nil || block == nil {
		return
	}
	out.Location = ast.LocOf(block)
	out.NodeID = hir.NodeID(block.ID())
	scope := parentScope
	if module.Semantics != nil {
		if s, ok := module.Semantics.BlockScopes[block.ID()]; ok && s != nil {
			scope = s
		}
	}
	for _, stmt := range block.Stmts {
		appendStmt(module, scope, out, stmt, returnType, ctx)
	}
}

func appendStmt(module *project.Module, scope *symbols.Scope, out *hir.Block, stmt ast.Stmt, returnType typeinfo.Type, ctx *project.CompilerContext) {
	switch node := stmt.(type) {
	case nil:
		return
	case *ast.BlockStmt:
		block := &hir.Block{Stmts: make([]hir.Stmt, 0), NodeID: hir.NodeID(node.ID()), Location: ast.LocOf(node)}
		appendBlock(module, scope, block, node, returnType, ctx)
		out.Stmts = append(out.Stmts, block)

	case *ast.LetDecl:
		if node.Name == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "let binding missing name", NodeID: hir.NodeID(node.ID()), Location: ast.LocOf(node)})
			return
		}
		sym, ok := scope.LookupNode(node)
		if !ok || sym == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "let binding missing symbol: " + node.Name.Name, NodeID: hir.NodeID(node.ID()), Location: ast.LocOf(node)})
			return
		}
		var valueExpr ir.Expr
		if node.Value != nil {
			valueExpr = lowerASTExpr(ctx, module, scope, node.Value, sym.Type)
		}
		if shouldDiscardBindingValue(sym) {
			out.Stmts = append(out.Stmts, &hir.ExprStmt{Value: valueExpr, NodeID: hir.NodeID(node.ID()), ValueNodeID: hir.NodeID(node.Value.ID()), Location: ast.LocOf(node)})
			return
		}
		out.Stmts = append(out.Stmts, &hir.Binding{Name: symbolName(module, sym), Constant: false, Type: loweredTypeID(ctx, module, sym.Type), Value: valueExpr, NodeID: hir.NodeID(node.ID()), SymbolID: sym.ID, Location: ast.LocOf(node)})

	case *ast.ConstDecl:
		if node.Name == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "const binding missing name", NodeID: hir.NodeID(node.ID()), Location: ast.LocOf(node)})
			return
		}
		sym, ok := scope.LookupNode(node)
		if !ok || sym == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "const binding missing symbol: " + node.Name.Name, NodeID: hir.NodeID(node.ID()), Location: ast.LocOf(node)})
			return
		}
		valueExpr := ir.Expr(&ir.InvalidExpr{Message: "missing initializer", Type: ir.InvalidType})
		if node.Value != nil {
			valueExpr = lowerASTExpr(ctx, module, scope, node.Value, sym.Type)
		}
		if shouldDiscardBindingValue(sym) {
			out.Stmts = append(out.Stmts, &hir.ExprStmt{Value: valueExpr, NodeID: hir.NodeID(node.ID()), ValueNodeID: hir.NodeID(node.Value.ID()), Location: ast.LocOf(node)})
			return
		}
		out.Stmts = append(out.Stmts, &hir.Binding{Name: symbolName(module, sym), Constant: true, Type: loweredTypeID(ctx, module, sym.Type), Value: valueExpr, NodeID: hir.NodeID(node.ID()), SymbolID: sym.ID, Location: ast.LocOf(node)})

	case *ast.IfStmt:
		condExpr := ir.Expr(&ir.InvalidExpr{Message: "invalid condition", Type: ir.InvalidType})
		if node.Cond != nil {
			condExpr = lowerASTExpr(ctx, module, scope, node.Cond, &typeinfo.BoolType{})
		}
		ifStmt := &hir.If{
			Cond:     condExpr,
			Then:     &hir.Block{Stmts: make([]hir.Stmt, 0), NodeID: hir.NodeID(node.Then.ID()), Location: ast.LocOf(node.Then)},
			NodeID:   hir.NodeID(node.ID()),
			Location: ast.LocOf(node),
		}
		appendBlock(module, scope, ifStmt.Then, node.Then, returnType, ctx)
		if node.Else != nil {
			ifStmt.Else = lowerElse(module, scope, node.Else, returnType, ctx)
		}
		out.Stmts = append(out.Stmts, ifStmt)
	case *ast.ForStmt:
		var condExpr ir.Expr
		if node.Cond != nil {
			condExpr = lowerASTExpr(ctx, module, scope, node.Cond, &typeinfo.BoolType{})
		}
		loop := &hir.For{
			Cond:     condExpr,
			Body:     &hir.Block{Stmts: make([]hir.Stmt, 0), NodeID: hir.NodeID(node.Body.ID()), Location: ast.LocOf(node.Body)},
			NodeID:   hir.NodeID(node.ID()),
			Location: ast.LocOf(node),
		}
		appendBlock(module, scope, loop.Body, node.Body, returnType, ctx)
		out.Stmts = append(out.Stmts, loop)

	case *ast.ReturnStmt:
		if node.Value == nil {
			out.Stmts = append(out.Stmts, &hir.Return{NodeID: hir.NodeID(node.ID()), Location: ast.LocOf(node)})
			return
		}
		valueExpr := lowerASTExpr(ctx, module, scope, node.Value, returnType)
		out.Stmts = append(out.Stmts, &hir.Return{Value: valueExpr, NodeID: hir.NodeID(node.ID()), Location: ast.LocOf(node)})

	case *ast.ExprStmt:
		if node.Expr == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "expression statement missing expression", NodeID: hir.NodeID(node.ID()), Location: ast.LocOf(node)})
			return
		}
		valueExpr := lowerASTExpr(ctx, module, scope, node.Expr, nil)
		out.Stmts = append(out.Stmts, &hir.ExprStmt{Value: valueExpr, NodeID: hir.NodeID(node.ID()), ValueNodeID: hir.NodeID(node.Expr.ID()), Location: ast.LocOf(node)})
	case *ast.AssignStmt:
		if node.Target == nil || node.Value == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "assignment missing target or value", NodeID: hir.NodeID(node.ID()), Location: ast.LocOf(node)})
			return
		}
		targetExpr := lowerPlace(ctx, module, scope, node.Target)
		targetType := exprResolvedType(module, node.Target)
		valueExpr := lowerASTExpr(ctx, module, scope, node.Value, targetType)
		out.Stmts = append(out.Stmts, &hir.Assign{Target: targetExpr, Value: valueExpr, NodeID: hir.NodeID(node.ID()), Location: ast.LocOf(node)})
	case *ast.BadStmt, *ast.BadDecl, *ast.ImportDecl, *ast.FnDecl,
		*ast.TypeAliasDecl, *ast.StructDecl, *ast.InterfaceDecl, *ast.EnumDecl:
		out.Stmts = append(out.Stmts, &hir.Invalid{Message: "unsupported statement", NodeID: hir.NodeID(node.ID()), Location: ast.LocOf(node)})
	default:
		panic(fmt.Sprintf("HIR lowering: unhandled statement %T", stmt))
	}
}

func lowerPlace(ctx *project.CompilerContext, module *project.Module, scope *symbols.Scope, expr ast.Expr) *ir.Place {
	if selector, ok := expr.(*ast.SelectorExpr); ok && selector != nil && selector.Expr != nil && selector.Name != nil {
		baseType := exprResolvedType(module, selector.Expr)
		if field, fieldIndex, ok := typeinfo.LookupStructField(loweredRuntimeType(module, baseType, nil), selector.Name.Name); ok {
			out := lowerPlace(ctx, module, scope, selector.Expr)
			runtimeBase := typeinfo.Underlying(baseType)
			target, indirect := typeinfo.PointerTarget(runtimeBase)
			if !indirect {
				target, _, indirect = typeinfo.ReferenceTarget(runtimeBase)
			}
			if indirect {
				out.Projections = append(out.Projections, ir.PlaceProjection{
					Kind: ir.PlaceProjectionDeref, Type: loweredTypeID(ctx, module, target), Location: ast.LocOf(selector.Expr),
				})
			}
			out.Projections = append(out.Projections, ir.PlaceProjection{
				Kind: ir.PlaceProjectionField, FieldIndex: fieldIndex,
				Type: loweredTypeID(ctx, module, field.Type), Location: ast.LocOf(selector),
			})
			out.Type = loweredTypeID(ctx, module, field.Type)
			out.Location = ast.LocOf(selector)
			return out
		}
	}
	if index, ok := expr.(*ast.IndexExpr); ok && index != nil && index.Expr != nil && index.Index != nil {
		if _, slicing := index.Index.(*ast.RangeExpr); !slicing {
			indexExpr := lowerASTExpr(ctx, module, scope, index.Index, typeinfo.DefaultIntegerType())
			if value, ok := consteval.EvaluateExpr(ctx, module, scope, index.Index, typeinfo.DefaultIntegerType()); ok {
				if intConst, ok := value.(*constvalue.IntConst); ok && intConst != nil {
					indexType, ok := ctx.Types.LookupText(intConst.TypeText())
					if ok {
						indexExpr = &ir.IntLit{Value: intConst.Text(), Type: indexType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(index.Index)}}
					}
				}
			}
			out := lowerPlace(ctx, module, scope, index.Expr)
			out.Type = loweredTypeID(ctx, module, exprResolvedType(module, index))
			out.Location = ast.LocOf(index)
			out.Projections = append(out.Projections, ir.PlaceProjection{
				Kind: ir.PlaceProjectionIndex, Index: indexExpr, Type: out.Type, Location: ast.LocOf(index),
			})
			return out
		}
	}
	typeText := loweredTypeID(ctx, module, exprResolvedType(module, expr))
	return &ir.Place{
		Root: lowerASTExpr(ctx, module, scope, expr, nil), Type: typeText, Location: ast.LocOf(expr),
	}
}

func lowerReferenceValue(ctx *project.CompilerContext, module *project.Module, scope *symbols.Scope, expr ast.Expr, resultType typeinfo.Type, typeID ir.TypeID) ir.Expr {
	target, _, reference := typeinfo.ReferenceTarget(typeinfo.Underlying(resultType))
	if !reference {
		return &ir.InvalidExpr{Message: "reference lowering requires reference type", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(expr)}}
	}
	borrowAsView := false
	switch runtimeTarget := loweredRuntimeType(module, target, nil).(type) {
	case *typeinfo.StringType:
		borrowAsView = true
	case *typeinfo.ArrayType:
		borrowAsView = runtimeTarget.Shape == typeinfo.ArraySlice
	}
	exprType := func(node ast.Expr) typeinfo.Type {
		return exprResolvedType(module, node)
	}
	if !place.Addressable(scope, expr, exprType, expandedDefaultBindingResolver(module)) {
		return &ir.TempBorrow{
			Value:      lowerASTExpr(ctx, module, scope, expr, target),
			Slice:      borrowAsView,
			Type:       typeID,
			SourceInfo: ir.SourceInfo{Location: ast.LocOf(expr)},
		}
	}
	value := lowerPlace(ctx, module, scope, expr)
	if borrowAsView {
		return &ir.SliceView{Source: value, Type: typeID, SourceInfo: ir.SourceInfo{Location: ast.LocOf(expr)}}
	}
	return &ir.AddrOf{Place: value, Type: typeID, SourceInfo: ir.SourceInfo{Location: ast.LocOf(expr)}}
}

func lowerImplicitReferenceValue(ctx *project.CompilerContext, module *project.Module, scope *symbols.Scope, expr ast.Expr, resultType typeinfo.Type) ir.Expr {
	typeID := loweredTypeID(ctx, module, resultType)
	if _, _, borrowed := typeinfo.ReferenceTarget(typeinfo.Underlying(exprResolvedType(module, expr))); borrowed {
		return lowerASTExpr(ctx, module, scope, expr, nil)
	}
	return lowerReferenceValue(ctx, module, scope, expr, resultType, typeID)
}

func lowerElse(module *project.Module, scope *symbols.Scope, stmt ast.Stmt, returnType typeinfo.Type, ctx *project.CompilerContext) hir.Stmt {
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		block := &hir.Block{Stmts: make([]hir.Stmt, 0), NodeID: hir.NodeID(node.ID()), Location: ast.LocOf(node)}
		appendBlock(module, scope, block, node, returnType, ctx)
		return block
	case *ast.IfStmt:
		condExpr := ir.Expr(&ir.InvalidExpr{Message: "invalid condition", Type: ir.InvalidType})
		if node.Cond != nil {
			condExpr = lowerASTExpr(ctx, module, scope, node.Cond, &typeinfo.BoolType{})
		}
		out := &hir.If{
			Cond:     condExpr,
			Then:     &hir.Block{Stmts: make([]hir.Stmt, 0), NodeID: hir.NodeID(node.Then.ID()), Location: ast.LocOf(node.Then)},
			NodeID:   hir.NodeID(node.ID()),
			Location: ast.LocOf(node),
		}
		appendBlock(module, scope, out.Then, node.Then, returnType, ctx)
		if node.Else != nil {
			out.Else = lowerElse(module, scope, node.Else, returnType, ctx)
		}
		return out
	case *ast.BadStmt, *ast.BadDecl, *ast.ImportDecl, *ast.FnDecl,
		*ast.TypeAliasDecl, *ast.StructDecl, *ast.InterfaceDecl, *ast.EnumDecl,
		*ast.LetDecl, *ast.ConstDecl, *ast.ReturnStmt, *ast.ExprStmt, *ast.AssignStmt, *ast.ForStmt:
		return &hir.Invalid{Message: "unsupported else branch", NodeID: hir.NodeID(node.ID()), Location: ast.LocOf(node)}
	case nil:
		return &hir.Invalid{Message: "unsupported else branch"}
	default:
		panic(fmt.Sprintf("HIR lowering: unhandled else statement %T", stmt))
	}
}

// lowerASTExpr directly lowers an AST expression to an IR expression using
// the module context's resolved expression types side-table.
func lowerASTExpr(ctx *project.CompilerContext, module *project.Module, scope *symbols.Scope, expr ast.Expr, expectedType typeinfo.Type) (result ir.Expr) {
	if expr == nil {
		return &ir.InvalidExpr{Message: "nil expression", Type: ir.InvalidType}
	}
	loc := ast.LocOf(expr)
	defer func() {
		result = ir.WithOrigin(result, ir.SourceInfo{NodeID: ir.NodeID(expr.ID()), Location: loc})
	}()

	// Fetch canonical type from the typechecker side-table when available.
	resolvedType := exprResolvedType(module, expr)
	resolvedTypeID := ir.InvalidType
	if resolvedType != nil {
		resolvedTypeID = loweredTypeID(ctx, module, resolvedType)
	}
	if innerExpected := optionalSomeInnerType(module, expectedType, resolvedType, expr); innerExpected != nil {
		return &ir.OptionalSome{
			Value:      lowerASTExpr(ctx, module, scope, expr, innerExpected),
			Type:       loweredTypeID(ctx, module, expectedType),
			SourceInfo: ir.SourceInfo{Location: loc},
		}
	}
	if ifaceExpr := maybeLowerInterfaceExpr(ctx, module, scope, expr, expectedType); ifaceExpr != nil {
		return ifaceExpr
	}
	if expectedType != nil && resolvedType != nil && !typeinfo.SameType(expectedType, resolvedType) &&
		typeinfo.CheckNumericCompatibility(expectedType, resolvedType) == typeinfo.Compatible {
		value := lowerASTExpr(ctx, module, scope, expr, nil)
		return &ir.Cast{Expr: value, Type: loweredTypeID(ctx, module, expectedType), SourceInfo: ir.SourceInfo{Location: loc}}
	}
	expectedTypeID := loweredTypeID(ctx, module, expectedType)

	switch node := expr.(type) {
	case *ast.NumberLit:
		t := resolvedType
		if t == nil {
			t = expectedType
		}
		return lowerNumberLit(ctx, module, node, t, loc)

	case *ast.StringLit:
		t := resolvedTypeID
		if t == ir.InvalidType {
			if node.CString {
				t = loweredTypeID(ctx, module, &typeinfo.CStrType{})
			} else {
				t = loweredTypeID(ctx, module, &typeinfo.StringType{})
			}
		}
		return &ir.StringLit{Value: node.Value, Type: t, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.ByteLit:
		return &ir.IntLit{Value: fmt.Sprintf("%d", node.Value[0]), Type: loweredTypeID(ctx, module, &typeinfo.ByteType{}), SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.CharLit:
		runeValue, _ := utf8.DecodeRuneInString(node.Value)
		return &ir.IntLit{Value: fmt.Sprintf("%d", runeValue), Type: loweredTypeID(ctx, module, &typeinfo.CharType{}), SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.BoolLit:
		return &ir.BoolLit{Value: node.Value, Type: loweredTypeID(ctx, module, &typeinfo.BoolType{}), SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.NoneLit:
		if none := lowerOptionalNone(ctx, expectedTypeID, loc); none != nil {
			return none
		}
		return &ir.InvalidExpr{Message: "`none` requires optional context", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.Ident:
		var sym *symbols.Symbol
		var ok bool
		if module != nil && module.Semantics != nil {
			sym = module.Semantics.ResolvedSymbols[node.ID()]
			ok = sym != nil
		}
		if !ok {
			sym, ok = scope.Lookup(node.Name)
		}
		if !ok || sym == nil {
			return &ir.InvalidExpr{Message: "unresolved identifier: " + node.Name, Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: loc}}
		}
		t := resolvedTypeID
		if t == ir.InvalidType {
			if symType, ok := symbols.GetSymbolType(sym); ok {
				t = loweredTypeID(ctx, module, symType)
			} else {
				t = ir.InvalidType
			}
		}
		return &ir.Ident{Name: symbolName(module, sym), Type: t, SymbolID: sym.ID, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.ScopeResolution:
		var sym *symbols.Symbol
		if module != nil && module.Semantics != nil {
			sym = module.Semantics.ResolvedSymbols[node.ID()]
		}
		if sym == nil {
			if resolved, ok := project.LookupImportedSymbol(ctx, module, node.Module.Name, node.Name.Name); ok {
				sym = resolved.Symbol
			}
		}
		if sym != nil {
			t := resolvedTypeID
			if t == ir.InvalidType {
				if symType, ok := symbols.GetSymbolType(sym); ok {
					t = loweredTypeID(ctx, module, symType)
				} else {
					t = ir.InvalidType
				}
			}
			return &ir.Ident{Name: symbolName(module, sym), Type: t, SymbolID: sym.ID, SourceInfo: ir.SourceInfo{Location: loc}}
		}
		return &ir.InvalidExpr{Message: "unresolved qualified identifier: " + node.Module.Name + "::" + node.Name.Name, Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.UnaryExpr:
		arg := lowerASTExpr(ctx, module, scope, node.Expr, expectedType)
		t := resolvedTypeID
		if t == ir.InvalidType {
			t = arg.TypeID()
			if node.Op == "!" {
				t = loweredTypeID(ctx, module, &typeinfo.BoolType{})
			}
		}
		return &ir.Unary{Op: node.Op, Arg: arg, Type: t, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.AddressExpr:
		t := resolvedTypeID
		if t == ir.InvalidType {
			valueType := loweredTypeID(ctx, module, exprResolvedType(module, node.Expr))
			switch node.Mode {
			case ast.AddressShared:
				t = ctx.Types.Intern(ir.Type{Kind: ir.TypeReference, Elem: valueType})
			case ast.AddressMutable:
				t = ctx.Types.Intern(ir.Type{Kind: ir.TypeReference, Mutable: true, Elem: valueType})
			default:
				t = ctx.Types.Intern(ir.Type{Kind: ir.TypeRawPtr})
			}
		}
		if node.Mode == ast.AddressShared || node.Mode == ast.AddressMutable {
			return lowerReferenceValue(ctx, module, scope, node.Expr, resolvedType, t)
		}
		return &ir.AddrOf{Place: lowerPlace(ctx, module, scope, node.Expr), Type: t, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.BinaryExpr:
		leftExpected := expectedType
		rightExpected := expectedType
		leftType := exprResolvedType(module, node.Left)
		rightType := exprResolvedType(module, node.Right)
		if node.Op == "<<" || node.Op == ">>" {
			rightExpected = rightType
		} else if common := typeinfo.CommonNumericType(leftType, rightType); common != nil {
			leftExpected = common
			rightExpected = common
		}
		switch node.Op {
		case "==", "!=", "<", "<=", ">", ">=", "&&", "||":
			if leftExpected == nil {
				leftExpected = leftType
			}
			if rightExpected == nil {
				rightExpected = rightType
			}
			if _, ok := node.Left.(*ast.NoneLit); ok && rightExpected != nil {
				leftExpected = rightExpected
			}
			if _, ok := node.Right.(*ast.NoneLit); ok && leftExpected != nil {
				rightExpected = leftExpected
			}
		}
		var left, right ir.Expr
		if _, none := node.Left.(*ast.NoneLit); none {
			right = lowerASTExpr(ctx, module, scope, node.Right, rightExpected)
			left = lowerOptionalNone(ctx, right.TypeID(), ast.LocOf(node.Left))
		} else {
			left = lowerASTExpr(ctx, module, scope, node.Left, leftExpected)
		}
		if _, none := node.Right.(*ast.NoneLit); none {
			right = lowerOptionalNone(ctx, left.TypeID(), ast.LocOf(node.Right))
		} else {
			right = lowerASTExpr(ctx, module, scope, node.Right, rightExpected)
		}
		t := resolvedTypeID
		if t == ir.InvalidType {
			t = left.TypeID()
			switch node.Op {
			case "==", "!=", "<", "<=", ">", ">=", "&&", "||":
				t = loweredTypeID(ctx, module, &typeinfo.BoolType{})
			}
		}
		return &ir.Binary{Op: node.Op, Left: left, Right: right, Type: t, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.CallExpr:
		if compilerCall, ok := module.Semantics.CompilerCalls[node.ID()]; ok {
			switch compilerCall.Kind {
			case intrinsics.FunctionAlloc:
				return lowerAllocCall(ctx, module, scope, node)
			case intrinsics.FunctionCollection:
				return lowerCollectionCall(ctx, module, scope, node, compilerCall.Operation)
			case intrinsics.FunctionDynamicArrayOwner:
				return lowerDynamicArrayOwnerCall(ctx, module, scope, node, compilerCall.Operation)
			default:
				panic(fmt.Sprintf("unsupported intrinsic function kind %d for %q", compilerCall.Kind, compilerCall.Operation))
			}
		}
		if selector, ok := node.Callee.(*ast.SelectorExpr); ok && selector != nil {
			return lowerSelectorMethodCall(ctx, module, scope, selector, node)
		}
		calleeExpr := lowerASTExpr(ctx, module, scope, node.Callee, nil)
		args := make([]ir.Expr, 0, len(node.Args))
		var fnType *typeinfo.FuncType
		if resolved := exprResolvedType(module, node.Callee); resolved != nil {
			fnType, _ = typeinfo.Underlying(resolved).(*typeinfo.FuncType)
		}
		for _, arg := range node.Args {
			var paramExpected typeinfo.Type
			if fnType != nil && len(args) < len(fnType.Params) {
				paramExpected = fnType.Params[len(args)]
			}
			if implicit := module.Semantics.ImplicitCallArguments[arg.ID()]; implicit != nil {
				args = append(args, lowerImplicitReferenceValue(ctx, module, scope, arg, implicit))
			} else {
				args = append(args, lowerASTExpr(ctx, module, scope, arg, paramExpected))
			}
		}
		t := resolvedTypeID
		if t == ir.InvalidType {
			var sym *symbols.Symbol
			switch callee := node.Callee.(type) {
			case *ast.Ident:
				if s, ok := scope.Lookup(callee.Name); ok {
					sym = s
				}
			case *ast.ScopeResolution:
				if resolved, ok := project.LookupImportedSymbol(ctx, module, callee.Module.Name, callee.Name.Name); ok && resolved.Symbol != nil {
					sym = resolved.Symbol
				}
			}
			if sym != nil {
				if fnType, ok := sym.Type.(*typeinfo.FuncType); ok && fnType != nil {
					t = loweredReturnTypeID(ctx, module, fnType.Return)
				}
			}
		}
		return &ir.Call{Callee: calleeExpr, Args: args, Type: t, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.PrintExpr:
		return &ir.Print{Value: lowerASTExpr(ctx, module, scope, node.Expr, nil), Newline: node.Newline, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.FreeExpr:
		return &ir.Drop{Value: lowerASTExpr(ctx, module, scope, node.Expr, nil), SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.AsExpr:
		t := resolvedTypeID
		if t == ir.InvalidType {
			t = loweredTypeID(ctx, module, typeinfo.TypeFromSyntax(node.TypeExpr, typeinfo.SyntaxOptions{Target: ctx.Target, AllowAbstractSelf: true}))
		}
		subExpr := lowerASTExpr(ctx, module, scope, node.Expr, expectedType)
		return &ir.Cast{Expr: subExpr, Type: t, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.SelectorExpr:
		return lowerSelectorExpr(ctx, module, scope, node)

	case *ast.IndexExpr:
		return lowerIndexExpr(ctx, module, scope, node)

	case *ast.StructLit:
		return lowerStructLiteralExpr(ctx, module, scope, node)

	case *ast.ArrayLit:
		return lowerArrayLiteralExpr(ctx, module, scope, node)

	case *ast.BadExpr:
		return &ir.InvalidExpr{Message: "unsupported expression", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: loc}}

	default:
		panic(fmt.Sprintf("HIR lowering: unhandled expression %T", expr))
	}
}

func lowerCollectionCall(ctx *project.CompilerContext, module *project.Module, scope *symbols.Scope, call *ast.CallExpr, op symbols.CompilerOp) ir.Expr {
	fnType, _ := exprResolvedType(module, call.Callee).(*typeinfo.FuncType)
	if fnType == nil || len(fnType.Params) != 1 {
		return &ir.InvalidExpr{Message: "collection function type missing", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(call)}}
	}
	value := call.Args[0]
	var receiver ir.Expr
	if implicit := module.Semantics.ImplicitCallArguments[value.ID()]; implicit != nil {
		receiver = lowerImplicitReferenceValue(ctx, module, scope, value, implicit)
	} else {
		receiver = lowerASTExpr(ctx, module, scope, value, fnType.Params[0])
	}
	switch op {
	case symbols.CompilerOpLen:
		return &ir.Len{Value: receiver, Type: loweredReturnTypeID(ctx, module, fnType.Return), SourceInfo: ir.SourceInfo{Location: ast.LocOf(call)}}
	case symbols.CompilerOpAsBytes:
		return &ir.SliceView{
			Source:     &ir.Place{Root: receiver, Type: receiver.TypeID(), Location: ast.LocOf(value)},
			Type:       loweredReturnTypeID(ctx, module, fnType.Return),
			SourceInfo: ir.SourceInfo{Location: ast.LocOf(call)},
		}
	case symbols.CompilerOpAsChars:
		return &ir.StringChars{
			Value:      receiver,
			Type:       loweredReturnTypeID(ctx, module, fnType.Return),
			SourceInfo: ir.SourceInfo{Location: ast.LocOf(call)},
		}
	default:
		return &ir.InvalidExpr{Message: "unsupported collection function lowering", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(call)}}
	}
}

func lowerOptionalNone(ctx *project.CompilerContext, typeID ir.TypeID, loc *source.Location) ir.Expr {
	if ctx == nil || ctx.Types == nil {
		return nil
	}
	typ, ok := ctx.Types.Type(typeID)
	if !ok || typ.Kind != ir.TypeOptional {
		return nil
	}
	return &ir.ZeroValue{Type: typeID, SourceInfo: ir.SourceInfo{Location: loc}}
}

func optionalSomeInnerType(module *project.Module, expectedType, resolvedType typeinfo.Type, expr ast.Expr) typeinfo.Type {
	if expectedType == nil || resolvedType == nil || expr == nil {
		return nil
	}
	if _, ok := expr.(*ast.NoneLit); ok {
		return nil
	}
	expected, ok := loweredRuntimeType(module, expectedType, nil).(*typeinfo.OptionalType)
	if !ok || expected == nil || expected.Inner == nil {
		return nil
	}
	// Typechecker accepts T in ?T contexts. HIR must keep the source expr at
	// type T and add the optional container explicitly so MIR/LLVM can choose
	// tagged or niche ABI later.
	switch loweredRuntimeType(module, resolvedType, nil).(type) {
	case *typeinfo.OptionalType, *typeinfo.NoneType:
		return nil
	default:
		return expected.Inner
	}
}

func lowerSelectorMethodCall(ctx *project.CompilerContext, module *project.Module, scope *symbols.Scope, selector *ast.SelectorExpr, call *ast.CallExpr) ir.Expr {
	if module == nil || selector == nil || selector.Expr == nil || selector.Name == nil {
		return &ir.InvalidExpr{Message: "invalid selector call", Type: ir.InvalidType}
	}
	baseType := exprResolvedType(module, selector.Expr)
	if iface, slot, ok := lookupInterfaceMethod(module, baseType, selector.Name.Name); ok {
		args := make([]ir.Expr, 0, len(call.Args))
		for i, arg := range call.Args {
			var argExpected typeinfo.Type
			if i+1 < len(iface.Params) {
				argExpected = iface.Params[i+1].Type
			}
			args = append(args, lowerASTExpr(ctx, module, scope, arg, argExpected))
		}
		consumes := false
		if len(iface.Params) > 0 {
			_, _, borrowedReceiver := typeinfo.ReferenceTarget(typeinfo.Underlying(iface.Params[0].Type))
			consumes = !borrowedReceiver
		}
		return &ir.InterfaceCall{
			Base:       lowerASTExpr(ctx, module, scope, selector.Expr, nil),
			Slot:       slot,
			Args:       args,
			Consumes:   consumes,
			Type:       loweredReturnTypeID(ctx, module, iface.Return),
			SourceInfo: ir.SourceInfo{Location: ast.LocOf(call)},
		}
	}
	methodSym := module.Semantics.ResolvedSymbols[selector.Name.ID()]
	fnType, _ := module.Semantics.ExprTypes[selector.ID()].(*typeinfo.FuncType)
	if methodSym == nil || fnType == nil || len(fnType.Params) == 0 {
		return &ir.InvalidExpr{Message: "unsupported selector call lowering", Type: ir.InvalidType}
	}
	if _, ok := typeinfo.ReceiverTarget(fnType.Params[0]); !ok {
		return &ir.InvalidExpr{Message: "selector method receiver missing", Type: ir.InvalidType}
	}
	var baseExpr ir.Expr
	if implicit := module.Semantics.ImplicitCallArguments[selector.Expr.ID()]; implicit != nil {
		baseExpr = lowerImplicitReferenceValue(ctx, module, scope, selector.Expr, implicit)
	} else {
		baseExpr = lowerASTExpr(ctx, module, scope, selector.Expr, nil)
	}
	args := make([]ir.Expr, 0, len(call.Args)+1)
	args = append(args, baseExpr)
	for i, arg := range call.Args {
		var argExpected typeinfo.Type
		if i+1 < len(fnType.Params) {
			argExpected = fnType.Params[i+1]
		}
		args = append(args, lowerASTExpr(ctx, module, scope, arg, argExpected))
	}
	return &ir.Call{
		Callee: &ir.Ident{
			Name:       symbolName(module, methodSym),
			Type:       loweredTypeID(ctx, module, fnType),
			SymbolID:   methodSym.ID,
			SourceInfo: ir.SourceInfo{Location: ast.LocOf(selector.Name)},
		},
		Args:       args,
		Type:       loweredReturnTypeID(ctx, module, fnType.Return),
		SourceInfo: ir.SourceInfo{Location: ast.LocOf(call)},
	}
}

func lowerSelectorExpr(ctx *project.CompilerContext, module *project.Module, scope *symbols.Scope, selector *ast.SelectorExpr) ir.Expr {
	if module == nil || selector == nil || selector.Expr == nil || selector.Name == nil {
		return &ir.InvalidExpr{Message: "invalid selector", Type: ir.InvalidType}
	}
	baseType := exprResolvedType(module, selector.Expr)
	if field, fieldIndex, ok := typeinfo.LookupStructField(loweredRuntimeType(module, baseType, nil), selector.Name.Name); ok {
		_, throughPtr := typeinfo.PointerTarget(baseType)
		if !throughPtr {
			_, _, throughPtr = typeinfo.ReferenceTarget(typeinfo.Underlying(baseType))
		}
		exprType := func(expr ast.Expr) typeinfo.Type {
			return exprResolvedType(module, expr)
		}
		if throughPtr || place.Addressable(scope, selector.Expr, exprType, expandedDefaultBindingResolver(module)) {
			return &ir.Load{Place: lowerPlace(ctx, module, scope, selector), SourceInfo: ir.SourceInfo{NodeID: ir.NodeID(selector.ID()), Location: ast.LocOf(selector)}}
		}
		return &ir.Field{
			Base:       lowerASTExpr(ctx, module, scope, selector.Expr, nil),
			Index:      fieldIndex,
			SourceInfo: ir.SourceInfo{NodeID: ir.NodeID(selector.ID()), Location: ast.LocOf(selector)},
			Type:       loweredTypeID(ctx, module, field.Type),
		}
	}
	return &ir.InvalidExpr{Message: "selector lowering not implemented", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(selector)}}
}

func lowerIndexExpr(ctx *project.CompilerContext, module *project.Module, scope *symbols.Scope, node *ast.IndexExpr) ir.Expr {
	if module == nil || node == nil || node.Expr == nil || node.Index == nil {
		return &ir.InvalidExpr{Message: "invalid index", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)}}
	}
	if rangeIndex, ok := node.Index.(*ast.RangeExpr); ok && rangeIndex != nil {
		var start, end ir.Expr
		if rangeIndex.Start != nil {
			start = lowerASTExpr(ctx, module, scope, rangeIndex.Start, typeinfo.DefaultIntegerType())
		}
		if rangeIndex.End != nil {
			end = lowerASTExpr(ctx, module, scope, rangeIndex.End, typeinfo.DefaultIntegerType())
		}
		resultType := exprResolvedType(module, node)
		source := lowerPlace(ctx, module, scope, node.Expr)
		if target, _, reference := typeinfo.ReferenceTarget(typeinfo.Underlying(resultType)); reference {
			if _, stringRange := typeinfo.Underlying(target).(*typeinfo.StringType); stringRange {
				root := lowerImplicitReferenceValue(ctx, module, scope, node.Expr, resultType)
				source = &ir.Place{Root: root, Type: root.TypeID(), Location: ast.LocOf(node.Expr)}
			}
		}
		return &ir.SliceView{
			Source:       source,
			Start:        start,
			End:          end,
			EndExclusive: rangeIndex.EndExclusive,
			Type:         loweredTypeID(ctx, module, resultType),
			SourceInfo:   ir.SourceInfo{Location: ast.LocOf(node)},
		}
	}
	return &ir.Load{Place: lowerPlace(ctx, module, scope, node), SourceInfo: ir.SourceInfo{NodeID: ir.NodeID(node.ID()), Location: ast.LocOf(node)}}
}

func lowerStructLiteralExpr(ctx *project.CompilerContext, module *project.Module, scope *symbols.Scope, node *ast.StructLit) ir.Expr {
	if module == nil || node == nil {
		return &ir.InvalidExpr{Message: "invalid struct literal", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)}}
	}
	resolved := exprResolvedType(module, node)
	strct, ok := loweredRuntimeType(module, resolved, nil).(*typeinfo.StructType)
	if !ok || strct == nil {
		return &ir.InvalidExpr{Message: "struct literal type missing", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)}}
	}
	fieldsByName := make(map[string]ast.Expr, len(node.Fields))
	for _, field := range node.Fields {
		if field.Name == nil || field.Value == nil {
			continue
		}
		fieldsByName[field.Name.Name] = field.Value
	}
	values := make([]ir.Expr, 0, len(strct.Fields))
	for _, field := range strct.Fields {
		value, ok := fieldsByName[field.Name]
		if !ok {
			return &ir.InvalidExpr{Message: "struct literal field missing during lowering", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)}}
		}
		values = append(values, lowerASTExpr(ctx, module, scope, value, field.Type))
	}
	return &ir.StructLit{
		Fields:     values,
		Type:       loweredTypeID(ctx, module, resolved),
		SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)},
	}
}

func lowerArrayLiteralExpr(ctx *project.CompilerContext, module *project.Module, scope *symbols.Scope, node *ast.ArrayLit) ir.Expr {
	if module == nil || node == nil {
		return &ir.InvalidExpr{Message: "invalid array literal", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)}}
	}
	resolved := exprResolvedType(module, node)
	array, ok := loweredRuntimeType(module, resolved, nil).(*typeinfo.ArrayType)
	if !ok || array == nil || array.Elem == nil {
		return &ir.InvalidExpr{Message: "array literal type missing", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)}}
	}
	values := make([]ir.Expr, 0, len(node.Values))
	for _, value := range node.Values {
		values = append(values, lowerASTExpr(ctx, module, scope, value, array.Elem))
	}
	return &ir.ArrayLit{
		Values:     values,
		Dynamic:    array.Shape == typeinfo.ArrayOwner,
		Type:       loweredTypeID(ctx, module, resolved),
		SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)},
	}
}

func lowerDynamicArrayOwnerCall(ctx *project.CompilerContext, module *project.Module, scope *symbols.Scope, node *ast.CallExpr, op symbols.CompilerOp) ir.Expr {
	fnType, _ := typeinfo.Underlying(exprResolvedType(module, node.Callee)).(*typeinfo.FuncType)
	if fnType == nil || len(fnType.Params) != len(node.Args) || len(node.Args) < 2 {
		return &ir.InvalidExpr{Message: "dynamic-array operation type missing", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)}}
	}
	args := make([]ir.Expr, 0, len(node.Args))
	for i, arg := range node.Args {
		if implicit := module.Semantics.ImplicitCallArguments[arg.ID()]; implicit != nil {
			args = append(args, lowerImplicitReferenceValue(ctx, module, scope, arg, implicit))
		} else {
			args = append(args, lowerASTExpr(ctx, module, scope, arg, fnType.Params[i]))
		}
	}
	ownerType, _, referenced := typeinfo.ReferenceTarget(typeinfo.Underlying(fnType.Params[0]))
	if !referenced {
		return &ir.InvalidExpr{Message: "dynamic-array owner reference missing", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)}}
	}
	out := &ir.DynamicArrayOp{
		Op:         op,
		Array:      args[0],
		ArrayType:  loweredTypeID(ctx, module, ownerType),
		Type:       loweredReturnTypeID(ctx, module, nil),
		SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)},
	}
	switch op {
	case symbols.CompilerOpAppend:
		out.Value = args[1]
	case symbols.CompilerOpReserve, symbols.CompilerOpShrink:
		out.Length = args[1]
	case symbols.CompilerOpResize:
		if len(args) != 3 {
			return &ir.InvalidExpr{Message: "resize operation arguments missing", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)}}
		}
		out.Length = args[1]
		out.Value = args[2]
	default:
		panic(fmt.Sprintf("unsupported dynamic-array compiler operation %q", op))
	}
	return out
}

func lowerAllocCall(ctx *project.CompilerContext, module *project.Module, scope *symbols.Scope, node *ast.CallExpr) ir.Expr {
	if len(node.Args) < 1 || len(node.Args) > 2 {
		return &ir.InvalidExpr{Message: "alloc requires 1 or 2 arguments", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)}}
	}
	value := lowerASTExpr(ctx, module, scope, node.Args[0], nil)
	var allocator ir.Expr
	if len(node.Args) > 1 {
		allocator = lowerASTExpr(ctx, module, scope, node.Args[1], &typeinfo.AllocatorType{})
	}
	resultType := loweredTypeID(ctx, module, exprResolvedType(module, node))
	return &ir.AllocExpr{
		Value:      value,
		Allocator:  allocator,
		Type:       resultType,
		SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)},
	}
}

func exprResolvedType(module *project.Module, expr ast.Expr) typeinfo.Type {
	if module == nil || module.Semantics == nil || expr == nil {
		return nil
	}
	return module.Semantics.ExprTypes[expr.ID()]
}

func lowerNumberLit(ctx *project.CompilerContext, module *project.Module, node *ast.NumberLit, expectedType typeinfo.Type, loc *source.Location) ir.Expr {
	if node == nil {
		return &ir.InvalidExpr{Message: "nil number literal", Type: ir.InvalidType}
	}
	integerValue := node.Value
	if !numeric.IsFloat(node.Value) {
		if canonical, err := numeric.CanonicalizeIntegerLiteral(node.Value); err == nil {
			integerValue = canonical
		}
	}
	if expectedType == nil || typeinfo.IsInvalidOrUnknown(expectedType) {
		// No expected type - use language default.
		if numeric.IsFloat(node.Value) {
			return &ir.FloatLit{Value: node.Value, Type: loweredTypeID(ctx, module, typeinfo.DefaultNumberType(node.Value)), SourceInfo: ir.SourceInfo{Location: loc}}
		}
		return &ir.IntLit{Value: integerValue, Type: loweredTypeID(ctx, module, typeinfo.DefaultNumberType(node.Value)), SourceInfo: ir.SourceInfo{Location: loc}}
	}
	family, _, numericType := typeinfo.NumericInfo(expectedType)
	if numericType && family == typeinfo.NumericFloat {
		v := node.Value
		if !numeric.IsFloat(node.Value) {
			v = integerValue + ".0"
		}
		return &ir.FloatLit{Value: v, Type: loweredTypeID(ctx, module, expectedType), SourceInfo: ir.SourceInfo{Location: loc}}
	}
	return &ir.IntLit{Value: integerValue, Type: loweredTypeID(ctx, module, expectedType), SourceInfo: ir.SourceInfo{Location: loc}}
}

func symbolName(module *project.Module, sym *symbols.Symbol) string {
	if sym == nil {
		return ""
	}
	if sym.CompilerOp == "" && (sym.Kind == symbols.SymbolFunc || sym.Kind == symbols.SymbolMethod) {
		name, external := callableName(module, sym)
		if external {
			return name
		}
		return fmt.Sprintf("%s$%d", name, sym.ID)
	}
	return fmt.Sprintf("%s$%d", sym.Name, sym.ID)
}

func callableName(module *project.Module, sym *symbols.Symbol) (string, bool) {
	if sym == nil || (sym.Kind != symbols.SymbolFunc && sym.Kind != symbols.SymbolMethod) {
		return "", false
	}
	if fn, ok := sym.ASTNode.(*ast.FnDecl); ok {
		if name, external := ast.FunctionLinkName(fn, sym.Name); external {
			return name, true
		}
	}
	if module != nil && module.IsEntry && sym.Kind == symbols.SymbolFunc && sym.Name == "main" && sym.DefiningModule == module.DefiningModuleKey() {
		return "main", false
	}
	receiver := ""
	if sym.Kind == symbols.SymbolMethod {
		if typ, ok := symbols.GetSymbolType(sym); ok {
			if fnType, ok := typ.(*typeinfo.FuncType); ok && fnType != nil && len(fnType.Params) > 0 {
				if target, ok := typeinfo.ReceiverTarget(fnType.Params[0]); ok {
					receiver = typeinfo.TypeText(target)
				}
			}
		}
	}
	components := [...]string{
		sym.DefiningModule.Origin,
		sym.DefiningModule.Namespace,
		sym.DefiningModule.Dependency,
		sym.DefiningModule.ImportPath,
		string(sym.Kind),
		sym.Name,
		receiver,
	}
	var b strings.Builder
	b.WriteString("__peeper_callable_")
	for _, component := range components {
		b.WriteString(strconv.Itoa(len(component)))
		b.WriteByte('_')
		b.WriteString(hex.EncodeToString([]byte(component)))
		b.WriteByte('_')
	}
	return b.String(), false
}

func expandedDefaultBindingResolver(module *project.Module) place.BindingResolver {
	return func(ident *ast.Ident) (place.Binding, bool) {
		if module == nil || module.Semantics == nil || ident == nil {
			return place.Binding{}, false
		}
		if _, ok := module.Semantics.ExpandedDefaultBindings[ident.ID()]; !ok {
			return place.Binding{}, false
		}
		return place.Binding{Symbol: module.Semantics.ResolvedSymbols[ident.ID()]}, true
	}
}

func shouldDiscardBindingValue(sym *symbols.Symbol) bool {
	if sym == nil || sym.Used {
		return false
	}
	if typ, ok := symbols.GetSymbolType(sym); ok && typeinfo.NeedsDrop(typ) {
		return false
	}
	switch node := sym.ASTNode.(type) {
	case *ast.LetDecl:
		_, ok := node.Value.(*ast.CallExpr)
		return sym.Kind == symbols.SymbolVar && ok
	case *ast.ConstDecl:
		_, ok := node.Value.(*ast.CallExpr)
		return sym.Kind == symbols.SymbolConst && ok
	default:
		return false
	}
}
