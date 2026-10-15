package typechecker

import (
	"math"
	"math/big"

	"compiler/internal/constvalue"
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/module"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeresolution"
	"compiler/internal/semantics/types"
	"compiler/pkg/numeric"
)

type constantEvaluator struct {
	ctx                 *project.CompilerContext
	module              *module.Module
	evidence            *evidence
	cache               map[symbols.SymbolID]constvalue.Value
	inProgress          map[symbols.SymbolID]struct{}
	publishModuleValues bool
}

func newConstantEvaluator(ctx *project.CompilerContext, module *module.Module, evidence *evidence) *constantEvaluator {
	return &constantEvaluator{
		ctx:        ctx,
		module:     module,
		evidence:   evidence,
		cache:      make(map[symbols.SymbolID]constvalue.Value),
		inProgress: make(map[symbols.SymbolID]struct{}),
	}
}

func (e *constantEvaluator) withContext(ctx *project.CompilerContext) *constantEvaluator {
	if e == nil || ctx == nil || ctx == e.ctx {
		return e
	}
	copy := *e
	copy.ctx = ctx
	return &copy
}

func (e *constantEvaluator) finalizeModuleValues() {
	if e == nil || e.module == nil || e.module.ModuleScope == nil || e.module.SymbolIndex == nil {
		return
	}
	e.module.SymbolIndex.ClearConstants()
	moduleSymbols := e.module.ModuleScope.Symbols()
	// Invalidate every module constant before evaluating any dependency.
	for _, sym := range moduleSymbols {
		if sym != nil && sym.Kind == symbols.SymbolConst {
			delete(e.cache, sym.ID)
		}
	}
	previous := e.publishModuleValues
	e.publishModuleValues = true
	for _, sym := range moduleSymbols {
		if sym != nil && sym.Kind == symbols.SymbolConst {
			e.evalConstSymbol(sym)
		}
	}
	e.publishModuleValues = previous
}

