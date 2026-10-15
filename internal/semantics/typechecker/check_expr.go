package typechecker

import (
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"compiler/internal/constvalue"
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/thir"
	"compiler/internal/problems"

	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeresolution"
	"compiler/internal/semantics/types"
	"compiler/pkg/numeric"
)

// typeExpr types expr and records the type it reads as. A use of a payload
// name reads the payload (typePayloadName); flow analysis later verifies every
// such read and never re-enters this checker.
func (c *checker) typeExpr(scope *symbols.Scope, expr ast.Expr, expected types.Type) types.Type {
	base := c.typeExprBase(scope, expr, expected)
	if base == nil || expr == nil {
		return base
	}
	if c.module != nil && c.evidence != nil {
		c.evidence.RecordExprType(expr.ID(), base)
	}
	if types.OptionalPayloadDepthForExpected(base, expected) > 0 {
		return c.requirePayload(expr, base)
	}
	return base
}

// typePayloadName types a use of a name that reads a variable's payload in
// place (resolver.declarePayloadName) and marks it as a payload read of that
// variable, which is what later phases see. Where the whole variable is
// expected or tested again (or, for a test inside a test, an outer payload)
// the use reads that; reading the payload is the default.
func (c *checker) typePayloadName(node *ast.Ident, name *symbols.Symbol, expected types.Type) types.Type {
	// The subject of a test or match is the nearest value that has cases.
	isSubject := c.wholeCarrierExpr == ast.Expr(node)
	read := name
	for level := name; level != nil; level = level.AliasOf {
		typ, _ := symbols.GetSymbolType(level)
		_, hasCases := types.VariantDescriptorOf(typ)
		if isSubject && hasCases || !isSubject && expected != nil && types.IsSameType(typ, expected) {
			read = level
			break
		}
	}
	typ, typed := symbols.GetSymbolType(read)
	if !typed || typ == nil {
		return &types.UnknownType{}
	}
	var carrier types.Type
	var path []int
	if read.AliasOf != nil {
		carrier, _ = symbols.GetSymbolType(name.Variable())
		// One level is the usual case and shares the name's own path; no
		// reader changes a path.
		path = read.PayloadPath
		for level := read.AliasOf; level.AliasOf != nil; level = level.AliasOf {
			path = append(slices.Clone(level.PayloadPath), path...)
		}
	}
	c.evidence.MarkCasePayload(node.ID(), carrier, path)
	return typ
}

// typePayloadExpr types an operand that is used through its value: a field or
// index base, a callee, an operator operand.
func (c *checker) typePayloadExpr(scope *symbols.Scope, expr ast.Expr, expected types.Type) types.Type {
	return c.requirePayload(expr, c.typeExpr(scope, expr, expected))
}

// requirePayload rejects an optional used where its payload is needed: nothing
// narrowed it here.
func (c *checker) requirePayload(expr ast.Expr, typ types.Type) types.Type {
	if !types.IsOptional(typ) {
		return typ
	}
	c.ctx.Diagnostics.Add(problems.OptionalPayloadProof(ast.LocOf(expr)))
	return &types.InvalidType{}
}

func (c *checker) typeWholeCarrierExpr(scope *symbols.Scope, expr ast.Expr) types.Type {
	previous := c.wholeCarrierExpr
	c.wholeCarrierExpr = expr
	defer func() { c.wholeCarrierExpr = previous }()
	return c.typeExpr(scope, expr, nil)
}

func (c *checker) recordCaseTest(node ast.Expr, subject testedSubject, caseIndex, caseCount int, caseWhenTrue bool, family types.VariantFamily) {
	if c == nil || c.module == nil || c.evidence == nil || node == nil || subject.place == nil {
		return
	}
	c.evidence.RecordCaseTest(node.ID(), CaseTest{
		Case: caseIndex, MatchesWhenTrue: caseWhenTrue,
		CaseCount: caseCount, Family: family, Subject: subject.place, Borrow: subject.borrow,
	})
}

func (c *checker) typeExprBase(scope *symbols.Scope, expr ast.Expr, expected types.Type) types.Type {
	if expr == nil {
		return nil
	}
	switch node := expr.(type) {
	case *ast.NumberLit:
		return c.typeNumber(node, expected)

	case *ast.StringLit:
		if node.IsCString {
			return &types.CStrType{}
		}
		return &types.StringType{}

	case *ast.ByteLit:
		return &types.ByteType{}

	case *ast.CharLit:
		return &types.CharType{}

	case *ast.BoolLit:
		return &types.BoolType{}

	case *ast.NoneLit:
		if optional, ok := types.Underlying(expected).(*types.OptionalType); ok && optional != nil {
			return expected
		}
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression,
			"`none` requires optional context", ast.LocOf(node), "")
		return &types.InvalidType{}

	case *ast.AddressExpr:
		return c.typeAddressExpr(scope, node, expected)

	case *ast.Ident:
		var sym *symbols.Symbol
		if c.module != nil && c.module.SymbolIndex != nil {
			sym = c.module.SymbolIndex.Symbol(node)
		}
		if sym == nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrUnknownIdentifier,
				fmt.Sprintf("unknown identifier `%s`\n", node.Name), ast.LocOf(node), "")
			return &types.InvalidType{}
		}
		if sym.CompilerOp != "" {
			c.ctx.Diagnostics.Add(invalidExpressionError(node,
				fmt.Sprintf("compiler operation `%s` must be called directly", node.Name)))
			return &types.InvalidType{}
		}
		if sym.AliasOf != nil {
			return c.typePayloadName(node, sym, expected)
		}
		t, ok := symbols.GetSymbolType(sym)
		if !ok || t == nil {
			return &types.UnknownType{}
		}
		return t

	case *ast.ScopeResolution:
		return c.qualifiedScopeType(scope, node)

	case *ast.SelectorExpr:
		return c.typeSelectorExpr(scope, node)

	case *ast.IndexExpr:
		return c.typeIndexExpr(scope, node)

	case *ast.RangeExpr:
		c.ctx.Diagnostics.Add(invalidExpressionError(node,
			"range expression is only valid inside an index expression"))
		return &types.InvalidType{}

	case *ast.StructLit:
		return c.typeStructLit(scope, node, expected)

	case *ast.VariantLit:
		return c.typeVariantConstruction(scope, node, node.Case, node.Payload)

	case *ast.ArrayLit:
		return c.typeArrayLit(scope, node)

	case *ast.UnaryExpr:
		return c.typeUnaryExpr(scope, node, expected)

	case *ast.BinaryExpr:
		return c.typeBinaryExpr(scope, node, expected)

	case *ast.IsExpr:
		return c.typeIsExpr(scope, node)

	case *ast.OrElseExpr:
		return c.typeOrElse(scope, node)

	case *ast.CallExpr:
		return c.typeCallExpr(scope, node)

	case *ast.FreeExpr:
		return c.typeFreeExpr(scope, node)

	case *ast.PrintExpr:
		return c.typePrintExpr(scope, node)

	case *ast.AsExpr:
		return c.typeAsExpr(scope, node)

	case *ast.BadExpr:
		return &types.InvalidType{}

	default:
		panic(fmt.Sprintf("typechecker: unhandled expression %T", expr))
	}
}

