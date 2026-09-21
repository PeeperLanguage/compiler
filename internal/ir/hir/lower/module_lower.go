package lower

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/ir/typelower"
	"compiler/internal/module"
	"compiler/internal/semantics/intrinsics"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typecheckresult"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
	"compiler/pkg/numeric"
)

// lowering carries shared phase owners through recursive HIR construction.
// Keeping it package-private prevents lowering from depending on project state.
type lowering struct {
	types       *ir.TypeTable
	diagnostics *diagnostics.DiagnosticBag
}

func GenerateHIR(types *ir.TypeTable, diag *diagnostics.DiagnosticBag, module *module.Module) *hir.Module {
	if types == nil || module == nil {
		return nil
	}
	ctx := &lowering{types: types, diagnostics: diag}
	out := &hir.Module{
		Name:     module.ID.ImportPath,
		FilePath: module.FilePath,
		Types:    ctx.types,
		Externs:  make([]hir.Extern, 0),
		Funcs:    make([]*hir.Function, 0),
	}
	for _, sym := range module.ModuleScope.Symbols() {
		if sym == nil || sym.Kind != symbols.SymbolConst {
			continue
		}
		constantType, ok := symbols.GetSymbolType(sym)
		if !ok || typelower.Type(ctx.types, ctx.diagnostics, constantType) == ir.InvalidType {
			return nil
		}
	}
	ast.ForEachDecl(module.AST, func(decl ast.Decl) bool {
		fn, ok := decl.(*ast.FnDecl)
		if !ok || fn == nil || fn.Name == nil {
			return true
		}
		sym := module.Bindings.Symbol(fn.Name)
		if sym == nil {
			return true
		}
		fnType, _ := symbols.GetSymbolType(sym)
		resolvedFnType, _ := fnType.(*typeinfo.FuncType)
		emittedName, _ := callableName(module, sym)
		if fn.Body == nil {
			params, returnType := lowerExternSignature(ctx, module, fn.ParamsWithReceiver(), resolvedFnType)
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

func lowerExternSignature(ctx *lowering, module *module.Module, params []ast.Param, resolvedFnType *typeinfo.FuncType) ([]ir.Param, ir.TypeID) {
	loweredParams := make([]ir.Param, 0, len(params))
	for i, param := range params {
		name := ""
		if param.Name != nil {
			name = param.Name.Name
		}
		var paramType typeinfo.Type
		if resolvedFnType != nil && i < len(resolvedFnType.Params) {
			paramType = resolvedFnType.Params[i]
		}
		var symbolID symbols.SymbolID
		if param.Name != nil {
			if sym := module.Bindings.Symbol(param.Name); sym != nil {
				symbolID = sym.ID
			}
		}
		loweredParams = append(loweredParams, ir.Param{Name: name, Type: typelower.Type(ctx.types, ctx.diagnostics, paramType), SymbolID: symbolID})
	}

	if resolvedFnType == nil {
		return loweredParams, ir.InvalidType
	}
	return loweredParams, typelower.ReturnType(ctx.types, ctx.diagnostics, resolvedFnType.Return)
}

func lowerASTFunctionNamed(ctx *lowering, module *module.Module, sym *symbols.Symbol, fn *ast.FnDecl, emittedName string) *hir.Function {
	if sym == nil || fn == nil || fn.Body == nil || sym.Scope == nil {
		return nil
	}
	funcScope := sym.Scope
	var retType typeinfo.Type
	retTypeID := ir.InvalidType
	if symbolType, ok := symbols.GetSymbolType(sym); ok {
		if fnType, ok := symbolType.(*typeinfo.FuncType); ok && fnType != nil {
			retType = fnType.Return
			retTypeID = typelower.ReturnType(ctx.types, ctx.diagnostics, retType)
		}
	}
	hirFn := &hir.Function{
		Name:       emittedName,
		Params:     make([]ir.Param, 0, len(fn.ParamsWithReceiver())),
		ReturnType: retTypeID,
		Body:       &hir.Block{Stmts: make([]hir.Stmt, 0), NodeID: hir.NodeID(fn.Body.ID()), Location: ast.LocOf(fn.Body)},
		NodeID:     hir.NodeID(fn.ID()),
		SymbolID:   sym.ID,
		Location:   ast.LocOf(fn),
	}
	for _, param := range fn.ParamsWithReceiver() {
		name := ""
		var symbolID symbols.SymbolID
		var paramType typeinfo.Type
		if param.Name != nil {
			sym := module.Bindings.Symbol(param.Name)
			if sym != nil {
				name = symbolName(module, sym)
				symbolID = sym.ID
				if t, ok := symbols.GetSymbolType(sym); ok {
					paramType = t
				}
			} else {
				name = param.Name.Name
			}
		}
		hirFn.Params = append(hirFn.Params, ir.Param{Name: name, Type: typelower.Type(ctx.types, ctx.diagnostics, paramType), SymbolID: symbolID})
	}
	appendBlock(module, funcScope, hirFn.Body, fn.Body, retType, ctx)
	return hirFn
}

func appendBlock(module *module.Module, parentScope *symbols.Scope, out *hir.Block, block *ast.BlockStmt, returnType typeinfo.Type, ctx *lowering) {
	if out == nil || block == nil {
		return
	}
	out.Location = ast.LocOf(block)
	out.NodeID = hir.NodeID(block.ID())
	scope := parentScope
	if module.Bindings != nil {
		if s := module.Bindings.Scope(block); s != nil {
			scope = s
		}
	}
	for _, stmt := range block.Stmts {
		appendStmt(module, scope, out, stmt, returnType, ctx)
	}
}

func appendStmt(module *module.Module, scope *symbols.Scope, out *hir.Block, stmt ast.Stmt, returnType typeinfo.Type, ctx *lowering) {
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
		sym := module.Bindings.Symbol(node.Name)
		if sym == nil {
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
		out.Stmts = append(out.Stmts, &hir.Binding{Name: symbolName(module, sym), Constant: false, Type: typelower.Type(ctx.types, ctx.diagnostics, sym.Type), Value: valueExpr, NodeID: hir.NodeID(node.ID()), SymbolID: sym.ID, Location: ast.LocOf(node)})

	case *ast.ConstDecl:
		if node.Name == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "const binding missing name", NodeID: hir.NodeID(node.ID()), Location: ast.LocOf(node)})
			return
		}
		sym := module.Bindings.Symbol(node.Name)
		if sym == nil {
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
		out.Stmts = append(out.Stmts, &hir.Binding{Name: symbolName(module, sym), Constant: true, Type: typelower.Type(ctx.types, ctx.diagnostics, sym.Type), Value: valueExpr, NodeID: hir.NodeID(node.ID()), SymbolID: sym.ID, Location: ast.LocOf(node)})

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
		if node.Iterable != nil {
			if checked := module.Typechecking.CheckedIteration(node.ID()); checked != nil {
				appendStmt(module, scope, out, checked, returnType, ctx)
				return
			}
		}
		out.Stmts = append(out.Stmts, lowerForStmt(ctx, module, scope, node, returnType))
	case *ast.MatchStmt:
		evidence, found := module.Typechecking.Match(node.ID())
		if !found || len(evidence.Arms) != len(node.Arms) {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "match statement missing semantic evidence", NodeID: hir.NodeID(node.ID()), Location: ast.LocOf(node)})
			return
		}
		switchStmt := &hir.SwitchVariant{
			Value:    lowerASTExpr(ctx, module, scope, node.Subject, evidence.EnumType),
			Cases:    make([]hir.VariantCaseBlock, 0, len(evidence.Arms)),
			NodeID:   hir.NodeID(node.ID()),
			Location: ast.LocOf(node),
		}
		for armIndex, arm := range evidence.Arms {
			sourceArm := node.Arms[armIndex]
			caseBlock := hir.VariantCaseBlock{
				Case: arm.Case,
				Body: &hir.Block{Stmts: make([]hir.Stmt, 0), NodeID: hir.NodeID(sourceArm.Body.ID()), Location: ast.LocOf(sourceArm.Body)},
			}
			if arm.Payload != nil {
				caseBlock.PayloadType = typelower.Type(ctx.types, ctx.diagnostics, arm.Payload)
			}
			for _, field := range arm.Bindings {
				wholePayload := false
				switch field.Projection {
				case typecheckresult.MatchPayloadField:
				case typecheckresult.MatchWholePayload:
					wholePayload = true
				default:
					out.Stmts = append(out.Stmts, &hir.Invalid{Message: "match binding has invalid projection", NodeID: hir.NodeID(node.ID()), Location: ast.LocOf(node)})
					return
				}
				if field.Binding == nil {
					continue
				}
				caseBlock.Bindings = append(caseBlock.Bindings, hir.VariantBinding{
					FieldIndex:   field.Field,
					WholePayload: wholePayload,
					Name:         symbolName(module, field.Binding),
					Type:         typelower.Type(ctx.types, ctx.diagnostics, field.Type),
					SymbolID:     field.Binding.ID,
				})
			}
			appendBlock(module, scope, caseBlock.Body, sourceArm.Body, returnType, ctx)
			switchStmt.Cases = append(switchStmt.Cases, caseBlock)
		}
		out.Stmts = append(out.Stmts, switchStmt)

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
	case *ast.BreakStmt, *ast.ContinueStmt:
		// CFG owns loop transfer; no executable HIR statement is needed.
	case *ast.BadStmt, *ast.BadDecl, *ast.ImportDecl, *ast.FnDecl,
		*ast.TypeAliasDecl, *ast.StructDecl, *ast.InterfaceDecl, *ast.EnumDecl:
		out.Stmts = append(out.Stmts, &hir.Invalid{Message: "unsupported statement", NodeID: hir.NodeID(node.ID()), Location: ast.LocOf(node)})
	default:
		panic(fmt.Sprintf("HIR lowering: unhandled statement %T", stmt))
	}
}

