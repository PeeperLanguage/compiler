package consteval

import (
	"compiler/internal/constvalue"
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/pkg/numeric"
)

type evaluator struct {
	ctx        *project.CompilerContext
	module     *project.Module
	inProgress map[symbols.SymbolID]struct{}
}

// Evaluate performs the eager semantic const prepass after name resolution.
// Typechecking may still request expected-type-sensitive values through
// EvaluateExpr before final symbol types are known.
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

// FinalizeValues recomputes module constants after typechecking assigns final
// symbol types. Local const cache entries remain available to later queries.
func FinalizeValues(ctx *project.CompilerContext, module *project.Module) {
	if ctx == nil || module == nil || module.ModuleScope == nil || module.Semantics == nil {
		return
	}
	for _, sym := range module.ModuleScope.Symbols() {
		if sym != nil && sym.Kind == symbols.SymbolConst {
			delete(module.Semantics.ConstValues, sym.ID)
		}
	}
	Evaluate(ctx, module)
}

// EvaluateExpr computes one semantic constant using expected type information
// available at the query site. It is valid during and after typechecking.
func EvaluateExpr(ctx *project.CompilerContext, module *project.Module, scope *symbols.Scope, expr ast.Expr, expected typeinfo.Type) (constvalue.Value, bool) {
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

func (e *evaluator) evalConstSymbol(sym *symbols.Symbol, scope *symbols.Scope) (constvalue.Value, bool) {
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

func (e *evaluator) evalExpr(scope *symbols.Scope, expr ast.Expr, expected typeinfo.Type) (constvalue.Value, bool) {
	if node, ok := expr.(*ast.StringLit); ok {
		typText := "str"
		if node.CString {
			typText = "cstr"
		}
		switch typeinfo.Underlying(expected).(type) {
		case *typeinfo.CStrType, *typeinfo.StringType:
			typText = typeinfo.TypeText(expected)
		}
		return constvalue.NewString(node.Value, typText)
	}
	_, _, numericExpected := typeinfo.NumericInfo(expected)
	if expected != nil && !numericExpected {
		expected = nil
	}
	switch node := expr.(type) {
	case *ast.NumberLit:
		typ := typeinfo.DefaultNumberType(node.Value)
		if node.ExplicitType != "" {
			if explicit, ok := typeinfo.NumericTypeFromName(node.ExplicitType, e.ctx.Target); ok {
				typ = explicit
			} else {
				return nil, false
			}
		} else if expected != nil {
			typ = expected
		}
		typText := typeinfo.TypeText(typeinfo.Underlying(typ))
		family, _, _ := typeinfo.NumericInfo(typ)
		if family == typeinfo.NumericFloat {
			return constvalue.NewFloatText(node.Value, typText)
		}
		value, err := numeric.CanonicalizeIntegerLiteral(node.Value)
		if err != nil {
			return nil, false
		}
		return constvalue.NewIntText(value, typText)
	case *ast.BoolLit:
		return constvalue.NewBool(node.Value), true
	case *ast.Ident:
		lookup := scope
		if lookup == nil {
			lookup = e.module.ModuleScope
		}
		sym, ok := lookup.Lookup(node.Name)
		if !ok || sym == nil || sym.Kind != symbols.SymbolConst {
			return nil, false
		}
		value, ok := e.evalConstSymbol(sym, lookup)
		if !ok {
			return nil, false
		}
		return expectedNumericConstValue(value, expected)
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

func expectedNumericConstValue(value constvalue.Value, expected typeinfo.Type) (constvalue.Value, bool) {
	if expected == nil {
		return value, true
	}
	family, _, ok := typeinfo.NumericInfo(expected)
	if !ok {
		return value, true
	}
	typeText := typeinfo.TypeText(typeinfo.Underlying(expected))
	switch v := value.(type) {
	case *constvalue.IntConst:
		if v == nil {
			return nil, false
		}
		if family == typeinfo.NumericFloat {
			return constvalue.NewFloatText(v.Text(), typeText)
		}
		return constvalue.NewInt(v.Int(), typeText)
	case *constvalue.FloatConst:
		if v == nil {
			return nil, false
		}
		if family == typeinfo.NumericFloat {
			return constvalue.NewFloat(v.Float(), typeText)
		}
		return nil, false
	default:
		return value, true
	}
}