func (c *checker) typeUnaryExpr(scope *symbols.Scope, node *ast.UnaryExpr, expected types.Type) types.Type {
	if node.Op != "+" && node.Op != "-" && node.Op != "!" && node.Op != "~" {
		c.ctx.Diagnostics.Add(invalidOperationError(node,
			"unsupported unary operator `"+node.Op+"`"))
		return nil
	}

	argExpected := types.Type(nil)
	if node.Op != "!" && expected != nil &&
		((node.Op == "~" && types.IsIntegral(expected)) || (node.Op != "~" && types.IsArithmetic(expected))) {
		argExpected = expected
		if node.Op == "-" {
			if _, ok := expected.(*types.IntegerType); ok {
				if _, ok := node.Expr.(*ast.NumberLit); ok {
					argExpected = nil
				}
			}
		}
	}

	argType := c.typePayloadExpr(scope, node.Expr, argExpected)
	argType = c.requireValueType(node.Expr, argType, "unary operand")
	if types.IsInvalidOrUnknown(argType) {
		return &types.InvalidType{}
	}
	if node.Op == "!" {
		if !types.IsSameType(argType, &types.BoolType{}) {
			c.ctx.Diagnostics.Add(explicitBoolCastRequiredError(node.Expr, "`!` operand must be bool"))
			return nil
		}
		// `!` of a case test is the same test with the other answer.
		if test, tested := c.evidence.CaseTest(node.Expr.ID()); tested {
			test.MatchesWhenTrue = !test.MatchesWhenTrue
			c.evidence.RecordCaseTest(node.ID(), test)
		}
		return &types.BoolType{}
	}
	if node.Op == "~" {
		if !types.IsIntegral(argType) {
			c.ctx.Diagnostics.Add(invalidOperationError(node,
				"unsupported unary operand type for operator `~`"))
			return nil
		}
		return argType
	}
	if !types.IsArithmetic(argType) {
		c.ctx.Diagnostics.Add(invalidOperationError(node,
			"unsupported unary operand type"))
		return nil
	}
	return argType
}

func (c *checker) typeAddressExpr(scope *symbols.Scope, node *ast.AddressExpr, expected types.Type) types.Type {
	if node == nil || node.Expr == nil {
		return &types.InvalidType{}
	}
	var valueType types.Type
	if node.Mode == ast.AddressRaw {
		valueType = c.typeWholeCarrierExpr(scope, node.Expr)
	} else {
		var valueExpected types.Type
		if target, _, isReference := types.ReferenceValueTarget(types.Underlying(expected)); isReference {
			valueExpected = target
		}
		valueType = c.typeExpr(scope, node.Expr, valueExpected)
	}
	valueType = c.requireValueType(node.Expr, valueType, "address operand")
	if types.IsInvalidOrUnknown(valueType) {
		return &types.InvalidType{}
	}
	isAddressable := place.IsAddressable(scope, node.Expr, c.exprType, c.expandedDefaultBinding)
	if node.Mode == ast.AddressMutable {
		isMutable, sharedReference, mutableBinding := place.MutableAddressable(scope, node.Expr, c.exprType, c.expandedDefaultBinding)
		if isAddressable && !isMutable {
			diagnostic := c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression,
				"mutable reference requires mutable addressable storage", ast.LocOf(node.Expr), "")
			if sharedReference != nil {
				diagnostic.WithSecondaryLabel(ast.LocOf(node.Expr), "value is behind an immutable reference")
			}
			return &types.InvalidType{}
		}
		if _, _, isNested := types.ReferenceTarget(types.Underlying(valueType)); isNested {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidType,
				"reference-to-reference types are not supported in v1", ast.LocOf(node), "")
			return &types.InvalidType{}
		}
		if mutableBinding != nil {
			c.module.SymbolIndex.RequireMutable(mutableBinding)
		}
		return &types.RefType{IsMutable: true, Target: valueType}
	}
	if node.Mode == ast.AddressRaw && !isAddressable {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression,
			"address operator requires addressable storage", ast.LocOf(node.Expr), "")
		return &types.InvalidType{}
	}
	if node.Mode == ast.AddressShared {
		if _, _, isNested := types.ReferenceTarget(types.Underlying(valueType)); isNested {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidType,
				"reference-to-reference types are not supported in v1", ast.LocOf(node), "")
			return &types.InvalidType{}
		}
		return &types.RefType{Target: valueType}
	}
	return &types.RawPtrType{}
}