func lowerForStmt(ctx *lowering, module *module.Module, scope *symbols.Scope, node *ast.ForStmt, returnType typeinfo.Type) hir.Stmt {
	location := ast.LocOf(node)
	loop := &hir.For{
		Body:     &hir.Block{Stmts: make([]hir.Stmt, 0), NodeID: hir.NodeID(node.Body.ID()), Location: ast.LocOf(node.Body)},
		NodeID:   hir.NodeID(node.ID()),
		Location: location,
	}
	appendBlock(module, scope, loop.Body, node.Body, returnType, ctx)
	if node.Iterable == nil {
		if node.Cond != nil {
			loop.Cond = lowerASTExpr(ctx, module, scope, node.Cond, &typeinfo.BoolType{})
		}
		return loop
	}

	// Published evidence is complete by construction: the typechecker publishes
	// only for a loop that typed cleanly. Absence is the one case to handle.
	evidence, found := module.Typechecking.ForIteration(node.ID())
	if !found {
		return &hir.Invalid{Message: "for-in statement missing semantic evidence", NodeID: hir.NodeID(node.ID()), Location: location}
	}
	loop.Init = &hir.Block{Stmts: make([]hir.Stmt, 0), Location: location}
	loop.Bindings = &hir.Block{Stmts: make([]hir.Stmt, 0), Location: location}
	loop.Next = &hir.Block{Stmts: make([]hir.Stmt, 0), Location: location}
	boolType := typelower.Type(ctx.types, ctx.diagnostics, &typeinfo.BoolType{})

	switch plan := evidence.Plan.(type) {
	case *typecheckresult.RangeIteration:
		// Range evidence is published only for a RangeExpr iterable with both
		// bounds present, so the assertion cannot fail for a published loop.
		rangeExpr, ok := node.Iterable.(*ast.RangeExpr)
		if !ok {
			return &hir.Invalid{Message: "range iteration evidence does not match syntax", NodeID: hir.NodeID(node.ID()), Location: location}
		}
		loop.Init.Stmts = append(loop.Init.Stmts,
			generatedBinding(ctx, module, evidence.Cursor, lowerASTExpr(ctx, module, scope, rangeExpr.Start, evidence.ElementType), location),
			generatedBinding(ctx, module, plan.Limit, lowerASTExpr(ctx, module, scope, rangeExpr.End, evidence.ElementType), location),
		)
		if plan.Ordinal != nil {
			ordinalType := typelower.Type(ctx.types, ctx.diagnostics, plan.Ordinal.Type)
			loop.Init.Stmts = append(loop.Init.Stmts, generatedBinding(ctx, module, plan.Ordinal,
				&ir.IntLit{Value: "0", Type: ordinalType, SourceInfo: ir.SourceInfo{Location: location}}, location))
		}
		loop.Cond = &ir.Binary{
			Op: "<", Left: generatedIdent(ctx, module, evidence.Cursor, location), Right: generatedIdent(ctx, module, plan.Limit, location), Type: boolType,
			SourceInfo: ir.SourceInfo{Location: location},
		}
		if evidence.Index != nil {
			loop.Bindings.Stmts = append(loop.Bindings.Stmts,
				generatedBinding(ctx, module, evidence.Index, generatedIdent(ctx, module, plan.Ordinal, location), location))
		}
		loop.Bindings.Stmts = append(loop.Bindings.Stmts,
			generatedBinding(ctx, module, evidence.Value, generatedIdent(ctx, module, evidence.Cursor, location), location))
		loop.Next.Stmts = append(loop.Next.Stmts, incrementSymbol(ctx, module, evidence.Cursor, location))
		if plan.Ordinal != nil {
			loop.Next.Stmts = append(loop.Next.Stmts, incrementSymbol(ctx, module, plan.Ordinal, location))
		}
	case *typecheckresult.SequenceIteration:
		carrier := generatedIdent(ctx, module, plan.Carrier, location)
		cursor := generatedIdent(ctx, module, evidence.Cursor, location)
		cursorType := typelower.Type(ctx.types, ctx.diagnostics, evidence.Cursor.Type)
		elementType := typelower.Type(ctx.types, ctx.diagnostics, evidence.ElementType)
		loop.Init.Stmts = append(loop.Init.Stmts,
			generatedBinding(ctx, module, plan.Carrier,
				lowerImplicitReferenceValue(ctx, module, scope, node.Iterable, plan.CarrierType), location),
			generatedBinding(ctx, module, evidence.Cursor,
				&ir.IntLit{Value: "0", Type: cursorType, SourceInfo: ir.SourceInfo{Location: location}}, location),
		)
		loop.Cond = &ir.Binary{
			Op: "<", Left: cursor,
			Right: &ir.Len{Value: carrier, Type: cursorType, SourceInfo: ir.SourceInfo{Location: location}},
			Type:  boolType, SourceInfo: ir.SourceInfo{Location: location},
		}
		if evidence.Index != nil {
			loop.Bindings.Stmts = append(loop.Bindings.Stmts,
				generatedBinding(ctx, module, evidence.Index, generatedIdent(ctx, module, evidence.Cursor, location), location))
		}
		loop.Bindings.Stmts = append(loop.Bindings.Stmts,
			generatedBinding(ctx, module, evidence.Value, &ir.Load{Place: &ir.Place{
				Root: generatedIdent(ctx, module, plan.Carrier, location),
				Projections: []ir.PlaceProjection{{
					Kind: ir.PlaceProjectionIndex, Index: generatedIdent(ctx, module, evidence.Cursor, location), Type: elementType, Location: location,
				}},
				Type: elementType, Location: location,
			}, SourceInfo: ir.SourceInfo{Location: location}}, location))
		loop.Next.Stmts = append(loop.Next.Stmts, incrementSymbol(ctx, module, evidence.Cursor, location))
	default:
		return &hir.Invalid{Message: "unknown for-in iteration evidence", NodeID: hir.NodeID(node.ID()), Location: location}
	}
	return loop
}

