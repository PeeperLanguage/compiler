package hir_lower

import (
	"fmt"
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
	"compiler/internal/semantics/table"
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
		emittedName := sym.Name
		if fn.Receiver != nil && resolvedFnType != nil && len(resolvedFnType.Params) > 0 {
			if target, ok := typeinfo.ReceiverTarget(resolvedFnType.Params[0]); ok {
				emittedName = methodFunctionName(typeinfo.TypeText(target), fn.Name.Name)
			}
		}
		if fn.Body == nil {
			if fn.Receiver == nil {
				emittedName = symbolName(sym)
			}
			params, returnType := lowerExternSignature(ctx, module, sym.Scope.(*table.Scope), fn.ParamsWithReceiver(), fn.ReturnType, resolvedFnType)
			if externName, ok := externSymbolName(sym, emittedName); ok {
				emittedName = externName
			}
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

func lowerExternSignature(ctx *project.CompilerContext, module *project.Module, scope *table.Scope, params []ast.Param, fallbackReturnType ast.TypeExpr, resolvedFnType *typeinfo.FuncType) ([]ir.Param, ir.TypeID) {
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
	funcScope := sym.Scope.(*table.Scope)
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
				name = symbolName(sym)
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

func appendBlock(module *project.Module, parentScope *table.Scope, out *hir.Block, block *ast.BlockStmt, returnType typeinfo.Type, ctx *project.CompilerContext) {
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

func appendStmt(module *project.Module, scope *table.Scope, out *hir.Block, stmt ast.Stmt, returnType typeinfo.Type, ctx *project.CompilerContext) {
	switch node := stmt.(type) {
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
		valueExpr := ir.Expr(&ir.InvalidExpr{Message: "missing initializer", Type: ir.InvalidType})
		if node.Value != nil {
			valueExpr = lowerASTExpr(ctx, module, scope, node.Value, sym.Type)
		}
		if shouldDiscardBindingValue(sym) {
			out.Stmts = append(out.Stmts, &hir.ExprStmt{Value: valueExpr, NodeID: hir.NodeID(node.ID()), ValueNodeID: hir.NodeID(node.Value.ID()), Location: ast.LocOf(node)})
			return
		}
		out.Stmts = append(out.Stmts, &hir.Binding{Name: symbolName(sym), Constant: false, Value: valueExpr, NodeID: hir.NodeID(node.ID()), SymbolID: sym.ID, Location: ast.LocOf(node)})

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
		out.Stmts = append(out.Stmts, &hir.Binding{Name: symbolName(sym), Constant: true, Value: valueExpr, NodeID: hir.NodeID(node.ID()), SymbolID: sym.ID, Location: ast.LocOf(node)})

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
	}
}

func lowerPlace(ctx *project.CompilerContext, module *project.Module, scope *table.Scope, expr ast.Expr) *ir.Place {
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
					indexType, ok := ctx.Types.LookupText(intConst.TypeID)
					if ok {
						indexExpr = &ir.IntLit{Value: intConst.Value, Type: indexType, Location: ast.LocOf(index.Index)}
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

func lowerReferenceValue(ctx *project.CompilerContext, module *project.Module, scope *table.Scope, expr ast.Expr, resultType typeinfo.Type, typeID ir.TypeID) ir.Expr {
	target, _, reference := typeinfo.ReferenceTarget(typeinfo.Underlying(resultType))
	if !reference {
		return &ir.InvalidExpr{Message: "reference lowering requires reference type", Type: ir.InvalidType, Location: ast.LocOf(expr)}
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
			Value:    lowerASTExpr(ctx, module, scope, expr, target),
			Slice:    borrowAsView,
			Type:     typeID,
			Location: ast.LocOf(expr),
		}
	}
	value := lowerPlace(ctx, module, scope, expr)
	if borrowAsView {
		return &ir.SliceView{Source: value, Type: typeID, Location: ast.LocOf(expr)}
	}
	return &ir.AddrOf{Place: value, Type: typeID, Location: ast.LocOf(expr)}
}

func lowerImplicitReferenceValue(ctx *project.CompilerContext, module *project.Module, scope *table.Scope, expr ast.Expr, resultType typeinfo.Type) ir.Expr {
	typeID := loweredTypeID(ctx, module, resultType)
	if _, _, borrowed := typeinfo.ReferenceTarget(typeinfo.Underlying(exprResolvedType(module, expr))); borrowed {
		return lowerASTExpr(ctx, module, scope, expr, nil)
	}
	return lowerReferenceValue(ctx, module, scope, expr, resultType, typeID)
}

func lowerElse(module *project.Module, scope *table.Scope, stmt ast.Stmt, returnType typeinfo.Type, ctx *project.CompilerContext) hir.Stmt {
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
	default:
		return &hir.Invalid{Message: "unsupported else branch", NodeID: hir.NodeID(node.ID()), Location: ast.LocOf(node)}
	}
}

// lowerASTExpr directly lowers an AST expression to an IR expression using
// the module context's resolved expression types side-table.
func lowerASTExpr(ctx *project.CompilerContext, module *project.Module, scope *table.Scope, expr ast.Expr, expectedType typeinfo.Type) (result ir.Expr) {
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
			Value:    lowerASTExpr(ctx, module, scope, expr, innerExpected),
			Type:     loweredTypeID(ctx, module, expectedType),
			Location: loc,
		}
	}
	if ifaceExpr := maybeLowerInterfaceExpr(ctx, module, scope, expr, expectedType); ifaceExpr != nil {
		return ifaceExpr
	}
	if expectedType != nil && resolvedType != nil && !typeinfo.SameType(expectedType, resolvedType) &&
		typeinfo.CheckNumericCompatibility(expectedType, resolvedType) == typeinfo.Compatible {
		value := lowerASTExpr(ctx, module, scope, expr, nil)
		return &ir.Cast{Expr: value, Type: loweredTypeID(ctx, module, expectedType), Location: loc}
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
		return &ir.StringLit{Value: node.Value, Type: t, Location: loc}

	case *ast.ByteLit:
		return &ir.IntLit{Value: fmt.Sprintf("%d", node.Value[0]), Type: loweredTypeID(ctx, module, &typeinfo.ByteType{}), Location: loc}

	case *ast.CharLit:
		runeValue, _ := utf8.DecodeRuneInString(node.Value)
		return &ir.IntLit{Value: fmt.Sprintf("%d", runeValue), Type: loweredTypeID(ctx, module, &typeinfo.CharType{}), Location: loc}

	case *ast.BoolLit:
		return &ir.BoolLit{Value: node.Value, Type: loweredTypeID(ctx, module, &typeinfo.BoolType{}), Location: loc}

	case *ast.NoneLit:
		if none := lowerOptionalNone(ctx, expectedTypeID, loc); none != nil {
			return none
		}
		return &ir.InvalidExpr{Message: "`none` requires optional context", Type: ir.InvalidType, Location: loc}

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
			return &ir.InvalidExpr{Message: "unresolved identifier: " + node.Name, Type: ir.InvalidType, Location: loc}
		}
		t := resolvedTypeID
		if t == ir.InvalidType {
			if symType, ok := symbols.GetSymbolType(sym); ok {
				t = loweredTypeID(ctx, module, symType)
			} else {
				t = ir.InvalidType
			}
		}
		return &ir.Ident{Name: symbolName(sym), Type: t, SymbolID: sym.ID, Location: loc}

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
			return &ir.Ident{Name: symbolName(sym), Type: t, SymbolID: sym.ID, Location: loc}
		}
		return &ir.InvalidExpr{Message: "unresolved qualified identifier: " + node.Module.Name + "::" + node.Name.Name, Type: ir.InvalidType, Location: loc}

	case *ast.UnaryExpr:
		arg := lowerASTExpr(ctx, module, scope, node.Expr, expectedType)
		t := resolvedTypeID
		if t == ir.InvalidType {
			t = arg.TypeID()
			if node.Op == "!" {
				t = loweredTypeID(ctx, module, &typeinfo.BoolType{})
			}
		}
		return &ir.Unary{Op: node.Op, Arg: arg, Type: t, Location: loc}

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
		return &ir.AddrOf{Place: lowerPlace(ctx, module, scope, node.Expr), Type: t, Location: loc}

	case *ast.BinaryExpr:
		leftExpected := expectedType
		rightExpected := expectedType
		leftType := exprResolvedType(module, node.Left)
		rightType := exprResolvedType(module, node.Right)
		if common := typeinfo.CommonNumericType(leftType, rightType); common != nil {
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
		return &ir.Binary{Op: node.Op, Left: left, Right: right, Type: t, Location: loc}

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
		return &ir.Call{Callee: calleeExpr, Args: args, Type: t, Location: loc}

	case *ast.PrintExpr:
		return &ir.Print{Value: lowerASTExpr(ctx, module, scope, node.Expr, nil), Newline: node.Newline, Location: loc}

	case *ast.FreeExpr:
		return &ir.Drop{Value: lowerASTExpr(ctx, module, scope, node.Expr, nil), Location: loc}

	case *ast.AsExpr:
		t := resolvedTypeID
		if t == ir.InvalidType {
			t = loweredTypeID(ctx, module, typeinfo.TypeFromSyntax(node.TypeExpr, typeinfo.SyntaxOptions{Target: ctx.Target, AllowAbstractSelf: true}))
		}
		subExpr := lowerASTExpr(ctx, module, scope, node.Expr, expectedType)
		return &ir.Cast{Expr: subExpr, Type: t, Location: loc}

	case *ast.SelectorExpr:
		return lowerSelectorExpr(ctx, module, scope, node)

	case *ast.IndexExpr:
		return lowerIndexExpr(ctx, module, scope, node)

	case *ast.StructLit:
		return lowerStructLiteralExpr(ctx, module, scope, node)

	case *ast.ArrayLit:
		return lowerArrayLiteralExpr(ctx, module, scope, node)

	default:
		return &ir.InvalidExpr{Message: "unsupported expression", Type: ir.InvalidType, Location: loc}
	}
}

func lowerCollectionCall(ctx *project.CompilerContext, module *project.Module, scope *table.Scope, call *ast.CallExpr, op symbols.CompilerOp) ir.Expr {
	fnType, _ := exprResolvedType(module, call.Callee).(*typeinfo.FuncType)
	if fnType == nil || len(fnType.Params) != 1 {
		return &ir.InvalidExpr{Message: "collection function type missing", Type: ir.InvalidType, Location: ast.LocOf(call)}
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
		return &ir.Len{Value: receiver, Type: loweredReturnTypeID(ctx, module, fnType.Return), Location: ast.LocOf(call)}
	case symbols.CompilerOpAsBytes:
		return &ir.SliceView{
			Source:   &ir.Place{Root: receiver, Type: receiver.TypeID(), Location: ast.LocOf(value)},
			Type:     loweredReturnTypeID(ctx, module, fnType.Return),
			Location: ast.LocOf(call),
		}
	case symbols.CompilerOpAsChars:
		return &ir.StringChars{
			Value:    receiver,
			Type:     loweredReturnTypeID(ctx, module, fnType.Return),
			Location: ast.LocOf(call),
		}
	default:
		return &ir.InvalidExpr{Message: "unsupported collection function lowering", Type: ir.InvalidType, Location: ast.LocOf(call)}
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
	return &ir.ZeroValue{Type: typeID, Location: loc}
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

func lowerSelectorMethodCall(ctx *project.CompilerContext, module *project.Module, scope *table.Scope, selector *ast.SelectorExpr, call *ast.CallExpr) ir.Expr {
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
			Base:     lowerASTExpr(ctx, module, scope, selector.Expr, nil),
			Slot:     slot,
			Args:     args,
			Consumes: consumes,
			Type:     loweredReturnTypeID(ctx, module, iface.Return),
			Location: ast.LocOf(call),
		}
	}
	methodSym := module.Semantics.ResolvedSymbols[selector.Name.ID()]
	fnType, _ := module.Semantics.ExprTypes[selector.ID()].(*typeinfo.FuncType)
	if methodSym == nil || fnType == nil || len(fnType.Params) == 0 {
		return &ir.InvalidExpr{Message: "unsupported selector call lowering", Type: ir.InvalidType}
	}
	methodOwner, ok := typeinfo.ReceiverTarget(fnType.Params[0])
	if !ok {
		return &ir.InvalidExpr{Message: "selector method receiver missing", Type: ir.InvalidType}
	}
	methodOwnerKey := typeinfo.TypeText(methodOwner)
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
			Name:     methodSymbolRefName(methodOwnerKey, methodSym),
			Type:     loweredTypeID(ctx, module, fnType),
			SymbolID: methodSym.ID,
			Location: ast.LocOf(selector.Name),
		},
		Args:     args,
		Type:     loweredReturnTypeID(ctx, module, fnType.Return),
		Location: ast.LocOf(call),
	}
}