func (c *checker) typeBinaryExpr(scope *symbols.Scope, node *ast.BinaryExpr, expected types.Type) types.Type {
	optionalTest := (node.Op == "==" || node.Op == "!=") && isNoneExpr(node.Left) != isNoneExpr(node.Right)
	operandExpected := expected
	if binaryResultIsBool(node.Op) {
		operandExpected = nil
	}

	var left, right types.Type
	var tested testedSubject
	if node.Op == "<<" || node.Op == ">>" {
		left = c.typeExpr(scope, node.Left, operandExpected)
		rightExpected := types.Type(nil)
		if rightNumber, ok := node.Right.(*ast.NumberLit); ok && rightNumber.ExplicitType == "" && !numeric.IsFloat(rightNumber.Value) {
			rightExpected = left
		}
		right = c.typeExpr(scope, node.Right, rightExpected)
	} else if isNoneExpr(node.Left) && !isNoneExpr(node.Right) {
		tested = c.caseSubject(scope, node.Right, "right operand")
		right = tested.typ
		left = c.typeExpr(scope, node.Left, optionalOperandExpected(right))
	} else if leftNumber, leftLiteral := node.Left.(*ast.NumberLit); leftLiteral {
		if rightNumber, rightLiteral := node.Right.(*ast.NumberLit); !rightLiteral {
			right = c.typeExpr(scope, node.Right, operandExpected)
			left = c.typeExpr(scope, node.Left, right)
		} else if leftNumber.ExplicitType != "" && rightNumber.ExplicitType == "" {
			left = c.typeExpr(scope, node.Left, operandExpected)
			right = c.typeExpr(scope, node.Right, left)
		} else if leftNumber.ExplicitType == "" && rightNumber.ExplicitType != "" {
			right = c.typeExpr(scope, node.Right, operandExpected)
			left = c.typeExpr(scope, node.Left, right)
		} else {
			left = c.typeExpr(scope, node.Left, operandExpected)
			right = c.typeExpr(scope, node.Right, operandExpected)
		}
	} else if _, rightLiteral := node.Right.(*ast.NumberLit); rightLiteral {
		left = c.typeExpr(scope, node.Left, operandExpected)
		right = c.typeExpr(scope, node.Right, left)
	} else {
		if isNoneExpr(node.Right) {
			tested = c.caseSubject(scope, node.Left, "left operand")
			left = tested.typ
			right = c.typeExpr(scope, node.Right, optionalOperandExpected(left))
		} else {
			left = c.typeExpr(scope, node.Left, operandExpected)
			// The right side runs only where the left side's test held (`&&`)
			// or failed (`||`).
			switch node.Op {
			case "&&":
				c.bindPayloadNames(node.Left, node, nil, nil)
			case "||":
				c.bindPayloadNames(node.Left, nil, node, nil)
			}
			right = c.typeExpr(scope, node.Right, operandExpected)
		}
	}
	left = c.requireValueType(node.Left, left, "left operand")
	right = c.requireValueType(node.Right, right, "right operand")

	if types.IsInvalidOrUnknown(left) || types.IsInvalidOrUnknown(right) {
		return &types.InvalidType{}
	}
	if node.Op == "+" {
		_, leftString := types.Underlying(left).(*types.StringType)
		_, rightString := types.Underlying(right).(*types.StringType)
		leftView, rightView := isStringView(left), isStringView(right)
		if leftString || rightString || leftView || rightView {
			wantRight := &types.RefType{Target: &types.StringType{}}
			if leftString && types.IsSameType(right, wantRight) {
				c.evidence.MarkStringConcatenation(node.ID())
				return &types.StringType{}
			}
			c.ctx.Diagnostics.Add(invalidOperationError(node,
				"string concatenation requires owned `str` on left and borrowed `&str` on right"))
			return &types.InvalidType{}
		}
	}
	if (node.Op == "==" || node.Op == "!=") && types.IsOptional(left) && types.IsOptional(right) &&
		!isNoneExpr(node.Left) && !isNoneExpr(node.Right) {
		c.ctx.Diagnostics.Add(invalidOperationError(node,
			"optional equality currently requires `none` on one side"))
		return &types.InvalidType{}
	}
	if node.Op == "==" || node.Op == "!=" || node.Op == "<" || node.Op == "<=" || node.Op == ">" || node.Op == ">=" {
		leftVariant, leftIsVariant := types.VariantDescriptorOf(left)
		rightVariant, rightIsVariant := types.VariantDescriptorOf(right)
		if leftIsVariant && leftVariant.Family == types.VariantFamilyNamed ||
			rightIsVariant && rightVariant.Family == types.VariantFamilyNamed {
			c.ctx.Diagnostics.Add(invalidOperationError(node,
				"named enum comparison is not supported; use `is` or `match`"))
			return &types.InvalidType{}
		}
	}
	if optionalTest {
		c.recordCaseTest(node, tested, ir.OptionalPresentCase, 2, node.Op == "!=", types.VariantFamilyOptional)
	} else {
		left = c.requirePayload(node.Left, left)
		right = c.requirePayload(node.Right, right)
		if types.IsInvalid(left) || types.IsInvalid(right) {
			return &types.InvalidType{}
		}
	}

	if node.Op == "<<" || node.Op == ">>" {
		if !types.IsIntegral(left) {
			c.ctx.Diagnostics.Add(invalidOperationError(node.Left,
				"shift left operand must be integral"))
			return &types.InvalidType{}
		}
		if !types.IsIntegral(right) {
			c.ctx.Diagnostics.Add(invalidOperationError(node.Right,
				"shift count must be integral"))
			return &types.InvalidType{}
		}
		conversion := types.CheckCompatibility(left, right)
		if conversion.Compatibility != types.Compatible {
			c.ctx.Diagnostics.Add(typeMismatchError(node.Right,
				fmt.Sprintf("shift count type %s requires explicit cast to %s",
					types.TypeText(right), types.TypeText(left))))
			return &types.InvalidType{}
		}
		c.recordImplicitConversion(node.Right, conversion)
		if value, ok := c.evaluateConstant(c.ctx, node.Right, right); ok {
			if count, ok := value.(*constvalue.IntConst); ok && count != nil {
				_, bits, _ := types.NumericInfo(left)
				normalized, normalizedOK := constvalue.NormalizeInteger(count.Int(),
					types.TypeText(types.Underlying(right)))
				if normalizedOK && (normalized.Sign() < 0 || normalized.Cmp(big.NewInt(int64(bits))) >= 0) {
					c.ctx.Diagnostics.Add(invalidOperationError(node.Right,
						fmt.Sprintf("shift count must be between 0 and %d", bits-1)))
					return &types.InvalidType{}
				}
			}
		}
		return left
	}

	commonType := types.CommonNumericType(left, right)
	if commonType != nil {
		c.recordImplicitConversion(node.Left, types.CheckCompatibility(commonType, left))
		c.recordImplicitConversion(node.Right, types.CheckCompatibility(commonType, right))
	}
	if commonType == nil && !c.isAssignable(left, right, node.Right) && !c.isAssignable(right, left, node.Left) {
		c.ctx.Diagnostics.Add(typeMismatchError(node,
			fmt.Sprintf("operand types mismatch: %s vs %s",
				types.TypeText(left), types.TypeText(right))))
		return &types.InvalidType{}
	}

	exprType := left
	if commonType != nil {
		exprType = commonType
	}
	switch node.Op {
	case "&&", "||":
		if !types.IsSameType(left, &types.BoolType{}) || !types.IsSameType(right, &types.BoolType{}) {
			c.ctx.Diagnostics.Add(explicitBoolCastRequiredError(node, "logical operators require bool operands"))
			return nil
		}
		return &types.BoolType{}
	case "==", "!=", "<", "<=", ">", ">=":
		_, leftShape, leftSequence := indexableSequence(left)
		_, rightShape, rightSequence := indexableSequence(right)
		leftSliceView := leftSequence && (leftShape == indexableSharedSliceView || leftShape == indexableMutableSliceView)
		rightSliceView := rightSequence && (rightShape == indexableSharedSliceView || rightShape == indexableMutableSliceView)
		if leftSliceView || rightSliceView {
			c.ctx.Diagnostics.Add(invalidOperationError(node,
				"slice-view comparison is not supported in current compiler stage"))
			return &types.InvalidType{}
		}
		if isStringView(left) || isStringView(right) {
			c.ctx.Diagnostics.Add(invalidOperationError(node,
				"string-view comparison is not supported in current compiler stage"))
			return &types.InvalidType{}
		}
	}

	if !c.areValidBinaryTypes(node.Op, exprType) {
		c.ctx.Diagnostics.Add(invalidOperationError(node,
			"unsupported operand type for operator `"+node.Op+"`"))
		return nil
	}
	if binaryResultIsBool(node.Op) {
		return &types.BoolType{}
	}
	return exprType
}