func generatedBinding(ctx *lowering, module *module.Module, sym *symbols.Symbol, value ir.Expr, location *source.Location) *hir.Binding {
	return &hir.Binding{
		Name: symbolName(module, sym), Type: typelower.Type(ctx.types, ctx.diagnostics, sym.Type), Value: value, SymbolID: sym.ID, Location: location,
	}
}

func generatedIdent(ctx *lowering, module *module.Module, sym *symbols.Symbol, location *source.Location) *ir.Ident {
	return &ir.Ident{
		Name: symbolName(module, sym), Type: typelower.Type(ctx.types, ctx.diagnostics, sym.Type), SymbolID: sym.ID,
		SourceInfo: ir.SourceInfo{Location: location},
	}
}

func incrementSymbol(ctx *lowering, module *module.Module, sym *symbols.Symbol, location *source.Location) *hir.Assign {
	typeID := typelower.Type(ctx.types, ctx.diagnostics, sym.Type)
	return &hir.Assign{
		Target: &ir.Place{Root: generatedIdent(ctx, module, sym, location), Type: typeID, Location: location},
		Value: &ir.Binary{
			Op: "+", Left: generatedIdent(ctx, module, sym, location),
			Right: &ir.IntLit{Value: "1", Type: typeID, SourceInfo: ir.SourceInfo{Location: location}},
			Type:  typeID, SourceInfo: ir.SourceInfo{Location: location},
		},
		Location: location,
	}
}

func lowerPlace(ctx *lowering, module *module.Module, scope *symbols.Scope, expr ast.Expr) *ir.Place {
	if selector, ok := expr.(*ast.SelectorExpr); ok && selector != nil && selector.Expr != nil && selector.Name != nil {
		if module != nil && module.Flow != nil {
			if access, found := module.Flow.VariantField(selector.ID()); found {
				out := lowerPlace(ctx, module, scope, selector.Expr)
				out.Projections = append(out.Projections, ir.PlaceProjection{
					Kind: ir.PlaceProjectionField, FieldIndex: access.Field,
					Type: typelower.Type(ctx.types, ctx.diagnostics, access.Type), Location: ast.LocOf(selector),
				})
				out.Type = typelower.Type(ctx.types, ctx.diagnostics, access.Type)
				out.Location = ast.LocOf(selector)
				return appendVariantPayloadPlace(ctx, module, selector, out)
			}
		}
		if module != nil && module.Typechecking != nil {
			if access, found := module.Typechecking.StructField(selector.ID()); found {
				out := lowerPlace(ctx, module, scope, selector.Expr)
				if access.DereferenceType != nil {
					out.Projections = append(out.Projections, ir.PlaceProjection{
						Kind: ir.PlaceProjectionDeref, Type: typelower.Type(ctx.types, ctx.diagnostics, access.DereferenceType), Location: ast.LocOf(selector.Expr),
					})
				}
				out.Projections = append(out.Projections, ir.PlaceProjection{
					Kind: ir.PlaceProjectionField, FieldIndex: access.Field,
					Type: typelower.Type(ctx.types, ctx.diagnostics, access.Type), Location: ast.LocOf(selector),
				})
				out.Type = typelower.Type(ctx.types, ctx.diagnostics, access.Type)
				out.Location = ast.LocOf(selector)
				return appendVariantPayloadPlace(ctx, module, selector, out)
			}
		}
	}
	if index, ok := expr.(*ast.IndexExpr); ok && index != nil && index.Expr != nil && index.Index != nil {
		if _, slicing := index.Index.(*ast.RangeExpr); !slicing {
			indexExpr := lowerASTExpr(ctx, module, scope, index.Index, typeinfo.DefaultIntegerType())
			if constant, ok := module.Typechecking.ConstantIndex(index.ID()); ok {
				indexExpr = &ir.IntLit{
					Value: constant.Text, Type: typelower.Type(ctx.types, ctx.diagnostics, constant.Type),
					SourceInfo: ir.SourceInfo{Location: ast.LocOf(index.Index)},
				}
			}
			out := lowerPlace(ctx, module, scope, index.Expr)
			baseType := exprResolvedType(module, index.Expr)
			if target, _, reference := typeinfo.ReferenceTarget(typeinfo.Underlying(baseType)); reference {
				baseType = target
			}
			array, ok := typeinfo.Underlying(baseType).(*typeinfo.ArrayType)
			if !ok || array == nil || array.Elem == nil {
				panic("HIR lowering: index base missing array element type")
			}
			out.Type = typelower.Type(ctx.types, ctx.diagnostics, array.Elem)
			out.Location = ast.LocOf(index)
			out.Projections = append(out.Projections, ir.PlaceProjection{
				Kind: ir.PlaceProjectionIndex, Index: indexExpr, Type: out.Type, Location: ast.LocOf(index),
			})
			return appendVariantPayloadPlace(ctx, module, index, out)
		}
	}
	ident, ok := expr.(*ast.Ident)
	if !ok || ident == nil {
		typeID := typelower.Type(ctx.types, ctx.diagnostics, exprResolvedType(module, expr))
		return &ir.Place{Root: lowerASTExpr(ctx, module, scope, expr, nil), Type: typeID, Location: ast.LocOf(expr)}
	}
	root := lowerIdentExpr(module, ident, typelower.Type(ctx.types, ctx.diagnostics, module.BaseExprType(ident.ID())))
	out := &ir.Place{
		Root: root, Type: root.TypeID(), Location: ast.LocOf(expr),
	}
	return appendVariantPayloadPlace(ctx, module, expr, out)
}