func lowerSelectorExpr(ctx *project.CompilerContext, module *project.Module, scope *table.Scope, selector *ast.SelectorExpr) ir.Expr {
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
			return &ir.Load{Place: lowerPlace(ctx, module, scope, selector), NodeID: ir.NodeID(selector.ID()), Location: ast.LocOf(selector)}
		}
		return &ir.Field{
			Base:     lowerASTExpr(ctx, module, scope, selector.Expr, nil),
			Index:    fieldIndex,
			NodeID:   ir.NodeID(selector.ID()),
			Type:     loweredTypeID(ctx, module, field.Type),
			Location: ast.LocOf(selector),
		}
	}
	return &ir.InvalidExpr{Message: "selector lowering not implemented", Type: ir.InvalidType, Location: ast.LocOf(selector)}
}

func lowerIndexExpr(ctx *project.CompilerContext, module *project.Module, scope *table.Scope, node *ast.IndexExpr) ir.Expr {
	if module == nil || node == nil || node.Expr == nil || node.Index == nil {
		return &ir.InvalidExpr{Message: "invalid index", Type: ir.InvalidType, Location: ast.LocOf(node)}
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
			Location:     ast.LocOf(node),
		}
	}
	return &ir.Load{Place: lowerPlace(ctx, module, scope, node), NodeID: ir.NodeID(node.ID()), Location: ast.LocOf(node)}
}