func (c *checker) typeIsExpr(scope *symbols.Scope, node *ast.IsExpr) types.Type {
	if node == nil || node.Value == nil || node.Case == nil {
		return &types.InvalidType{}
	}
	tested := c.caseSubject(scope, node.Value, "case-test subject")
	valueType := tested.typ
	if types.IsInvalidOrUnknown(valueType) {
		return &types.InvalidType{}
	}

	resolved, ok := c.resolveNamedVariant(node.Case)
	if !ok {
		return &types.InvalidType{}
	}
	if !types.IsSameType(valueType, resolved.EnumType) {
		c.ctx.Diagnostics.Add(typeMismatchError(node.Value,
			fmt.Sprintf("case test requires %s, got %s", types.TypeText(resolved.EnumType), types.TypeText(valueType))))
		return &types.InvalidType{}
	}
	c.recordCaseTest(node, tested, resolved.CaseIndex, len(resolved.Descriptor.Cases), true, types.VariantFamilyNamed)
	return &types.BoolType{}
}

type resolvedNamedVariant struct {
	EnumType   types.Type
	Descriptor types.VariantDescriptor
	Case       types.VariantCase
	CaseName   *ast.Ident
	CaseIndex  int
}

// resolveNamedVariant keeps variant type syntax bound to resolver-owned symbol
// identity. Expanded defaults retain declaration-module symbols even when their
// cloned syntax is typechecked inside a caller module.
func (c *checker) resolveNamedVariant(path *ast.ScopeResolution) (resolvedNamedVariant, bool) {
	if c == nil || c.module == nil || c.module.SymbolIndex == nil || path == nil {
		return resolvedNamedVariant{}, false
	}
	typePath, caseName, ok := path.EnumVariantMember()
	caseSymbol := c.module.SymbolIndex.Symbol(path)
	if !ok || caseName == nil || caseSymbol == nil || caseSymbol.Kind != symbols.SymbolVariant || caseSymbol.Name != caseName.Name {
		return resolvedNamedVariant{}, false
	}
	qualifierSymbol := c.module.SymbolIndex.Symbol(typePath)
	if qualifierSymbol == nil || qualifierSymbol.Kind != symbols.SymbolType {
		return resolvedNamedVariant{}, false
	}
	if _, ok := symbols.GetSymbolType(qualifierSymbol); !ok {
		return resolvedNamedVariant{}, false
	}

	enumType := c.ctx.TypeResolver.Resolve(c.ctx.Diagnostics, c.module, typePath, typeresolution.Context{})
	descriptor, ok := types.VariantDescriptorOf(enumType)
	if !ok || descriptor.Family != types.VariantFamilyNamed {
		return resolvedNamedVariant{}, false
	}
	selected, caseIndex, ok := types.LookupVariantCase(descriptor, caseName.Name)
	if !ok {
		return resolvedNamedVariant{}, false
	}
	return resolvedNamedVariant{
		EnumType: enumType, Descriptor: descriptor, Case: selected,
		CaseName: caseName, CaseIndex: caseIndex,
	}, true
}