func appendVariantPayloadPlace(ctx *lowering, module *module.Module, expr ast.Expr, out *ir.Place) *ir.Place {
	if ctx == nil || module == nil || module.Flow == nil || expr == nil || out == nil {
		return out
	}
	payload, _ := module.Flow.Payload(expr.ID())
	if !payload.AppliesTo(module.Flow.StorageOrigins(expr.ID())) {
		return out
	}
	for _, caseIndex := range payload.Cases {
		variant, ok := ctx.types.Type(out.Type)
		variantCase, caseOK := variant.VariantCase(caseIndex)
		if !ok || !caseOK || variantCase.Payload == ir.InvalidType {
			break
		}
		out.Type = variantCase.Payload
		out.Projections = append(out.Projections, ir.PlaceProjection{
			Kind: ir.PlaceProjectionVariantPayload, Case: caseIndex,
			Type: out.Type, Location: ast.LocOf(expr),
		})
	}
	return out
}

func lowerReferenceValue(ctx *lowering, module *module.Module, scope *symbols.Scope, expr ast.Expr, resultType typeinfo.Type, typeID ir.TypeID) ir.Expr {
	target, _, reference := typeinfo.ReferenceTarget(typeinfo.Underlying(resultType))
	if !reference {
		return &ir.InvalidExpr{Message: "reference lowering requires reference type", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(expr)}}
	}
	borrowAsView := false
	switch runtimeTarget := typeinfo.Underlying(target).(type) {
	case *typeinfo.StringType:
		borrowAsView = true
	case *typeinfo.ArrayType:
		borrowAsView = runtimeTarget.Shape == typeinfo.ArraySlice
	}
	exprType := func(node ast.Expr) typeinfo.Type {
		return exprResolvedType(module, node)
	}
	if !place.Addressable(scope, expr, exprType, module.ExpandedDefaultBinding) {
		return &ir.TempBorrow{
			Value:      lowerASTExpr(ctx, module, scope, expr, target),
			Slice:      borrowAsView,
			Type:       typeID,
			SourceInfo: ir.SourceInfo{Location: ast.LocOf(expr)},
		}
	}
	value := lowerPlace(ctx, module, scope, expr)
	if borrowAsView {
		return &ir.SliceView{Place: value, Type: typeID, SourceInfo: ir.SourceInfo{Location: ast.LocOf(expr)}}
	}
	return &ir.AddrOf{Place: value, Type: typeID, SourceInfo: ir.SourceInfo{Location: ast.LocOf(expr)}}
}

func lowerImplicitReferenceValue(ctx *lowering, module *module.Module, scope *symbols.Scope, expr ast.Expr, resultType typeinfo.Type) ir.Expr {
	typeID := typelower.Type(ctx.types, ctx.diagnostics, resultType)
	if _, _, borrowed := typeinfo.ReferenceTarget(typeinfo.Underlying(exprResolvedType(module, expr))); borrowed {
		return lowerASTExpr(ctx, module, scope, expr, nil)
	}
	return lowerReferenceValue(ctx, module, scope, expr, resultType, typeID)
}