func lowerStructLiteralExpr(ctx *project.CompilerContext, module *project.Module, scope *table.Scope, node *ast.StructLit) ir.Expr {
	if module == nil || node == nil {
		return &ir.InvalidExpr{Message: "invalid struct literal", Type: ir.InvalidType, Location: ast.LocOf(node)}
	}
	resolved := exprResolvedType(module, node)
	strct, ok := loweredRuntimeType(module, resolved, nil).(*typeinfo.StructType)
	if !ok || strct == nil {
		return &ir.InvalidExpr{Message: "struct literal type missing", Type: ir.InvalidType, Location: ast.LocOf(node)}
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
			return &ir.InvalidExpr{Message: "struct literal field missing during lowering", Type: ir.InvalidType, Location: ast.LocOf(node)}
		}
		values = append(values, lowerASTExpr(ctx, module, scope, value, field.Type))
	}
	return &ir.StructLit{
		Fields:   values,
		Type:     loweredTypeID(ctx, module, resolved),
		Location: ast.LocOf(node),
	}
}

func lowerArrayLiteralExpr(ctx *project.CompilerContext, module *project.Module, scope *table.Scope, node *ast.ArrayLit) ir.Expr {
	if module == nil || node == nil {
		return &ir.InvalidExpr{Message: "invalid array literal", Type: ir.InvalidType, Location: ast.LocOf(node)}
	}
	resolved := exprResolvedType(module, node)
	array, ok := loweredRuntimeType(module, resolved, nil).(*typeinfo.ArrayType)
	if !ok || array == nil || array.Elem == nil {
		return &ir.InvalidExpr{Message: "array literal type missing", Type: ir.InvalidType, Location: ast.LocOf(node)}
	}
	values := make([]ir.Expr, 0, len(node.Values))
	for _, value := range node.Values {
		values = append(values, lowerASTExpr(ctx, module, scope, value, array.Elem))
	}
	return &ir.ArrayLit{
		Values:   values,
		Dynamic:  array.Shape == typeinfo.ArrayOwner,
		Type:     loweredTypeID(ctx, module, resolved),
		Location: ast.LocOf(node),
	}
}

