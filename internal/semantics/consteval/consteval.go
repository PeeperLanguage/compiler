package consteval

import (
	"compiler/internal/constvalue"
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
)

type evaluator struct {
	ctx        *project.CompilerContext
	module     *project.Module
	inProgress map[symbols.SymbolID]struct{}
}

// Evaluate fills semantic const values after names resolve and before
// typechecking consumes const-dependent facts. Arithmetic itself is shared with
// HIR folding through constvalue, so diagnostics and optimization never fork.
func Evaluate(ctx *project.CompilerContext, module *project.Module) {
	if ctx == nil || module == nil || module.ModuleScope == nil {
		return
	}
	if module.Semantics == nil {
		module.Semantics = project.NewSemanticInfo()
	}
	if module.Semantics.ConstValues == nil {
		module.Semantics.ConstValues = make(map[symbols.SymbolID]constvalue.Value)
	}
	e := &evaluator{
		ctx:        ctx,
		module:     module,
		inProgress: make(map[symbols.SymbolID]struct{}),
	}
	for _, sym := range module.ModuleScope.Symbols() {
		if sym != nil && sym.Kind == symbols.SymbolConst {
			e.evalConstSymbol(sym, module.ModuleScope)
		}
	}
}

func EvaluateExpr(ctx *project.CompilerContext, module *project.Module, scope *table.Scope, expr ast.Expr, expected typeinfo.Type) (constvalue.Value, bool) {
	if ctx == nil || module == nil || expr == nil {
		return nil, false
	}
	if scope == nil && module.ModuleScope == nil {
		return nil, false
	}
	if module.Semantics == nil {
		module.Semantics = project.NewSemanticInfo()
	}
	if module.Semantics.ConstValues == nil {
		module.Semantics.ConstValues = make(map[symbols.SymbolID]constvalue.Value)
	}
	e := &evaluator{
		ctx:        ctx,
		module:     module,
		inProgress: make(map[symbols.SymbolID]struct{}),
	}
	return e.evalExpr(scope, expr, expected)
}

func (e *evaluator) evalConstSymbol(sym *symbols.Symbol, scope *table.Scope) (constvalue.Value, bool) {
	if e == nil || e.module == nil || e.module.Semantics == nil || sym == nil {
		return nil, false
	}
	if value, ok := e.module.Semantics.ConstValues[sym.ID]; ok {
		return value, true
	}
	if _, ok := e.inProgress[sym.ID]; ok {
		e.ctx.Diagnostics.AddError(
			diagnostics.ErrCircularDependency,
			"constant evaluation cycle involving `"+sym.Name+"`",
			sym.Location,
			"constant depends on itself",
		)
		return nil, false
	}
	decl, ok := sym.ASTNode.(*ast.ConstDecl)
	if !ok || decl == nil || decl.Value == nil {
		return nil, false
	}
	e.inProgress[sym.ID] = struct{}{}
	valueScope := scope
	if e.module.ModuleScope != nil {
		if found, ok := e.module.ModuleScope.LookupLocal(sym.Name); ok && found != nil && found.ID == sym.ID {
			valueScope = e.module.ModuleScope
		}
	}
	if valueScope == nil {
		valueScope = e.module.ModuleScope
	}
	expected := typeinfo.Type(nil)
	if sym.Type != nil && !typeinfo.IsInvalidOrUnknown(sym.Type) {
		expected = sym.Type
	}
	value, ok := e.evalExpr(valueScope, decl.Value, expected)
	delete(e.inProgress, sym.ID)
	if !ok {
		return nil, false
	}
	e.module.Semantics.ConstValues[sym.ID] = value
	return value, true
}

func (e *evaluator) evalExpr(scope *table.Scope, expr ast.Expr, expected typeinfo.Type) (constvalue.Value, bool) {
	_, _, numericExpected := typeinfo.NumericInfo(expected)
	if expected != nil && !numericExpected {
		expected = nil
	}
	switch node := expr.(type) {
	case *ast.NumberLit:
		typ := typeinfo.DefaultNumberType(node.Value)
		if expected != nil {
			typ = expected
		}
		typText := typeinfo.TypeText(typ)
		family, _, _ := typeinfo.NumericInfo(typ)
		if family == typeinfo.NumericFloat {
			return &constvalue.FloatConst{Value: node.Value, TypeID: typText}, true
		}
		return &constvalue.IntConst{Value: node.Value, TypeID: typText}, true
	case *ast.BoolLit:
		return &constvalue.BoolConst{Value: node.Value}, true
	case *ast.Ident:
		lookup := scope
		if lookup == nil {
			lookup = e.module.ModuleScope
		}
		sym, ok := lookup.Lookup(node.Name)
		if !ok || sym == nil || sym.Kind != symbols.SymbolConst {
			return nil, false
		}
		return e.evalConstSymbol(sym, lookup)
	case *ast.UnaryExpr:
		value, ok := e.evalExpr(scope, node.Expr, expected)
		if !ok {
			return nil, false
		}
		return constvalue.FoldUnary(node.Op, value)
	case *ast.BinaryExpr:
		left, lok := e.evalExpr(scope, node.Left, expected)
		right, rok := e.evalExpr(scope, node.Right, expected)
		if !lok || !rok {
			return nil, false
		}
		if folded, ok := constvalue.FoldBinary(node.Op, left, right); ok {
			return folded, true
		}
		if expected != nil {
			return nil, false
		}
		commonType := typeinfo.CommonNumericType(&typeinfo.NamedType{Name: left.TypeText()}, &typeinfo.NamedType{Name: right.TypeText()})
		if commonType == nil {
			return nil, false
		}
		left, lok = e.evalExpr(scope, node.Left, commonType)
		right, rok = e.evalExpr(scope, node.Right, commonType)
		if !lok || !rok {
			return nil, false
		}
		return constvalue.FoldBinary(node.Op, left, right)
	default:
		return nil, false
	}
}