func lowerElse(module *module.Module, scope *symbols.Scope, stmt ast.Stmt, returnType typeinfo.Type, ctx *lowering) hir.Stmt {
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
func lowerASTExpr(ctx *lowering, module *module.Module, scope *symbols.Scope, expr ast.Expr, expectedType typeinfo.Type) (result ir.Expr) {
	if expr == nil {
		return &ir.InvalidExpr{Message: "nil expression", Type: ir.InvalidType}
	}
	loc := ast.LocOf(expr)
	defer func() {
		result = ir.WithOrigin(result, ir.SourceInfo{NodeID: ir.NodeID(expr.ID()), Location: loc})
	}()

	// Fetch canonical type from the typechecker side-table when available.
	resolvedType := exprResolvedType(module, expr)
	resolvedTypeID := typelower.Type(ctx.types, ctx.diagnostics, resolvedType)
	expectedTypeID := typelower.Type(ctx.types, ctx.diagnostics, expectedType)
	conversion, converting := typeinfo.Conversion{}, false
	if module != nil && module.Typechecking != nil {
		conversion, converting = module.Typechecking.ImplicitConversion(expr.ID())
	}
	if module != nil && module.Flow != nil {
		if test, ok := module.Flow.CaseTest(expr.ID()); ok {
			subject, _ := module.TypedASTNodes[test.SubjectID].(ast.Expr)
			membership := &ir.VariantIs{
				Value: lowerASTExpr(ctx, module, scope, subject, nil),
				Case:  test.Case,
				Type:  typelower.Type(ctx.types, ctx.diagnostics, &typeinfo.BoolType{}),
			}
			if test.CaseWhenTrue {
				return membership
			}
			return &ir.Unary{Op: "!", Arg: membership, Type: membership.Type}
		}
		if payload, _ := module.Flow.Payload(expr.ID()); len(payload.Cases) > 0 && place.IsPlaceExpr(expr) {
			return &ir.Load{Place: lowerPlace(ctx, module, scope, expr)}
		}
	}
	if expectedType != nil && resolvedType != nil && converting &&
		conversion.Kind == typeinfo.ConversionOptional && conversion.Compatibility == typeinfo.Compatible {
		if innerExpected := optionalPromotionInnerType(expectedType, resolvedType, expr); innerExpected != nil {
			return &ir.VariantMake{
				Case:       ir.OptionalPresentCase,
				Payload:    lowerASTExpr(ctx, module, scope, expr, innerExpected),
				Type:       typelower.Type(ctx.types, ctx.diagnostics, expectedType),
				SourceInfo: ir.SourceInfo{Location: loc},
			}
		}
	}
	if ifaceExpr := maybeLowerInterfaceExpr(ctx, module, scope, expr, expectedType); ifaceExpr != nil {
		return ifaceExpr
	}
	if expectedType != nil && resolvedType != nil && converting && conversion.Compatibility == typeinfo.Compatible {
		switch conversion.Kind {
		case typeinfo.ConversionNumeric, typeinfo.ConversionReference, typeinfo.ConversionStruct:
			if expectedTypeID != resolvedTypeID {
				value := lowerASTExpr(ctx, module, scope, expr, nil)
				return &ir.Cast{Expr: value, Type: expectedTypeID, SourceInfo: ir.SourceInfo{Location: loc}}
			}
		}
	}
	if construction, ok := module.Typechecking.VariantConstruction(expr.ID()); ok {
		variant := &ir.VariantMake{
			Case: construction.Case,
			Type: typelower.Type(ctx.types, ctx.diagnostics, construction.EnumType),
		}
		if construction.Payload != nil {
			variant.Payload = lowerASTExpr(ctx, module, scope, construction.Value, construction.Payload)
		}
		return variant
	}
	switch node := expr.(type) {
	case *ast.NumberLit:
		return lowerNumberLit(ctx, module, node, resolvedType, loc)

	case *ast.StringLit:
		return &ir.StringLit{Value: node.Value, Type: resolvedTypeID, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.ByteLit:
		return &ir.IntLit{Value: fmt.Sprintf("%d", node.Value[0]), Type: resolvedTypeID, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.CharLit:
		runeValue, _ := utf8.DecodeRuneInString(node.Value)
		return &ir.IntLit{Value: fmt.Sprintf("%d", runeValue), Type: resolvedTypeID, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.BoolLit:
		return &ir.BoolLit{Value: node.Value, Type: resolvedTypeID, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.NoneLit:
		if none := lowerOptionalAbsent(ctx, resolvedTypeID, loc); none != nil {
			return none
		}
		return &ir.InvalidExpr{Message: "`none` requires optional context", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.Ident:
		return lowerIdentExpr(module, node, resolvedTypeID)

	case *ast.ScopeResolution:
		var sym *symbols.Symbol
		if module != nil && module.Bindings != nil {
			sym = module.Bindings.Symbol(node)
		}
		if sym != nil {
			return &ir.Ident{Name: symbolName(module, sym), Type: resolvedTypeID, SymbolID: sym.ID, SourceInfo: ir.SourceInfo{Location: loc}}
		}
		return &ir.InvalidExpr{Message: "unresolved qualified identifier: " + node.TypeText(), Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.UnaryExpr:
		arg := lowerASTExpr(ctx, module, scope, node.Expr, expectedType)
		return &ir.Unary{Op: node.Op, Arg: arg, Type: resolvedTypeID, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.AddressExpr:
		if node.Mode == ast.AddressShared || node.Mode == ast.AddressMutable {
			return lowerReferenceValue(ctx, module, scope, node.Expr, resolvedType, resolvedTypeID)
		}
		return &ir.AddrOf{Place: lowerPlace(ctx, module, scope, node.Expr), Type: resolvedTypeID, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.BinaryExpr:
		if module.Typechecking.StringConcatenation(node.ID()) {
			return &ir.StringConcat{
				Left:       lowerASTExpr(ctx, module, scope, node.Left, &typeinfo.StringType{}),
				Right:      lowerASTExpr(ctx, module, scope, node.Right, &typeinfo.RefType{Target: &typeinfo.StringType{}}),
				Type:       resolvedTypeID,
				SourceInfo: ir.SourceInfo{Location: loc},
			}
		}
		leftType := exprResolvedType(module, node.Left)
		rightType := exprResolvedType(module, node.Right)
		leftExpected := resolvedType
		rightExpected := resolvedType
		switch node.Op {
		case "<<", ">>":
			leftExpected = leftType
			rightExpected = rightType
		case "==", "!=", "<", "<=", ">", ">=", "&&", "||":
			leftExpected = leftType
			rightExpected = rightType
			if conversion, ok := module.Typechecking.ImplicitConversion(node.Left.ID()); ok && conversion.Compatibility == typeinfo.Compatible {
				leftExpected = rightType
			}
			if conversion, ok := module.Typechecking.ImplicitConversion(node.Right.ID()); ok && conversion.Compatibility == typeinfo.Compatible {
				rightExpected = leftType
			}
		}
		left := lowerASTExpr(ctx, module, scope, node.Left, leftExpected)
		right := lowerASTExpr(ctx, module, scope, node.Right, rightExpected)
		return &ir.Binary{Op: node.Op, Left: left, Right: right, Type: resolvedTypeID, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.CallExpr:
		effectiveArgs, ok := module.Typechecking.CallArguments(node.ID())
		if !ok {
			return &ir.InvalidExpr{Message: "call missing effective argument evidence", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: loc}}
		}
		if compilerCall, ok := module.Typechecking.CompilerCall(node.ID()); ok {
			switch compilerCall.Kind {
			case intrinsics.FunctionAlloc:
				return lowerAllocCall(ctx, module, scope, node, effectiveArgs)
			case intrinsics.FunctionCollection:
				return lowerCollectionCall(ctx, module, scope, node, effectiveArgs, compilerCall.Operation)
			case intrinsics.FunctionDynamicArrayOwner:
				return lowerDynamicArrayOwnerCall(ctx, module, scope, node, effectiveArgs, compilerCall.Operation)
			case intrinsics.FunctionFromBytes:
				return lowerStringFromBytesCall(ctx, module, scope, node, effectiveArgs)
			default:
				panic(fmt.Sprintf("unsupported intrinsic function kind %d for %q", compilerCall.Kind, compilerCall.Operation))
			}
		}
		if selector, ok := node.Callee.(*ast.SelectorExpr); ok && selector != nil {
			return lowerSelectorMethodCall(ctx, module, scope, selector, node, effectiveArgs)
		}
		calleeExpr := lowerASTExpr(ctx, module, scope, node.Callee, nil)
		args := make([]ir.Expr, 0, len(effectiveArgs))
		var fnType *typeinfo.FuncType
		if resolved := exprResolvedType(module, node.Callee); resolved != nil {
			fnType, _ = typeinfo.Underlying(resolved).(*typeinfo.FuncType)
		}
		for _, arg := range effectiveArgs {
			var paramExpected typeinfo.Type
			if fnType != nil && len(args) < len(fnType.Params) {
				paramExpected = fnType.Params[len(args)]
			}
			if implicit := module.Typechecking.ImplicitCallArgument(arg.ID()); implicit != nil {
				args = append(args, lowerImplicitReferenceValue(ctx, module, scope, arg, implicit))
			} else {
				args = append(args, lowerASTExpr(ctx, module, scope, arg, paramExpected))
			}
		}
		callType := resolvedTypeID
		if callType == ir.InvalidType && fnType != nil {
			callType = typelower.ReturnType(ctx.types, ctx.diagnostics, fnType.Return)
		}
		return &ir.Call{Callee: calleeExpr, Args: args, Type: callType, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.PrintExpr:
		return &ir.Print{Value: lowerASTExpr(ctx, module, scope, node.Expr, nil), Newline: node.Newline, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.FreeExpr:
		return &ir.Drop{Value: lowerASTExpr(ctx, module, scope, node.Expr, nil), SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.AsExpr:
		subExpr := lowerASTExpr(ctx, module, scope, node.Expr, expectedType)
		return &ir.Cast{Expr: subExpr, Type: resolvedTypeID, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.SelectorExpr:
		return lowerSelectorExpr(ctx, module, scope, node)

	case *ast.IndexExpr:
		return lowerIndexExpr(ctx, module, scope, node)

	case *ast.StructLit:
		return lowerStructLiteralExpr(ctx, module, scope, node)

	case *ast.VariantLit:
		return &ir.InvalidExpr{Message: "enum variant construction evidence missing", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.IsExpr:
		return &ir.InvalidExpr{Message: "enum case-test evidence missing", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: loc}}

	case *ast.ArrayLit:
		return lowerArrayLiteralExpr(ctx, module, scope, node)

	case *ast.BadExpr:
		return &ir.InvalidExpr{Message: "unsupported expression", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: loc}}

	default:
		panic(fmt.Sprintf("HIR lowering: unhandled expression %T", expr))
	}
}

func lowerCollectionCall(ctx *lowering, module *module.Module, scope *symbols.Scope, call *ast.CallExpr, effectiveArgs []ast.Expr, op symbols.CompilerOp) ir.Expr {
	fnType, _ := exprResolvedType(module, call.Callee).(*typeinfo.FuncType)
	if fnType == nil || len(fnType.Params) != 1 || len(effectiveArgs) != 1 {
		return &ir.InvalidExpr{Message: "collection function type or arguments missing", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(call)}}
	}
	value := effectiveArgs[0]
	var receiver ir.Expr
	if implicit := module.Typechecking.ImplicitCallArgument(value.ID()); implicit != nil {
		receiver = lowerImplicitReferenceValue(ctx, module, scope, value, implicit)
	} else {
		receiver = lowerASTExpr(ctx, module, scope, value, fnType.Params[0])
	}
	switch op {
	case symbols.CompilerOpLen:
		return &ir.Len{Value: receiver, Type: typelower.ReturnType(ctx.types, ctx.diagnostics, fnType.Return), SourceInfo: ir.SourceInfo{Location: ast.LocOf(call)}}
	case symbols.CompilerOpAsBytes:
		return &ir.SliceView{
			Place:      &ir.Place{Root: receiver, Type: receiver.TypeID(), Location: ast.LocOf(value)},
			Type:       typelower.ReturnType(ctx.types, ctx.diagnostics, fnType.Return),
			SourceInfo: ir.SourceInfo{Location: ast.LocOf(call)},
		}
	case symbols.CompilerOpAsChars:
		return &ir.StringChars{
			Value:      receiver,
			Type:       typelower.ReturnType(ctx.types, ctx.diagnostics, fnType.Return),
			SourceInfo: ir.SourceInfo{Location: ast.LocOf(call)},
		}
	default:
		return &ir.InvalidExpr{Message: "unsupported collection function lowering", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(call)}}
	}
}

func lowerOptionalAbsent(ctx *lowering, typeID ir.TypeID, loc *source.Location) ir.Expr {
	if ctx == nil || ctx.types == nil {
		return nil
	}
	typ, ok := ctx.types.Type(typeID)
	if _, optional := typ.OptionalPayload(); !ok || !optional {
		return nil
	}
	return &ir.VariantMake{Case: ir.OptionalAbsentCase, Type: typeID, SourceInfo: ir.SourceInfo{Location: loc}}
}

func optionalPromotionInnerType(expectedType, resolvedType typeinfo.Type, expr ast.Expr) typeinfo.Type {
	if expectedType == nil || resolvedType == nil || expr == nil {
		return nil
	}
	if _, ok := expr.(*ast.NoneLit); ok {
		return nil
	}
	expected, ok := typeinfo.Underlying(expectedType).(*typeinfo.OptionalType)
	if !ok || expected == nil || expected.Inner == nil || !typeinfo.SameType(expected.Inner, resolvedType) {
		return nil
	}
	return expected.Inner
}

func lowerSelectorMethodCall(ctx *lowering, module *module.Module, scope *symbols.Scope, selector *ast.SelectorExpr, call *ast.CallExpr, effectiveArgs []ast.Expr) ir.Expr {
	if module == nil || selector == nil || selector.Expr == nil || selector.Name == nil {
		return &ir.InvalidExpr{Message: "invalid selector call", Type: ir.InvalidType}
	}
	baseType := exprResolvedType(module, selector.Expr)
	if iface, slot, ok := lookupInterfaceMethod(module, baseType, selector.Name.Name); ok {
		interfaceType := typelower.Type(ctx.types, ctx.diagnostics, baseType)
		loweredMethod, lowered := ctx.types.InterfaceMethod(interfaceType, slot)
		if !lowered || loweredMethod.Name != iface.Name || loweredMethod.SlotType == ir.InvalidType {
			return &ir.InvalidExpr{Message: "missing lowered interface slot evidence", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(call)}}
		}
		args := make([]ir.Expr, 0, len(effectiveArgs))
		for i, arg := range effectiveArgs {
			var argExpected typeinfo.Type
			if i < len(iface.Params) {
				argExpected = iface.Params[i].Type
			}
			args = append(args, lowerASTExpr(ctx, module, scope, arg, argExpected))
		}
		consumes := iface.Receiver == typeinfo.MethodReceiverValue
		return &ir.InterfaceCall{
			Base:       lowerASTExpr(ctx, module, scope, selector.Expr, nil),
			Slot:       slot,
			SlotType:   loweredMethod.SlotType,
			Args:       args,
			Consumes:   consumes,
			Type:       loweredMethod.Return,
			SourceInfo: ir.SourceInfo{Location: ast.LocOf(call)},
		}
	}
	methodSym := module.Bindings.Symbol(selector.Name)
	fnType, _ := exprResolvedType(module, selector).(*typeinfo.FuncType)
	if methodSym == nil || fnType == nil || len(fnType.Params) == 0 {
		return &ir.InvalidExpr{Message: "unsupported selector call lowering", Type: ir.InvalidType}
	}
	if _, ok := typeinfo.ReceiverTarget(fnType.Params[0]); !ok {
		return &ir.InvalidExpr{Message: "selector method receiver missing", Type: ir.InvalidType}
	}
	var baseExpr ir.Expr
	if implicit := module.Typechecking.ImplicitCallArgument(selector.Expr.ID()); implicit != nil {
		baseExpr = lowerImplicitReferenceValue(ctx, module, scope, selector.Expr, implicit)
	} else {
		baseExpr = lowerASTExpr(ctx, module, scope, selector.Expr, fnType.Params[0])
	}
	args := make([]ir.Expr, 0, len(effectiveArgs)+1)
	args = append(args, baseExpr)
	for i, arg := range effectiveArgs {
		var argExpected typeinfo.Type
		if i+1 < len(fnType.Params) {
			argExpected = fnType.Params[i+1]
		}
		args = append(args, lowerASTExpr(ctx, module, scope, arg, argExpected))
	}
	return &ir.Call{
		Callee: &ir.Ident{
			Name:       symbolName(module, methodSym),
			Type:       typelower.Type(ctx.types, ctx.diagnostics, fnType),
			SymbolID:   methodSym.ID,
			SourceInfo: ir.SourceInfo{Location: ast.LocOf(selector.Name)},
		},
		Args:       args,
		Type:       typelower.ReturnType(ctx.types, ctx.diagnostics, fnType.Return),
		SourceInfo: ir.SourceInfo{Location: ast.LocOf(call)},
	}
}

func lowerSelectorExpr(ctx *lowering, module *module.Module, scope *symbols.Scope, selector *ast.SelectorExpr) ir.Expr {
	if module == nil || selector == nil || selector.Expr == nil || selector.Name == nil {
		return &ir.InvalidExpr{Message: "invalid selector", Type: ir.InvalidType}
	}
	if module.Flow != nil {
		if _, found := module.Flow.VariantField(selector.ID()); found {
			return &ir.Load{Place: lowerPlace(ctx, module, scope, selector), SourceInfo: ir.SourceInfo{NodeID: ir.NodeID(selector.ID()), Location: ast.LocOf(selector)}}
		}
	}
	if module.Typechecking != nil {
		if access, found := module.Typechecking.StructField(selector.ID()); found {
			exprType := func(expr ast.Expr) typeinfo.Type {
				return exprResolvedType(module, expr)
			}
			if access.DereferenceType != nil || place.Addressable(scope, selector.Expr, exprType, module.ExpandedDefaultBinding) {
				return &ir.Load{Place: lowerPlace(ctx, module, scope, selector), SourceInfo: ir.SourceInfo{NodeID: ir.NodeID(selector.ID()), Location: ast.LocOf(selector)}}
			}
			return &ir.Field{
				Base:       lowerASTExpr(ctx, module, scope, selector.Expr, nil),
				Index:      access.Field,
				SourceInfo: ir.SourceInfo{NodeID: ir.NodeID(selector.ID()), Location: ast.LocOf(selector)},
				Type:       typelower.Type(ctx.types, ctx.diagnostics, access.Type),
			}
		}
	}
	return &ir.InvalidExpr{Message: "selector lowering not implemented", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(selector)}}
}

func lowerIndexExpr(ctx *lowering, module *module.Module, scope *symbols.Scope, node *ast.IndexExpr) ir.Expr {
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
			Place:        source,
			Start:        start,
			End:          end,
			EndExclusive: rangeIndex.EndExclusive,
			Type:         typelower.Type(ctx.types, ctx.diagnostics, resultType),
			SourceInfo:   ir.SourceInfo{Location: ast.LocOf(node)},
		}
	}
	return &ir.Load{Place: lowerPlace(ctx, module, scope, node), SourceInfo: ir.SourceInfo{NodeID: ir.NodeID(node.ID()), Location: ast.LocOf(node)}}
}

func lowerStructLiteralExpr(ctx *lowering, module *module.Module, scope *symbols.Scope, node *ast.StructLit) ir.Expr {
	if module == nil || node == nil {
		return &ir.InvalidExpr{Message: "invalid struct literal", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)}}
	}
	resolved := exprResolvedType(module, node)
	strct, ok := typeinfo.Underlying(resolved).(*typeinfo.StructType)
	if !ok || strct == nil {
		return &ir.InvalidExpr{Message: "struct literal type missing", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)}}
	}
	ordered, ok := module.Typechecking.StructLiteralFields(node.ID())
	if !ok || len(ordered) != len(strct.Fields) {
		return &ir.InvalidExpr{Message: "struct literal missing ordered field evidence", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)}}
	}
	semanticStruct, _ := typeinfo.Underlying(resolved).(*typeinfo.StructType)
	values := make([]ir.Expr, 0, len(ordered))
	for index, value := range ordered {
		fieldType := strct.Fields[index].Type
		if semanticStruct != nil && index < len(semanticStruct.Fields) {
			fieldType = semanticStruct.Fields[index].Type
		}
		values = append(values, lowerASTExpr(ctx, module, scope, value, fieldType))
	}
	return &ir.StructLit{
		Fields:     values,
		Type:       typelower.Type(ctx.types, ctx.diagnostics, resolved),
		SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)},
	}
}