func (e *constantEvaluator) evalConstSymbol(sym *symbols.Symbol) (constvalue.Value, bool) {
	if e == nil || e.module == nil || sym == nil {
		return nil, false
	}
	if ownerID := sym.DefiningModule; ownerID.IsValid() && ownerID != e.module.ID {
		value := e.ctx.PublishedConstant(e.module, sym)
		return value, value != nil
	}
	if e.module.SymbolIndex != nil {
		if value := e.module.SymbolIndex.ConstantValue(sym.ID); value != nil {
			return value, true
		}
	}
	if value, ok := e.cache[sym.ID]; ok {
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
	expected := types.Type(nil)
	if sym.Type != nil && !types.IsInvalidOrUnknown(sym.Type) {
		expected = sym.Type
	}
	value, ok := e.evalExpr(decl.Value, expected)
	delete(e.inProgress, sym.ID)
	if !ok {
		return nil, false
	}
	if e.publishModuleValues {
		if topLevel, found := e.module.ModuleScope.LookupLocal(sym.Name); found && topLevel != nil && topLevel.ID == sym.ID {
			e.module.SymbolIndex.PublishConstant(sym.ID, value)
			delete(e.cache, sym.ID)
			return value, true
		}
	}
	e.cache[sym.ID] = value
	return value, true
}

func (e *constantEvaluator) evalExpr(expr ast.Expr, expected types.Type) (value constvalue.Value, evaluated bool) {
	if expr == nil {
		return nil, false
	}
	var checkedType types.Type
	if e.evidence != nil {
		checkedType = e.evidence.ExprType(expr.ID())
		if conversion, found := e.evidence.ImplicitConversion(expr.ID()); found && checkedType != nil &&
			conversion.Kind == types.ConversionNumeric && conversion.Compatibility == types.Compatible {
			// Fold at the checked source width before converting to the context's
			// destination. Widening earlier would change intermediate overflow.
			defer func(destination types.Type) {
				if evaluated {
					value, evaluated = expectedNumericConstValue(value, destination)
				}
			}(expected)
			expected = checkedType
		}
		if construction, ok := e.evidence.VariantConstruction(expr.ID()); ok {
			if types.OwnershipCapabilityOf(construction.EnumType).Copy != types.CopyImplicit {
				return nil, false
			}
			descriptor, isVariant := types.VariantDescriptorOf(construction.EnumType)
			if !isVariant || construction.Case < 0 || construction.Case >= len(descriptor.Cases) {
				return nil, false
			}
			if construction.Value != nil {
				if literal, ok := construction.Value.(*ast.StructLit); ok {
					payload, isStructured := types.Underlying(construction.Payload).(*types.StructType)
					if !isStructured || payload == nil {
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
						value, ok := e.evalExpr(valueExpr, field.Type)
						if !ok {
							return nil, false
						}
						fields[index] = value
					}
					return constvalue.NewVariant(descriptor.Identity, types.TypeText(construction.EnumType), construction.Case, fields)
				}
				value, ok := e.evalExpr(construction.Value, construction.Payload)
				if !ok {
					return nil, false
				}
				return constvalue.NewVariant(descriptor.Identity, types.TypeText(construction.EnumType), construction.Case, []constvalue.Value{value})
			}
			return constvalue.NewVariant(descriptor.Identity, types.TypeText(construction.EnumType), construction.Case, nil)
		}
	}
	if node, ok := expr.(*ast.IsExpr); ok {
		if e.evidence == nil {
			return nil, false
		}
		test, found := e.evidence.CaseTest(node.ID())
		if !found || test.Family != types.VariantFamilyNamed {
			return nil, false
		}
		value, ok := e.evalExpr(node.Value, e.evidence.ExprType(node.Value.ID()))
		variant, isConstant := value.(*constvalue.VariantConst)
		if !ok || !isConstant || variant == nil {
			return nil, false
		}
		matched := variant.CaseIndex() == test.Case
		if !test.MatchesWhenTrue {
			matched = !matched
		}
		return constvalue.NewBool(matched), true
	}
	if node, ok := expr.(*ast.StringLit); ok {
		typText := "str"
		if node.IsCString {
			typText = "cstr"
		}
		switch types.Underlying(expected).(type) {
		case *types.CStrType, *types.StringType:
			typText = types.TypeText(expected)
		}
		return constvalue.NewString(node.Value, typText)
	}
	_, _, numericExpected := types.NumericInfo(expected)
	if expected != nil && !numericExpected {
		expected = nil
	}
	switch node := expr.(type) {
	case *ast.NumberLit:
		typ := types.DefaultNumberType(node.Value)
		if node.ExplicitType != "" {
			if explicit, ok := types.NumericTypeFromName(node.ExplicitType, e.ctx.Target); ok {
				typ = explicit
			} else {
				return nil, false
			}
		} else if expected != nil {
			typ = expected
		}
		typText := types.TypeText(types.Underlying(typ))
		family, _, _ := types.NumericInfo(typ)
		if family == types.NumericFloat {
			return constvalue.NewFloatText(node.Value, typText)
		}
		value, err := numeric.CanonicalizeIntegerLiteral(node.Value)
		if err != nil {
			return nil, false
		}
		return constvalue.NewIntText(value, typText)
	case *ast.BoolLit:
		return constvalue.NewBool(node.Value), true
	case *ast.Ident, *ast.ScopeResolution:
		// Later declarations may shadow the name, but not its resolved binding.
		sym := e.module.SymbolIndex.Symbol(expr)
		if sym == nil || sym.Kind != symbols.SymbolConst {
			return nil, false
		}
		value, ok := e.evalConstSymbol(sym)
		if !ok {
			return nil, false
		}
		return expectedNumericConstValue(value, expected)
	case *ast.AsExpr:
		targetType := checkedType
		var sourceType types.Type
		if e.evidence != nil {
			sourceType = e.evidence.ExprType(node.Expr.ID())
		}
		if targetType == nil {
			targetType = e.ctx.TypeResolver.Query(e.module, node.TypeExpr, typeresolution.Context{}).Type
		}
		if _, _, numericTarget := types.NumericInfo(targetType); !numericTarget {
			return nil, false
		}
		// Cast input keeps its own width; destination context must not change
		// overflow before the explicit finite-width conversion.
		value, ok := e.evalExpr(node.Expr, sourceType)
		if !ok {
			return nil, false
		}
		converted, ok := expectedNumericConstValue(value, targetType)
		if !ok {
			return nil, false
		}
		if types.IsSameType(targetType, expected) {
			return converted, true
		}
		return expectedNumericConstValue(converted, expected)
	case *ast.UnaryExpr:
		value, ok := e.evalExpr(node.Expr, expected)
		if !ok {
			return nil, false
		}
		return constvalue.FoldUnary(node.Op, value)
	case *ast.BinaryExpr:
		operandType := expected
		if checkedType != nil {
			operandType = checkedType
			if binaryResultIsBool(node.Op) {
				// The result is bool; the unconverted operand owns the checked
				// numeric destination for comparisons.
				operandType = e.evidence.ExprType(node.Left.ID())
				if conversion, found := e.evidence.ImplicitConversion(node.Left.ID()); found &&
					conversion.Kind == types.ConversionNumeric && conversion.Compatibility == types.Compatible {
					operandType = e.evidence.ExprType(node.Right.ID())
				}
			}
		}
		left, lok := e.evalExpr(node.Left, operandType)
		right, rok := e.evalExpr(node.Right, operandType)
		if !lok || !rok {
			return nil, false
		}
		if folded, ok := constvalue.FoldBinary(node.Op, left, right); ok {
			return folded, true
		}
		if checkedType != nil || expected != nil {
			return nil, false
		}
		commonType := types.CommonNumericType(&types.NamedType{Name: left.TypeText()}, &types.NamedType{Name: right.TypeText()})
		if commonType == nil {
			return nil, false
		}
		left, lok = e.evalExpr(node.Left, commonType)
		right, rok = e.evalExpr(node.Right, commonType)
		if !lok || !rok {
			return nil, false
		}
		return constvalue.FoldBinary(node.Op, left, right)
	default:
		return nil, false
	}
}

func expectedNumericConstValue(value constvalue.Value, expected types.Type) (constvalue.Value, bool) {
	if expected == nil {
		return value, true
	}
	family, bits, ok := types.NumericInfo(expected)
	if !ok {
		return value, true
	}
	typeText := types.TypeText(types.Underlying(expected))
	switch v := value.(type) {
	case *constvalue.IntConst:
		integer := v.Int()
		if integer == nil {
			return nil, false
		}
		if family == types.NumericFloat {
			// Round the exact integer directly to its destination. Routing f32
			// through f64 can double-round, and text parsing rejects IEEE overflow.
			number := new(big.Float).SetInt(integer)
			if bits == 32 {
				rounded, _ := number.Float32()
				return constvalue.NewFloat(float64(rounded), typeText)
			}
			rounded, _ := number.Float64()
			return constvalue.NewFloat(rounded, typeText)
		}
		return constvalue.NewInt(integer, typeText)
	case *constvalue.FloatConst:
		if v == nil {
			return nil, false
		}
		if family == types.NumericFloat {
			return constvalue.NewFloat(v.Float(), typeText)
		}
		if math.IsNaN(v.Float()) || math.IsInf(v.Float(), 0) {
			return nil, false
		}
		integer, _ := big.NewFloat(v.Float()).Int(nil)
		// LLVM float-to-integer conversion is undefined outside the destination
		// range. Do not turn an unmaterializable cast into a wrapping integer.
		if !numeric.FitsIntegerLiteral(integer.String(), bits, family == types.NumericSigned) {
			return nil, false
		}
		return constvalue.NewInt(integer, typeText)
	default:
		return value, true
	}
}
