package consteval

import (
	"compiler/internal/constvalue"
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
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
			e.evalConstSymbol(sym)
		}
	}
}

func (e *evaluator) evalConstSymbol(sym *symbols.Symbol) (constvalue.Value, bool) {
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
	expected := typeinfo.Type(nil)
	if sym.Type != nil && !typeinfo.IsInvalidOrUnknown(sym.Type) {
		expected = sym.Type
	}
	value, ok := e.evalExpr(decl.Value, expected)
	delete(e.inProgress, sym.ID)
	if !ok {
		return nil, false
	}
	e.module.Semantics.ConstValues[sym.ID] = value
	return value, true
}

func (e *evaluator) evalExpr(expr ast.Expr, expected typeinfo.Type) (constvalue.Value, bool) {
	if expected != nil && !typeinfo.IsArithmetic(typeinfo.Underlying(expected)) {
		expected = nil
	}
	switch node := expr.(type) {
	case *ast.NumberLit:
		typ := typeinfo.DefaultNumberType(node.Value)
		if expected != nil {
			typ = expected
		}
		typText := typeinfo.TypeText(typ)
		if _, ok := typeinfo.Underlying(typ).(*typeinfo.FloatType); ok {
			return &constvalue.FloatConst{Value: node.Value, TypeID: typText}, true
		}
		return &constvalue.IntConst{Value: node.Value, TypeID: typText}, true
	case *ast.BoolLit:
		return &constvalue.BoolConst{Value: node.Value}, true
	case *ast.Ident:
		sym, ok := e.module.ModuleScope.Lookup(node.Name)
		if !ok || sym == nil || sym.Kind != symbols.SymbolConst {
			return nil, false
		}
		return e.evalConstSymbol(sym)
	case *ast.UnaryExpr:
		value, ok := e.evalExpr(node.Expr, expected)
		if !ok {
			return nil, false
		}
		return constvalue.FoldUnary(node.Op, value)
	case *ast.BinaryExpr:
		left, lok := e.evalExpr(node.Left, expected)
		right, rok := e.evalExpr(node.Right, expected)
		if !lok || !rok {
			return nil, false
		}
		return constvalue.FoldBinary(node.Op, left, right)
	default:
		return nil, false
	}
}