func lowerArrayLiteralExpr(ctx *lowering, module *module.Module, scope *symbols.Scope, node *ast.ArrayLit) ir.Expr {
	if module == nil || node == nil {
		return &ir.InvalidExpr{Message: "invalid array literal", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)}}
	}
	resolved := exprResolvedType(module, node)
	array, ok := typeinfo.Underlying(resolved).(*typeinfo.ArrayType)
	if !ok || array == nil || array.Elem == nil {
		return &ir.InvalidExpr{Message: "array literal type missing", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)}}
	}
	elementType := array.Elem
	if semanticArray, ok := typeinfo.Underlying(resolved).(*typeinfo.ArrayType); ok && semanticArray != nil && semanticArray.Elem != nil {
		elementType = semanticArray.Elem
	}
	values := make([]ir.Expr, 0, len(node.Values))
	for _, value := range node.Values {
		values = append(values, lowerASTExpr(ctx, module, scope, value, elementType))
	}
	return &ir.ArrayLit{
		Values:     values,
		Dynamic:    array.Shape == typeinfo.ArrayOwner,
		Type:       typelower.Type(ctx.types, ctx.diagnostics, resolved),
		SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)},
	}
}

func lowerDynamicArrayOwnerCall(ctx *lowering, module *module.Module, scope *symbols.Scope, node *ast.CallExpr, effectiveArgs []ast.Expr, op symbols.CompilerOp) ir.Expr {
	fnType, _ := typeinfo.Underlying(exprResolvedType(module, node.Callee)).(*typeinfo.FuncType)
	if fnType == nil || len(fnType.Params) != len(effectiveArgs) || len(effectiveArgs) < 2 {
		return &ir.InvalidExpr{Message: "dynamic-array operation type or arguments missing", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)}}
	}
	args := make([]ir.Expr, 0, len(effectiveArgs))
	for i, arg := range effectiveArgs {
		if implicit := module.Typechecking.ImplicitCallArgument(arg.ID()); implicit != nil {
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
		ArrayType:  typelower.Type(ctx.types, ctx.diagnostics, ownerType),
		Type:       typelower.ReturnType(ctx.types, ctx.diagnostics, nil),
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

func lowerAllocCall(ctx *lowering, module *module.Module, scope *symbols.Scope, node *ast.CallExpr, effectiveArgs []ast.Expr) ir.Expr {
	if len(effectiveArgs) < 1 || len(effectiveArgs) > 2 {
		return &ir.InvalidExpr{Message: "alloc requires 1 or 2 effective arguments", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)}}
	}
	value := lowerASTExpr(ctx, module, scope, effectiveArgs[0], nil)
	var allocator ir.Expr
	if len(effectiveArgs) > 1 {
		allocator = lowerASTExpr(ctx, module, scope, effectiveArgs[1], &typeinfo.AllocatorType{})
	}
	resultType := typelower.Type(ctx.types, ctx.diagnostics, exprResolvedType(module, node))
	return &ir.AllocExpr{
		Value:      value,
		Allocator:  allocator,
		Type:       resultType,
		SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)},
	}
}