func lowerDynamicArrayOwnerCall(ctx *project.CompilerContext, module *project.Module, scope *table.Scope, node *ast.CallExpr, op symbols.CompilerOp) ir.Expr {
	fnType, _ := typeinfo.Underlying(exprResolvedType(module, node.Callee)).(*typeinfo.FuncType)
	if fnType == nil || len(fnType.Params) != len(node.Args) || len(node.Args) < 2 {
		return &ir.InvalidExpr{Message: "dynamic-array operation type missing", Type: ir.InvalidType, Location: ast.LocOf(node)}
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
		return &ir.InvalidExpr{Message: "dynamic-array owner reference missing", Type: ir.InvalidType, Location: ast.LocOf(node)}
	}
	out := &ir.DynamicArrayOp{
		Op:        op,
		Array:     args[0],
		ArrayType: loweredTypeID(ctx, module, ownerType),
		Type:      loweredReturnTypeID(ctx, module, nil),
		Location:  ast.LocOf(node),
	}
	switch op {
	case symbols.CompilerOpAppend:
		out.Value = args[1]
	case symbols.CompilerOpReserve, symbols.CompilerOpShrink:
		out.Length = args[1]
	case symbols.CompilerOpResize:
		if len(args) != 3 {
			return &ir.InvalidExpr{Message: "resize operation arguments missing", Type: ir.InvalidType, Location: ast.LocOf(node)}
		}
		out.Length = args[1]
		out.Value = args[2]
	default:
		panic(fmt.Sprintf("unsupported dynamic-array compiler operation %q", op))
	}
	return out
}

