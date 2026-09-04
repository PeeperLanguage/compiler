package consteval

import (
	"compiler/internal/constvalue"
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/constantresult"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/pkg/numeric"
)

type evaluator struct {
	ctx                 *project.CompilerContext
	module              *project.Module
	constants           *constantresult.Result
	inProgress          map[symbols.SymbolID]struct{}
	publishModuleValues bool
}

// Evaluate performs the eager semantic const prepass after name resolution.
// Typechecking may still request expected-type-sensitive values through
// EvaluateExpr before final symbol types are known.
func Evaluate(ctx *project.CompilerContext, module *project.Module) {
	if ctx == nil || module == nil || module.ModuleScope == nil {
		return
	}
	e := newEvaluator(ctx, module, false)
	e.evalModuleConstants()
}

// FinalizeValues recomputes and publishes authoritative module constants after
// typechecking assigns final symbol types. Local query-cache entries remain mutable.
func FinalizeValues(ctx *project.CompilerContext, module *project.Module) {
	if ctx == nil || module == nil || module.ModuleScope == nil {
		return
	}
	e := newEvaluator(ctx, module, true)
	clear(e.constants.ModuleValues)
	for _, sym := range module.ModuleScope.Symbols() {
		if sym != nil && sym.Kind == symbols.SymbolConst {
			delete(e.constants.QueryCache, sym.ID)
		}
	}
	e.evalModuleConstants()
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
	e := newEvaluator(ctx, module, false)
	return e.evalExpr(scope, expr, expected)
}

func newEvaluator(ctx *project.CompilerContext, module *project.Module, publishModuleValues bool) *evaluator {
	if module.Constants == nil {
		module.Constants = constantresult.New()
	}
	return &evaluator{
		ctx:                 ctx,
		module:              module,
		constants:           module.Constants,
		inProgress:          make(map[symbols.SymbolID]struct{}),
		publishModuleValues: publishModuleValues,
	}
}

func (e *evaluator) evalModuleConstants() {
	for _, sym := range e.module.ModuleScope.Symbols() {
		if sym != nil && sym.Kind == symbols.SymbolConst {
			e.evalConstSymbol(sym, e.module.ModuleScope)
		}
	}
}

func (e *evaluator) evalConstSymbol(sym *symbols.Symbol, scope *symbols.Scope) (constvalue.Value, bool) {
	if e == nil || e.module == nil || sym == nil {
		return nil, false
	}
	if ownerID := sym.DefiningModule; ownerID.Valid() && ownerID != e.module.ID {
		value := e.ctx.PublishedConstant(e.module, sym)
		return value, value != nil
	}
	if value, ok := e.constants.ModuleValues[sym.ID]; ok {
		return value, true
	}
	if value, ok := e.constants.QueryCache[sym.ID]; ok {
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
	if e.publishModuleValues {
		if topLevel, found := e.module.ModuleScope.LookupLocal(sym.Name); found && topLevel != nil && topLevel.ID == sym.ID {
			e.constants.ModuleValues[sym.ID] = value
			delete(e.constants.QueryCache, sym.ID)
			return value, true
		}
	}
	e.constants.QueryCache[sym.ID] = value
	return value, true
}

func (e *evaluator) evalExpr(scope *symbols.Scope, expr ast.Expr, expected typeinfo.Type) (constvalue.Value, bool) {
	if e.module.Typechecking != nil {
		if construction, ok := e.module.Typechecking.VariantConstructions[expr.ID()]; ok {
			if typeinfo.OwnershipCapabilityOf(construction.EnumType).Copy != typeinfo.CopyImplicit {
				return nil, false
			}
			descriptor, variant := typeinfo.VariantDescriptorOf(construction.EnumType)
			if !variant || construction.Case < 0 || construction.Case >= len(descriptor.Cases) {
				return nil, false
			}
			if construction.Value != nil {
				if literal, ok := construction.Value.(*ast.StructLit); ok {
					payload, structured := typeinfo.Underlying(construction.Payload).(*typeinfo.StructType)
					if !structured || payload == nil {
						return nil, false
					}
					valuesByName := make(map[string]ast.Expr, len(literal.Fields))
					for _, field := range literal.Fields {
						if field.Name != nil {
							valuesByName[field.Name.Name] = field.Value
						}
					}
					fields := make([]constvalue.Value, len(payload.Fields))
					for index, field := range payload.Fields {
						valueExpr := valuesByName[field.Name]
						value, ok := e.evalExpr(scope, valueExpr, field.Type)
						if !ok {
							return nil, false
						}
						fields[index] = value
					}
					return constvalue.NewVariant(descriptor.Identity, typeinfo.TypeText(construction.EnumType), construction.Case, fields)
				}
				value, ok := e.evalExpr(scope, construction.Value, construction.Payload)
				if !ok {
					return nil, false
				}
				return constvalue.NewVariant(descriptor.Identity, typeinfo.TypeText(construction.EnumType), construction.Case, []constvalue.Value{value})
			}
			return constvalue.NewVariant(descriptor.Identity, typeinfo.TypeText(construction.EnumType), construction.Case, nil)
		}
	}
	if node, ok := expr.(*ast.IsExpr); ok {
		if e.module.Typechecking == nil {
			return nil, false
		}
		test, found := e.module.Typechecking.CaseTests[node.ID()]
		if !found || test.Family != typeinfo.VariantFamilyNamed {
			return nil, false
		}
		value, ok := e.evalExpr(scope, node.Value, e.module.BaseExprType(node.Value.ID()))
		variant, constant := value.(*constvalue.VariantConst)
		if !ok || !constant || variant == nil {
			return nil, false
		}
		matched := variant.CaseIndex() == test.Case
		if !test.CaseWhenTrue {
			matched = !matched
		}
		return constvalue.NewBool(matched), true
	}
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