func binaryResultIsBool(op string) bool {
	switch op {
	case "&&", "||", "==", "!=", "<", "<=", ">", ">=":
		return true
	default:
		return false
	}
}

func isNoneExpr(expr ast.Expr) bool {
	_, ok := expr.(*ast.NoneLit)
	return ok
}

func optionalOperandExpected(typ types.Type) types.Type {
	if optional, ok := types.Underlying(typ).(*types.OptionalType); ok && optional != nil {
		return typ
	}
	return nil
}

func (c *checker) typeSelectorExpr(scope *symbols.Scope, node *ast.SelectorExpr) types.Type {
	if node == nil || node.Expr == nil || node.Name == nil {
		return &types.InvalidType{}
	}
	baseType := c.typePayloadExpr(scope, node.Expr, nil)
	if baseType == nil || types.IsInvalidOrUnknown(baseType) {
		return &types.InvalidType{}
	}
	if field, fieldIndex, ok := types.LookupStructField(baseType, node.Name.Name); ok {
		var dereferenceType types.Type
		if target, isIndirect := types.PointerTarget(baseType); isIndirect {
			dereferenceType = target
		} else if target, _, isIndirect := types.ReferenceTarget(types.Underlying(baseType)); isIndirect {
			dereferenceType = target
		}
		c.evidence.RecordStructField(node.ID(), StructFieldAccess{
			Field: fieldIndex, Type: field.Type, DereferenceType: dereferenceType,
		})
		return field.Type
	}
	if method, ok := c.lookupCallableMember(baseType, node.Name.Name); ok {
		if method.Symbol != nil && c.module.SymbolIndex != nil {
			c.module.SymbolIndex.Bind(node.Name, method.Symbol)
		}
		return method.Type
	}
	d := diagnostics.NewError(fmt.Sprintf("unknown member `%s`", node.Name.Name)).
		WithCode(diagnostics.ErrFieldNotFound).
		WithPrimaryLabel(ast.LocOf(node.Name), "")
	if match, ok := diagnostics.NearestName(node.Name.Name, append(availableFields(baseType), c.availableMethods(baseType)...)); ok {
		d.Help("did you mean `" + match + "`?")
	}
	c.ctx.Diagnostics.Add(d)
	return &types.InvalidType{}
}

func (c *checker) typeIndexExpr(scope *symbols.Scope, node *ast.IndexExpr) types.Type {
	if node == nil || node.Expr == nil || node.Index == nil {
		return &types.InvalidType{}
	}
	baseType := c.typePayloadExpr(scope, node.Expr, nil)
	if types.IsInvalidOrUnknown(baseType) {
		return &types.InvalidType{}
	}
	if rangeIndex, ok := node.Index.(*ast.RangeExpr); ok {
		return c.typeRangeIndexExpr(scope, node, rangeIndex, baseType)
	}
	indexType := c.typeExpr(scope, node.Index, types.DefaultIntegerType())
	indexType = c.requireValueType(node.Index, indexType, "index")
	if types.IsInvalidOrUnknown(indexType) {
		return &types.InvalidType{}
	}
	if !types.IsIntegral(indexType) {
		c.ctx.Diagnostics.Add(invalidOperationError(node.Index,
			"index expression must be an integer"))
		return &types.InvalidType{}
	}
	if isStringSequence(baseType) {
		c.ctx.Diagnostics.Add(invalidExpressionError(node.Expr,
			"string indexing requires `value |> as_bytes()` or `value |> as_chars()`"))
		return &types.InvalidType{}
	}
	elem, shape, ok := indexableSequence(baseType)
	if !ok {
		c.ctx.Diagnostics.Add(invalidExpressionError(node.Expr,
			"indexing requires array or slice value"))
		return &types.InvalidType{}
	}
	if shape != indexableFixedArray {
		return elem
	}
	array := types.Underlying(baseType).(*types.ArrayType)
	// Literal inference is finished; bounds checking must keep the checked width.
	value, ok := c.evaluateConstant(c.ctx, node.Index, indexType)
	if !ok {
		return elem
	}
	indexConst, ok := value.(*constvalue.IntConst)
	if !ok || indexConst == nil {
		return elem
	}
	c.evidence.RecordConstantIndex(node.ID(), thir.ConstantIndex{
		Text: indexConst.Text(),
		Type: indexType,
	})
	length, lengthErr := numeric.StringToBigInt(array.Len)
	indexValue := indexConst.Int()
	if lengthErr == nil && (indexValue == nil || indexValue.Sign() < 0 || indexValue.Cmp(length) >= 0) {
		c.ctx.Diagnostics.Add(problems.ArrayIndexOutOfBounds(indexConst.Text(), array.Len, ast.LocOf(node.Index)).Isolate())
	}
	return elem
}