func lowerStringFromBytesCall(ctx *lowering, module *module.Module, scope *symbols.Scope, node *ast.CallExpr, effectiveArgs []ast.Expr) ir.Expr {
	fnType, _ := exprResolvedType(module, node.Callee).(*typeinfo.FuncType)
	if fnType == nil || len(fnType.Params) != 2 || len(effectiveArgs) < 1 || len(effectiveArgs) > 2 {
		panic("validated from_bytes call missing intrinsic signature or effective arguments")
	}
	bytes := lowerASTExpr(ctx, module, scope, effectiveArgs[0], fnType.Params[0])
	var allocator ir.Expr
	if len(effectiveArgs) == 2 {
		allocator = lowerASTExpr(ctx, module, scope, effectiveArgs[1], fnType.Params[1])
	}
	return &ir.StringFromBytes{
		Bytes:      bytes,
		Allocator:  allocator,
		Type:       typelower.ReturnType(ctx.types, ctx.diagnostics, fnType.Return),
		SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)},
	}
}

func exprResolvedType(module *module.Module, expr ast.Expr) typeinfo.Type {
	if module == nil || expr == nil {
		return nil
	}
	return module.EffectiveExprType(expr.ID())
}

func lowerIdentExpr(module *module.Module, node *ast.Ident, typeID ir.TypeID) ir.Expr {
	if node == nil {
		return &ir.InvalidExpr{Message: "nil identifier", Type: ir.InvalidType}
	}
	var sym *symbols.Symbol
	if module != nil && module.Bindings != nil {
		sym = module.Bindings.Symbol(node)
	}
	if sym == nil {
		return &ir.InvalidExpr{Message: "unresolved identifier: " + node.Name, Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)}}
	}

	return &ir.Ident{
		Name: symbolName(module, sym), Type: typeID, SymbolID: sym.ID,
		SourceInfo: ir.SourceInfo{Location: ast.LocOf(node)},
	}
}