func lowerAllocCall(ctx *project.CompilerContext, module *project.Module, scope *table.Scope, node *ast.CallExpr) ir.Expr {
	if len(node.Args) < 1 || len(node.Args) > 2 {
		return &ir.InvalidExpr{Message: "alloc requires 1 or 2 arguments", Type: ir.InvalidType, Location: ast.LocOf(node)}
	}
	value := lowerASTExpr(ctx, module, scope, node.Args[0], nil)
	var allocator ir.Expr
	if len(node.Args) > 1 {
		allocator = lowerASTExpr(ctx, module, scope, node.Args[1], &typeinfo.AllocatorType{})
	}
	resultType := loweredTypeID(ctx, module, exprResolvedType(module, node))
	return &ir.AllocExpr{
		Value:     value,
		Allocator: allocator,
		Type:      resultType,
		Location:  ast.LocOf(node),
	}
}

func maybeLowerInterfaceExpr(ctx *project.CompilerContext, module *project.Module, scope *table.Scope, expr ast.Expr, expectedType typeinfo.Type) ir.Expr {
	if expectedType == nil {
		return nil
	}
	expectedRuntime := loweredRuntimeType(module, expectedType, nil)
	iface, ok := typeinfo.InterfaceTypeOf(expectedRuntime)
	if !ok {
		return nil
	}
	resolved := exprResolvedType(module, expr)
	if resolved == nil {
		return nil
	}
	resolvedRuntime := loweredRuntimeType(module, resolved, nil)
	if _, ok := typeinfo.InterfaceTypeOf(resolvedRuntime); ok {
		return nil
	}
	dataType := resolvedRuntime
	if target, _, ok := typeinfo.ReferenceTarget(typeinfo.Underlying(resolvedRuntime)); ok {
		dataType = target
	} else if target, ok := typeinfo.PointerTarget(typeinfo.Underlying(resolvedRuntime)); ok {
		dataType = target
	}
	slots := make([]ir.InterfaceSlot, 0, len(iface.Methods))
	implementations := module.Semantics.InterfaceImplementations[expr.ID()]
	if len(implementations) != len(iface.Methods) {
		return &ir.InvalidExpr{Message: "missing interface implementation evidence", Type: ir.InvalidType, Location: ast.LocOf(expr)}
	}
	for index, method := range iface.Methods {
		implementation := implementations[index]
		if implementation.MethodName != method.Name || implementation.CallableType == nil || implementation.Symbol == nil || implementation.OwnerKey == "" {
			return &ir.InvalidExpr{Message: "missing interface method implementation", Type: ir.InvalidType, Location: ast.LocOf(expr)}
		}
		slotType, ok := interfaceSlotTypeID(ctx, module, method)
		if !ok {
			return &ir.InvalidExpr{Message: "unsupported interface method shape", Type: ir.InvalidType, Location: ast.LocOf(expr)}
		}
		slots = append(slots, ir.InterfaceSlot{
			InterfaceType: loweredTypeID(ctx, module, expectedType),
			MethodName:    method.Name,
			SlotType:      slotType,
			FuncName:      methodSymbolRefName(implementation.OwnerKey, implementation.Symbol),
			FuncType:      loweredTypeID(ctx, module, implementation.CallableType),
			DataType:      loweredTypeID(ctx, module, dataType),
		})
	}
	return &ir.InterfaceMake{
		Value:    lowerASTExpr(ctx, module, scope, expr, nil),
		Slots:    slots,
		Type:     loweredTypeID(ctx, module, expectedType),
		Location: ast.LocOf(expr),
	}
}

func lookupInterfaceMethod(module *project.Module, baseType typeinfo.Type, name string) (*typeinfo.Method, int, bool) {
	iface, ok := typeinfo.InterfaceTypeOf(loweredRuntimeType(module, baseType, nil))
	if !ok {
		return nil, -1, false
	}
	for i := range iface.Methods {
		if iface.Methods[i].Name == name {
			return &iface.Methods[i], i, true
		}
	}
	return nil, -1, false
}