func (c *checker) typeRangeIndexExpr(scope *symbols.Scope, node *ast.IndexExpr, rangeIndex *ast.RangeExpr, baseType types.Type) types.Type {
	if c == nil || node == nil || rangeIndex == nil {
		return &types.InvalidType{}
	}
	if isStringSequence(baseType) {
		c.checkRangeBound(scope, rangeIndex.Start, types.DefaultIntegerType())
		c.checkRangeBound(scope, rangeIndex.End, types.DefaultIntegerType())
		return &types.RefType{Target: &types.StringType{}}
	}
	elem, shape, ok := indexableSequence(baseType)
	if !ok {
		c.ctx.Diagnostics.Add(invalidExpressionError(node.Expr,
			"slicing requires array or slice value"))
		return &types.InvalidType{}
	}
	c.checkRangeBound(scope, rangeIndex.Start, types.DefaultIntegerType())
	c.checkRangeBound(scope, rangeIndex.End, types.DefaultIntegerType())
	if shape == indexableFixedArray || shape == indexableDynamicArray {
		if !place.IsAddressable(scope, node.Expr, c.exprType, c.expandedDefaultBinding) {
			c.ctx.Diagnostics.Add(invalidExpressionError(node.Expr,
				"slicing requires addressable array storage"))
			return &types.InvalidType{}
		}
	}
	isMutable := shape == indexableMutableSliceView
	var mutableBinding *symbols.Symbol
	if shape == indexableFixedArray || shape == indexableDynamicArray {
		isMutable, _, mutableBinding = place.MutableAddressable(scope, node.Expr, c.exprType, c.expandedDefaultBinding)
	}
	if mutableBinding != nil {
		c.module.SymbolIndex.RequireMutable(mutableBinding)
	}
	return &types.RefType{
		IsMutable: isMutable,
		Target:    &types.ArrayType{Shape: types.ArraySlice, Elem: elem},
	}
}

func isStringSequence(typ types.Type) bool {
	if _, ok := types.Underlying(typ).(*types.StringType); ok {
		return true
	}
	return isStringView(typ)
}

func isStringView(typ types.Type) bool {
	target, _, ok := types.ReferenceTarget(types.Underlying(typ))
	if !ok {
		return false
	}
	_, ok = types.Underlying(target).(*types.StringType)
	return ok
}

type indexableSequenceShape uint8

const (
	indexableFixedArray indexableSequenceShape = iota
	indexableDynamicArray
	indexableSharedSliceView
	indexableMutableSliceView
)

func indexableSequence(t types.Type) (types.Type, indexableSequenceShape, bool) {
	switch base := types.Underlying(t).(type) {
	case *types.ArrayType:
		if base == nil || base.Elem == nil {
			return nil, 0, false
		}
		if base.Shape == types.ArrayOwner {
			return base.Elem, indexableDynamicArray, true
		}
		if base.Len != "" {
			return base.Elem, indexableFixedArray, true
		}
	case *types.RefType:
		if base == nil || base.Target == nil {
			return nil, 0, false
		}
		target, ok := types.Underlying(base.Target).(*types.ArrayType)
		if !ok || target == nil || (target.Shape != types.ArrayOwner && target.Shape != types.ArraySlice) || target.Elem == nil {
			return nil, 0, false
		}
		if base.IsMutable {
			return target.Elem, indexableMutableSliceView, true
		}
		return target.Elem, indexableSharedSliceView, true
	}
	return nil, 0, false
}

func (c *checker) checkRangeBound(scope *symbols.Scope, expr ast.Expr, expected types.Type) types.Type {
	if c == nil || expr == nil {
		return nil
	}
	boundType := c.typeExpr(scope, expr, expected)
	boundType = c.requireValueType(expr, boundType, "range bound")
	if types.IsInvalidOrUnknown(boundType) {
		return boundType
	}
	if !types.IsIntegral(boundType) {
		c.ctx.Diagnostics.Add(invalidOperationError(expr,
			"range bound must be an integer").Isolate())
	}
	return boundType
}

func (c *checker) typeStructLit(scope *symbols.Scope, node *ast.StructLit, expected types.Type) types.Type {
	if node == nil {
		return &types.InvalidType{}
	}
	if node.Type != nil {
		targetType := c.ctx.TypeResolver.Resolve(c.ctx.Diagnostics, c.module, node.Type, typeresolution.Context{})
		targetStruct, ok := types.Underlying(targetType).(*types.StructType)
		if !ok || targetStruct == nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidType,
				"composite literal type must be struct", ast.LocOf(node.Type), "")
			return &types.InvalidType{}
		}
		ordered, valid := c.typeLiteralFields(scope, node, node.Fields, targetStruct, "struct literal")
		if valid {
			c.evidence.RecordStructLiteralFields(node.ID(), ordered)
		}
		return targetType
	}
	targetStruct, targetType := c.expectedStructType(expected)
	if targetStruct != nil {
		ordered, valid := c.typeLiteralFields(scope, node, node.Fields, targetStruct, "struct literal")
		if valid {
			c.evidence.RecordStructLiteralFields(node.ID(), ordered)
		}
		return targetType
	}
	return c.typeStructLitAnonymous(scope, node)
}

func (c *checker) expectedStructType(expected types.Type) (*types.StructType, types.Type) {
	if expected == nil {
		return nil, nil
	}
	if strct, ok := types.Underlying(expected).(*types.StructType); ok && strct != nil {
		return strct, expected
	}
	return nil, nil
}