func lowerNumberLit(ctx *lowering, module *module.Module, node *ast.NumberLit, expectedType typeinfo.Type, loc *source.Location) ir.Expr {
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
		return &ir.InvalidExpr{Message: "number literal missing resolved type evidence", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: loc}}
	}
	family, _, numericType := typeinfo.NumericInfo(expectedType)
	if numericType && family == typeinfo.NumericFloat {
		v := node.Value
		if !numeric.IsFloat(node.Value) {
			v = integerValue + ".0"
		}
		return &ir.FloatLit{Value: v, Type: typelower.Type(ctx.types, ctx.diagnostics, expectedType), SourceInfo: ir.SourceInfo{Location: loc}}
	}
	return &ir.IntLit{Value: integerValue, Type: typelower.Type(ctx.types, ctx.diagnostics, expectedType), SourceInfo: ir.SourceInfo{Location: loc}}
}

func symbolName(module *module.Module, sym *symbols.Symbol) string {
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

func callableName(module *module.Module, sym *symbols.Symbol) (string, bool) {
	if sym == nil || (sym.Kind != symbols.SymbolFunc && sym.Kind != symbols.SymbolMethod) {
		return "", false
	}
	if fn, ok := sym.ASTNode.(*ast.FnDecl); ok {
		if name, external := ast.FunctionLinkName(fn, sym.Name); external {
			return name, true
		}
	}
	if module != nil && module.IsEntry && sym.Kind == symbols.SymbolFunc && sym.Name == "main" && sym.DefiningModule == module.ID {
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

func shouldDiscardBindingValue(sym *symbols.Symbol) bool {
	if sym == nil || sym.IsUsed() {
		return false
	}
	if typ, ok := symbols.GetSymbolType(sym); ok && typeinfo.OwnershipCapabilityOf(typ).Drop {
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