func interfaceSlotTypeID(ctx *project.CompilerContext, module *project.Module, method typeinfo.Method) (ir.TypeID, bool) {
	params := []ir.TypeID{ctx.Types.Intern(ir.Type{Kind: ir.TypeRawPtr})}
	for i, param := range method.Params {
		if i == 0 {
			continue
		}
		typ, ok := lowerInterfaceSlotValueType(ctx, module, param.Type)
		if !ok {
			return ir.InvalidType, false
		}
		params = append(params, typ)
	}
	returnType, ok := lowerInterfaceSlotValueType(ctx, module, method.Return)
	if !ok {
		return ir.InvalidType, false
	}
	if returnType == ir.InvalidType {
		returnType = ctx.Types.Intern(ir.Type{Kind: ir.TypeVoid})
	}
	return ctx.Types.Intern(ir.Type{Kind: ir.TypeFunction, Params: params, Return: returnType}), true
}

func lowerInterfaceSlotValueType(ctx *project.CompilerContext, module *project.Module, t typeinfo.Type) (ir.TypeID, bool) {
	if t == nil {
		return ctx.Types.Intern(ir.Type{Kind: ir.TypeVoid}), true
	}
	runtimeType := loweredRuntimeType(module, t, nil)
	if _, ok := typeinfo.InterfaceTypeOf(runtimeType); ok {
		return loweredTypeID(ctx, module, runtimeType), true
	}
	if typeinfo.ContainsAbstractSelf(runtimeType) {
		return ir.InvalidType, false
	}
	typ := loweredTypeID(ctx, module, runtimeType)
	if typ == ir.InvalidType {
		return ir.InvalidType, false
	}
	return typ, true
}

// exprResolvedType reads typechecker output from semantic cache.
// Lowering consumes that result; it should not re-infer expression types.
func exprResolvedType(module *project.Module, expr ast.Expr) typeinfo.Type {
	if module == nil || module.Semantics == nil || expr == nil {
		return nil
	}
	return module.Semantics.ExprTypes[expr.ID()]
}

func methodFunctionName(targetText, methodName string) string {
	var b strings.Builder
	b.WriteString("__impl__")
	b.WriteString(ir.SanitizeSymbolName(targetText))
	b.WriteString("__")
	b.WriteString(methodName)
	return b.String()
}

func methodSymbolRefName(targetText string, sym *symbols.Symbol) string {
	if sym == nil {
		return ""
	}
	return fmt.Sprintf("%s$%d", methodFunctionName(targetText, sym.Name), sym.ID)
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
			return &ir.FloatLit{Value: node.Value, Type: loweredTypeID(ctx, module, typeinfo.DefaultNumberType(node.Value)), Location: loc}
		}
		return &ir.IntLit{Value: integerValue, Type: loweredTypeID(ctx, module, typeinfo.DefaultNumberType(node.Value)), Location: loc}
	}
	family, _, numericType := typeinfo.NumericInfo(expectedType)
	if numericType && family == typeinfo.NumericFloat {
		v := node.Value
		if !numeric.IsFloat(node.Value) {
			v = integerValue + ".0"
		}
		return &ir.FloatLit{Value: v, Type: loweredTypeID(ctx, module, expectedType), Location: loc}
	}
	return &ir.IntLit{Value: integerValue, Type: loweredTypeID(ctx, module, expectedType), Location: loc}
}