func (c *checker) typeLiteralFields(scope *symbols.Scope, site ast.Node, fields []ast.StructLitField, targetStruct *types.StructType, literal string) ([]ast.Expr, bool) {
	if targetStruct == nil {
		return nil, false
	}
	valid := true
	fieldsByName := make(map[string]ast.StructLitField, len(fields))
	for _, field := range fields {
		if field.Name == nil || field.Name.Name == "" {
			continue
		}
		if _, exists := fieldsByName[field.Name.Name]; exists {
			valid = false
			c.ctx.Diagnostics.AddError(diagnostics.ErrDuplicateField,
				"duplicate "+literal+" field `"+field.Name.Name+"`", ast.LocOf(field.Name), "")
			continue
		}
		fieldsByName[field.Name.Name] = field
	}
	ordered := make([]ast.Expr, len(targetStruct.Fields))
	required := availableFields(targetStruct)
	for index, targetField := range targetStruct.Fields {
		field, ok := fieldsByName[targetField.Name]
		if !ok {
			valid = false
			c.ctx.Diagnostics.AddError(diagnostics.ErrMissingInitializer,
				"missing "+literal+" field `"+targetField.Name+"`", ast.LocOf(site), "").
				Help(fmt.Sprintf("required fields: %s", strings.Join(required, ", ")))
			continue
		}
		ordered[index] = field.Value
		delete(fieldsByName, targetField.Name)
		valueType := c.typeExpr(scope, field.Value, targetField.Type)
		valueType = c.requireValueType(field.Value, valueType, literal+" field initializer")
		if types.IsInvalidOrUnknown(valueType) {
			valid = false
			continue
		}
		if !c.isAssignable(targetField.Type, valueType, field.Value) {
			valid = false
			c.ctx.Diagnostics.AddError(diagnostics.ErrTypeMismatch,
				fmt.Sprintf("cannot assign %s to field `%s` of type %s",
					types.TypeText(valueType), targetField.Name, types.TypeText(targetField.Type)), ast.LocOf(field.Value), "")
		}
	}
	for name, field := range fieldsByName {
		valid = false
		c.ctx.Diagnostics.AddError(diagnostics.ErrFieldNotFound,
			"unknown "+literal+" field `"+name+"`", ast.LocOf(field.Name), "")
	}
	return ordered, valid
}

func (c *checker) typeVariantConstruction(scope *symbols.Scope, site ast.Expr, path *ast.ScopeResolution, value ast.Expr) types.Type {
	resolved, ok := c.resolveNamedVariant(path)
	if !ok {
		return &types.InvalidType{}
	}
	_, initialized := site.(*ast.VariantLit)
	if resolved.Case.Payload == nil {
		if initialized {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression,
				"payloadless enum variant `"+resolved.CaseName.Name+"` does not accept a payload", ast.LocOf(site), "remove `with` and its value")
			return &types.InvalidType{}
		}
		c.evidence.RecordVariantConstruction(site.ID(), VariantConstruction{EnumType: resolved.EnumType, Case: resolved.CaseIndex})
		return resolved.EnumType
	}
	if !initialized {
		c.ctx.Diagnostics.AddError(diagnostics.ErrMissingInitializer,
			"data enum variant `"+resolved.CaseName.Name+"` requires a payload", ast.LocOf(site), "add `with` and its payload value")
		return &types.InvalidType{}
	}
	if value == nil {
		c.ctx.Diagnostics.AddError(diagnostics.ErrMissingInitializer,
			"enum variant `"+resolved.CaseName.Name+"` requires a payload value", ast.LocOf(site), "write `with <expression>`")
		return &types.InvalidType{}
	}
	valueType := c.typeExpr(scope, value, resolved.Case.Payload)
	valueType = c.requireValueType(value, valueType, "enum variant payload")
	if types.IsInvalidOrUnknown(valueType) {
		return &types.InvalidType{}
	}
	if !c.isAssignable(resolved.Case.Payload, valueType, value) {
		c.ctx.Diagnostics.AddError(diagnostics.ErrTypeMismatch,
			fmt.Sprintf("cannot assign %s to enum variant payload of type %s", types.TypeText(valueType), types.TypeText(resolved.Case.Payload)), ast.LocOf(value), "")
		return &types.InvalidType{}
	}
	c.evidence.RecordVariantConstruction(site.ID(), VariantConstruction{
		EnumType: resolved.EnumType,
		Case:     resolved.CaseIndex,
		Payload:  resolved.Case.Payload,
		Value:    value,
	})
	return resolved.EnumType
}

func (c *checker) typeStructLitAnonymous(scope *symbols.Scope, node *ast.StructLit) types.Type {
	fields := make([]types.Field, 0, len(node.Fields))
	seen := make(map[string]struct{}, len(node.Fields))
	for _, field := range node.Fields {
		if field.Name == nil || field.Name.Name == "" {
			continue
		}
		if _, exists := seen[field.Name.Name]; exists {
			c.ctx.Diagnostics.AddError(diagnostics.ErrDuplicateField,
				"duplicate struct literal field `"+field.Name.Name+"`", ast.LocOf(field.Name), "")
			continue
		}
		seen[field.Name.Name] = struct{}{}
		valueType := c.typeExpr(scope, field.Value, nil)
		valueType = c.requireValueType(field.Value, valueType, "struct field initializer")
		if types.IsInvalidOrUnknown(valueType) {
			valueType = &types.InvalidType{}
		}
		fields = append(fields, types.Field{Name: field.Name.Name, Type: valueType})
	}
	ordered := make([]ast.Expr, 0, len(node.Fields))
	for _, field := range node.Fields {
		if field.Name != nil && field.Name.Name != "" && field.Value != nil {
			ordered = append(ordered, field.Value)
		}
	}
	c.evidence.RecordStructLiteralFields(node.ID(), ordered)
	return &types.StructType{Fields: fields}
}