func symbolName(sym *symbols.Symbol) string {
	if sym == nil {
		return ""
	}
	if name, ok := externSymbolName(sym, sym.Name); ok {
		return name
	}
	return fmt.Sprintf("%s$%d", sym.Name, sym.ID)
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

func externSymbolName(sym *symbols.Symbol, defaultName string) (string, bool) {
	if sym == nil {
		return "", false
	}
	fn, ok := sym.ASTNode.(*ast.FnDecl)
	if !ok {
		return "", false
	}
	return ast.FunctionLinkName(fn, defaultName)
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

func loweredTypeID(ctx *project.CompilerContext, module *project.Module, t typeinfo.Type) ir.TypeID {
	if ctx == nil || ctx.Types == nil || t == nil {
		return ir.InvalidType
	}
	return internRuntimeType(ctx.Types, loweredRuntimeType(module, t, nil))
}

func loweredReturnTypeID(ctx *project.CompilerContext, module *project.Module, t typeinfo.Type) ir.TypeID {
	if t == nil {
		return ctx.Types.Intern(ir.Type{Kind: ir.TypeVoid})
	}
	return loweredTypeID(ctx, module, t)
}

// internRuntimeType is the semantic-to-IR type boundary. It receives only
// runtime-normalized semantic types, so IR never reparses source type text.
func internRuntimeType(types *ir.TypeTable, t typeinfo.Type) ir.TypeID {
	if types == nil || t == nil {
		return ir.InvalidType
	}
	switch typ := typeinfo.Underlying(t).(type) {
	case *typeinfo.InvalidType, *typeinfo.UnknownType:
		return ir.InvalidType
	case *typeinfo.IntegerType:
		if typ == nil {
			return ir.InvalidType
		}
		return types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: typ.Signed, Bits: typ.Bits})
	case *typeinfo.ByteType:
		return types.Intern(ir.Type{Kind: ir.TypeByte})
	case *typeinfo.CharType:
		return types.Intern(ir.Type{Kind: ir.TypeChar})
	case *typeinfo.FloatType:
		if typ == nil {
			return ir.InvalidType
		}
		return types.Intern(ir.Type{Kind: ir.TypeFloat, Bits: typ.Bits})
	case *typeinfo.BoolType:
		return types.Intern(ir.Type{Kind: ir.TypeBool})
	case *typeinfo.CStrType:
		return types.Intern(ir.Type{Kind: ir.TypeCStr})
	case *typeinfo.StringType:
		return types.Intern(ir.Type{Kind: ir.TypeString})
	case *typeinfo.NoneType:
		return types.Intern(ir.Type{Kind: ir.TypeVoid})
	case *typeinfo.AllocatorType:
		return types.Intern(ir.Type{Kind: ir.TypeAllocator})
	case *typeinfo.NamedType:
		if typ == nil || typ.Name == "" {
			return ir.InvalidType
		}
		return types.Intern(ir.Type{Kind: ir.TypeNamed, Name: typ.Name})
	case *typeinfo.OwnedPtrType:
		if typ == nil {
			return ir.InvalidType
		}
		return types.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: internRuntimeType(types, typ.Target)})
	case *typeinfo.RawPtrType:
		return types.Intern(ir.Type{Kind: ir.TypeRawPtr})
	case *typeinfo.RefType:
		if typ == nil {
			return ir.InvalidType
		}
		return types.Intern(ir.Type{Kind: ir.TypeReference, Mutable: typ.Mutable, Elem: internRuntimeType(types, typ.Target)})
	case *typeinfo.OptionalType:
		if typ == nil {
			return ir.InvalidType
		}
		return types.Intern(ir.Type{Kind: ir.TypeOptional, Elem: internRuntimeType(types, typ.Inner)})
	case *typeinfo.ArrayType:
		if typ == nil {
			return ir.InvalidType
		}
		if typ.Shape == typeinfo.ArraySlice {
			return types.Intern(ir.Type{Kind: ir.TypeSlice, Elem: internRuntimeType(types, typ.Elem)})
		}
		return types.Intern(ir.Type{Kind: ir.TypeArray, Length: typ.Len, Elem: internRuntimeType(types, typ.Elem)})
	case *typeinfo.StructType:
		if typ == nil {
			return ir.InvalidType
		}
		fields := make([]ir.TypeField, 0, len(typ.Fields))
		for _, field := range typ.Fields {
			fields = append(fields, ir.TypeField{Name: field.Name, Type: internRuntimeType(types, field.Type)})
		}
		return types.Intern(ir.Type{Kind: ir.TypeStruct, Fields: fields})
	case *typeinfo.InterfaceType:
		if typ == nil {
			return ir.InvalidType
		}
		methods := make([]ir.TypeMethod, 0, len(typ.Methods))
		for _, method := range typ.Methods {
			params := make([]ir.TypeField, 0, len(method.Params))
			for _, param := range method.Params {
				params = append(params, ir.TypeField{Name: param.Name, Type: internRuntimeType(types, param.Type)})
			}
			returnType := internRuntimeType(types, method.Return)
			if returnType == ir.InvalidType {
				returnType = types.Intern(ir.Type{Kind: ir.TypeVoid})
			}
			methods = append(methods, ir.TypeMethod{Name: method.Name, Params: params, Return: returnType})
		}
		return types.Intern(ir.Type{Kind: ir.TypeInterface, Methods: methods})
	case *typeinfo.FuncType:
		if typ == nil {
			return ir.InvalidType
		}
		params := make([]ir.TypeID, 0, len(typ.Params))
		for _, param := range typ.Params {
			params = append(params, internRuntimeType(types, param))
		}
		returnType := internRuntimeType(types, typ.Return)
		if returnType == ir.InvalidType {
			returnType = types.Intern(ir.Type{Kind: ir.TypeVoid})
		}
		return types.Intern(ir.Type{Kind: ir.TypeFunction, Params: params, Return: returnType})
	case *typeinfo.EnumType:
		if typ == nil {
			return ir.InvalidType
		}
		return types.Intern(ir.Type{Kind: ir.TypeNamed, Name: typ.Text()})
	default:
		return ir.InvalidType
	}
}