func (c *checker) typeArrayLit(scope *symbols.Scope, node *ast.ArrayLit) types.Type {
	if node == nil {
		return &types.InvalidType{}
	}
	arrayType := c.ctx.TypeResolver.Resolve(c.ctx.Diagnostics, c.module, node.Type, typeresolution.Context{})
	if types.IsInvalidOrUnknown(arrayType) || types.ContainsInvalid(arrayType) {
		return &types.InvalidType{}
	}
	array, ok := types.Underlying(arrayType).(*types.ArrayType)
	if !ok || array == nil || array.Elem == nil || types.IsInvalidOrUnknown(array.Elem) {
		c.ctx.Diagnostics.Add(invalidTypeError(node.Type, "invalid array literal type"))
		return &types.InvalidType{}
	}
	if array.Shape == types.ArrayOwner {
		if c.rejectReferenceStorage(array.Elem, node.Type, "dynamic arrays", true) ||
			c.rejectUnsizedType(array.Elem, node.Type, "dynamic array element") {
			return &types.InvalidType{}
		}
		if !types.IsLowerableType(array.Elem) {
			c.ctx.Diagnostics.Add(invalidTypeError(node.Type,
				"dynamic array element type is not lowerable in current compiler stage"))
			return &types.InvalidType{}
		}
	}
	if !node.HasInferredLength {
		if nodeLen, err := strconv.Atoi(array.Len); err == nil && nodeLen != len(node.Values) {
			c.ctx.Diagnostics.AddError(diagnostics.ErrTypeMismatch,
				fmt.Sprintf("array literal has %d values but length is %d", len(node.Values), nodeLen), ast.LocOf(node), "").Isolate()
		}
	}
	for _, value := range node.Values {
		valueType := c.typeExpr(scope, value, array.Elem)
		valueType = c.requireValueType(value, valueType, "array element initializer")
		if types.IsInvalidOrUnknown(valueType) {
			continue
		}
		if !c.isAssignable(array.Elem, valueType, value) {
			c.ctx.Diagnostics.Add(typeMismatchError(value,
				fmt.Sprintf("cannot assign %s to array element of type %s",
					types.TypeText(valueType), types.TypeText(array.Elem))).Isolate())
		}
	}
	return arrayType
}

func (c *checker) typeAsExpr(scope *symbols.Scope, node *ast.AsExpr) types.Type {
	if c == nil || node == nil {
		return nil
	}
	targetType := c.ctx.TypeResolver.Resolve(c.ctx.Diagnostics, c.module, node.TypeExpr, typeresolution.Context{})
	if targetType == nil || types.IsInvalidOrUnknown(targetType) {
		c.ctx.Diagnostics.Add(invalidTypeError(node.TypeExpr, "invalid target type for cast"))
		return &types.InvalidType{}
	}
	if c.rejectUnsizedType(targetType, node.TypeExpr, "cast target") {
		return &types.InvalidType{}
	}
	if node.Expr == nil {
		return &types.InvalidType{}
	}
	exprType := c.typePayloadExpr(scope, node.Expr, nil)
	exprType = c.requireValueType(node.Expr, exprType, "cast")
	if types.IsInvalidOrUnknown(exprType) {
		return &types.InvalidType{}
	}
	conversion := types.CheckCompatibility(targetType, exprType)
	if conversion.Compatibility != types.Incompatible &&
		(conversion.Kind == types.ConversionIdentity || conversion.Kind == types.ConversionBool ||
			conversion.Kind == types.ConversionNumeric) {
		return targetType
	}
	c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCast,
		fmt.Sprintf("cannot cast %s to %s",
			types.TypeText(exprType), types.TypeText(targetType)), ast.LocOf(node), "")
	return &types.InvalidType{}
}

func (c *checker) typeNumber(node *ast.NumberLit, expected types.Type) types.Type {
	if node == nil {
		return nil
	}
	if types.IsInvalidOrUnknown(expected) {
		expected = nil
	}
	if node.ExplicitType != "" {
		explicit, ok := types.NumericTypeFromName(node.ExplicitType, c.ctx.Target)
		if !ok {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidNumber,
				fmt.Sprintf("unsupported numeric literal suffix `%s`", node.ExplicitType), ast.LocOf(node), "").
				Help("integer suffix widths must be between 1 and 8388608; float suffixes are limited to f32 and f64")
			return &types.InvalidType{}
		}
		if !types.LiteralFitsType(node.Value, explicit) {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidNumber,
				fmt.Sprintf("literal `%s%s` does not fit %s", node.Value, node.ExplicitType, node.ExplicitType), ast.LocOf(node), "")
			return &types.InvalidType{}
		}
		return explicit
	}
	if expected != nil {
		numberTarget := expected
		if optional, ok := types.Underlying(expected).(*types.OptionalType); ok && optional != nil && optional.Inner != nil {
			numberTarget = optional.Inner
		}
		naturalType := types.DefaultNumberType(node.Value)
		conversion := types.CheckCompatibility(numberTarget, naturalType)
		if (conversion.Kind != types.ConversionIdentity && conversion.Kind != types.ConversionNumeric) ||
			conversion.Compatibility == types.Incompatible {
			c.ctx.Diagnostics.Add(typeMismatchError(node,
				fmt.Sprintf("literal `%s` cannot be used as %s", node.Value, types.TypeText(expected))))
			return nil
		}
		if !types.LiteralFitsType(node.Value, numberTarget) {
			d := diagnostics.NewError(fmt.Sprintf("literal `%s` does not fit %s", node.Value, types.TypeText(numberTarget))).
				WithCode(diagnostics.ErrInvalidNumber).
				WithPrimaryLabel(ast.LocOf(node), "")
			if intType, ok := numberTarget.(*types.IntegerType); ok {
				d.Help(integerRangeHint(intType))
			}
			c.ctx.Diagnostics.Add(d)
			return nil
		}
		return numberTarget
	}
	return types.DefaultNumberType(node.Value)
}

func integerRangeHint(t *types.IntegerType) string {
	if t.IsSigned {
		bits := t.Bits - 1
		return fmt.Sprintf("%s range: -2^%d to 2^%d-1", types.TypeText(t), bits, bits)
	}
	return fmt.Sprintf("%s range: 0 to 2^%d-1", types.TypeText(t), t.Bits)
}

func (c *checker) areValidBinaryTypes(op string, typ types.Type) bool {
	switch op {
	case "+", "-", "*", "/", "%":
		return types.IsArithmetic(typ)
	case "&", "|", "^", "<<", ">>":
		return types.IsIntegral(typ)
	case "==", "!=":
		return types.IsEquatable(typ)
	case "<", "<=", ">", ">=":
		return types.IsOrderable(typ)
	case "&&", "||":
		return types.IsCondition(typ)
	default:
		return false
	}
}