// resolveNamedType performs a single-hop scope lookup for a NamedType so the
// lowerer can collapse source-level aliases before runtime layout work.
// Called only from loweredRuntimeType; lives here to avoid importing table
// from the leaf typeinfo package.
func resolveNamedType(scope *table.Scope, t typeinfo.Type) typeinfo.Type {
	if scope == nil || t == nil {
		return t
	}
	named, ok := t.(*typeinfo.NamedType)
	if !ok || named == nil {
		return t
	}
	sym, found := scope.Lookup(named.Name)
	if found && sym != nil && sym.Kind == symbols.SymbolType {
		if resolved, ok := symbols.GetSymbolType(sym); ok && resolved != nil {
			return resolved
		}
	}
	return t
}

// loweredRuntimeType strips semantic-only named layers and preserves recursive
// shells so MIR sees runtime layout, not source-level aliases.
func loweredRuntimeType(module *project.Module, t typeinfo.Type, seen map[*typeinfo.DefinedType]struct{}) typeinfo.Type {
	if seen == nil {
		seen = make(map[*typeinfo.DefinedType]struct{})
	}
	if t == nil {
		return nil
	}
	if module != nil {
		t = resolveNamedType(module.ModuleScope, t)
	}
	switch typ := t.(type) {
	case *typeinfo.DefinedType:
		if typ == nil {
			return nil
		}
		if _, ok := seen[typ]; ok {
			// Stop self-recursive expansion once shell already seen.
			return &typeinfo.NamedType{Name: typ.Name}
		}
		seen[typ] = struct{}{}
		defer delete(seen, typ)
		return loweredRuntimeType(module, typ.Underlying, seen)
	case *typeinfo.OwnedPtrType:
		if typ == nil {
			return nil
		}
		return &typeinfo.OwnedPtrType{Target: loweredRuntimeType(module, typ.Target, seen)}
	case *typeinfo.RawPtrType:
		if typ == nil {
			return nil
		}
		return &typeinfo.RawPtrType{}
	case *typeinfo.RefType:
		if typ == nil {
			return nil
		}
		return &typeinfo.RefType{Mutable: typ.Mutable, Target: loweredRuntimeType(module, typ.Target, seen)}
	case *typeinfo.OptionalType:
		if typ == nil {
			return nil
		}
		return &typeinfo.OptionalType{Inner: loweredRuntimeType(module, typ.Inner, seen)}
	case *typeinfo.ArrayType:
		if typ == nil {
			return nil
		}
		return &typeinfo.ArrayType{Len: typ.Len, Shape: typ.Shape, Elem: loweredRuntimeType(module, typ.Elem, seen)}
	case *typeinfo.StructType:
		if typ == nil {
			return nil
		}
		fields := make([]typeinfo.Field, 0, len(typ.Fields))
		for _, field := range typ.Fields {
			fields = append(fields, typeinfo.Field{Name: field.Name, Type: loweredRuntimeType(module, field.Type, seen)})
		}
		return &typeinfo.StructType{Fields: fields}
	case *typeinfo.InterfaceType:
		if typ == nil {
			return nil
		}
		methods := make([]typeinfo.Method, 0, len(typ.Methods))
		for _, method := range typ.Methods {
			params := make([]typeinfo.Field, 0, len(method.Params))
			for _, param := range method.Params {
				params = append(params, typeinfo.Field{
					Name: param.Name,
					Type: loweredRuntimeType(module, param.Type, seen),
				})
			}
			methods = append(methods, typeinfo.Method{
				Name:   method.Name,
				Params: params,
				Return: loweredRuntimeType(module, method.Return, seen),
			})
		}
		return &typeinfo.InterfaceType{Methods: methods}
	case *typeinfo.FuncType:
		if typ == nil {
			return nil
		}
		params := make([]typeinfo.Type, 0, len(typ.Params))
		for _, param := range typ.Params {
			params = append(params, loweredRuntimeType(module, param, seen))
		}
		// defensive slice copy to prevent sharing original backing array
		return &typeinfo.FuncType{Params: params, Return: loweredRuntimeType(module, typ.Return, seen)}
	default:
		return typeinfo.Underlying(t)
	}
}
