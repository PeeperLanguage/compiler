package typechecker

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"compiler/internal/constvalue"
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/problems"
	"compiler/internal/project"
	"compiler/internal/semantics/consteval"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/pkg/numeric"
)

// typeExpr computes the type of an expression using scope lookup, records it in the
// module's ExprTypes side table for downstream phases, and returns it.
func (c *checker) typeExpr(scope *symbols.Scope, expr ast.Expr, expected typeinfo.Type) (resolved typeinfo.Type) {
	if expr == nil {
		return nil
	}
	defer func() {
		if resolved != nil {
			if c.module != nil && c.module.Semantics != nil {
				c.module.Semantics.ExprTypes[expr.ID()] = resolved
			}
		}
	}()
	switch node := expr.(type) {
	case *ast.NumberLit:
		return c.typeNumber(node, expected)

	case *ast.StringLit:
		if node.CString {
			return &typeinfo.CStrType{}
		}
		return &typeinfo.StringType{}

	case *ast.ByteLit:
		return &typeinfo.ByteType{}

	case *ast.CharLit:
		return &typeinfo.CharType{}

	case *ast.BoolLit:
		return &typeinfo.BoolType{}

	case *ast.NoneLit:
		if optional, ok := typeinfo.Underlying(expected).(*typeinfo.OptionalType); ok && optional != nil {
			return expected
		}
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression,
			"`none` requires optional context", ast.LocOf(node), "")
		return &typeinfo.InvalidType{}

	case *ast.AddressExpr:
		return c.typeAddressExpr(scope, node, expected)

	case *ast.Ident:
		var sym *symbols.Symbol
		var ok bool
		if c.module != nil && c.module.Semantics != nil {
			sym = c.module.Semantics.ResolvedSymbols[node.ID()]
			ok = sym != nil
		}
		if !ok {
			sym, ok = scope.Lookup(node.Name)
		}
		if !ok || sym == nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrUnknownIdentifier,
				fmt.Sprintf("unknown identifier `%s`\n", node.Name), ast.LocOf(node), "")
			return &typeinfo.InvalidType{}
		}
		if sym.CompilerOp != "" {
			c.ctx.Diagnostics.Add(invalidExpressionError(node,
				fmt.Sprintf("compiler operation `%s` must be called directly", node.Name)))
			return &typeinfo.InvalidType{}
		}
		t, ok := symbols.GetSymbolType(sym)
		if !ok || t == nil {
			return &typeinfo.UnknownType{}
		}
		return t

	case *ast.ScopeResolution:
		return c.qualifiedScopeType(node)

	case *ast.SelectorExpr:
		return c.typeSelectorExpr(scope, node)

	case *ast.IndexExpr:
		return c.typeIndexExpr(scope, node)

	case *ast.RangeExpr:
		c.ctx.Diagnostics.Add(invalidExpressionError(node,
			"range expression is only valid inside an index expression"))
		return &typeinfo.InvalidType{}

	case *ast.StructLit:
		return c.typeStructLit(scope, node, expected)

	case *ast.ArrayLit:
		return c.typeArrayLit(scope, node)

	case *ast.UnaryExpr:
		return c.typeUnaryExpr(scope, node, expected)

	case *ast.BinaryExpr:
		return c.typeBinaryExpr(scope, node, expected)

	case *ast.CallExpr:
		return c.typeCallExpr(scope, node, expected)

	case *ast.FreeExpr:
		return c.typeFreeExpr(scope, node)

	case *ast.PrintExpr:
		return c.typePrintExpr(scope, node)

	case *ast.AsExpr:
		return c.typeAsExpr(scope, node)

	case *ast.BadExpr:
		return nil // resolver already diagnosed unsupported expressions

	default:
		panic(fmt.Sprintf("typechecker: unhandled expression %T", expr))
	}
}

func (c *checker) typeUnaryExpr(scope *symbols.Scope, node *ast.UnaryExpr, expected typeinfo.Type) typeinfo.Type {
	if node.Op != "+" && node.Op != "-" && node.Op != "!" && node.Op != "~" {
		c.ctx.Diagnostics.Add(invalidOperationError(node,
			"unsupported unary operator `"+node.Op+"`"))
		return nil
	}

	argExpected := typeinfo.Type(nil)
	if node.Op != "!" && expected != nil &&
		((node.Op == "~" && typeinfo.IsIntegral(expected)) || (node.Op != "~" && typeinfo.IsArithmetic(expected))) {
		argExpected = expected
		if node.Op == "-" {
			if _, ok := expected.(*typeinfo.IntegerType); ok {
				if _, ok := node.Expr.(*ast.NumberLit); ok {
					argExpected = nil
				}
			}
		}
	}

	argType := c.typeExpr(scope, node.Expr, argExpected)
	argType = c.requireValueType(node.Expr, argType, "unary operand")
	if typeinfo.IsInvalidOrUnknown(argType) {
		return &typeinfo.InvalidType{}
	}
	if node.Op == "!" {
		if !typeinfo.SameType(argType, &typeinfo.BoolType{}) {
			c.ctx.Diagnostics.Add(explicitBoolCastRequiredError(node.Expr, "`!` operand must be bool"))
			return nil
		}
		return &typeinfo.BoolType{}
	}
	if node.Op == "~" {
		if !typeinfo.IsIntegral(argType) {
			c.ctx.Diagnostics.Add(invalidOperationError(node,
				"unsupported unary operand type for operator `~`"))
			return nil
		}
		return argType
	}
	if !typeinfo.IsArithmetic(argType) {
		c.ctx.Diagnostics.Add(invalidOperationError(node,
			"unsupported unary operand type"))
		return nil
	}
	return argType
}

func (c *checker) typeAddressExpr(scope *symbols.Scope, node *ast.AddressExpr, expected typeinfo.Type) typeinfo.Type {
	if node == nil || node.Expr == nil {
		return &typeinfo.InvalidType{}
	}
	valueType := c.typeExpr(scope, node.Expr, nil)
	valueType = c.requireValueType(node.Expr, valueType, "address operand")
	if typeinfo.IsInvalidOrUnknown(valueType) {
		return &typeinfo.InvalidType{}
	}
	exprType := func(expr ast.Expr) typeinfo.Type {
		return c.typeExpr(scope, expr, nil)
	}
	addressable := place.Addressable(scope, node.Expr, exprType, c.expandedDefaultBinding)
	if node.Mode == ast.AddressMutable {
		if mutable, sharedReference := place.MutableAddressable(scope, node.Expr, exprType, c.expandedDefaultBinding); addressable && !mutable {
			diagnostic := c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression,
				"mutable reference requires mutable addressable storage", ast.LocOf(node.Expr), "")
			if sharedReference != nil {
				diagnostic.WithSecondaryLabel(ast.LocOf(node.Expr), "value is behind an immutable reference")
			}
			return &typeinfo.InvalidType{}
		}
		if _, _, nested := typeinfo.ReferenceTarget(typeinfo.Underlying(valueType)); nested {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidType,
				"reference-to-reference types are not supported in v1", ast.LocOf(node), "")
			return &typeinfo.InvalidType{}
		}
		return &typeinfo.RefType{Mutable: true, Target: valueType}
	}
	if node.Mode == ast.AddressRaw && !addressable {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression,
			"address operator requires addressable storage", ast.LocOf(node.Expr), "")
		return &typeinfo.InvalidType{}
	}
	if node.Mode == ast.AddressShared {
		if _, _, nested := typeinfo.ReferenceTarget(typeinfo.Underlying(valueType)); nested {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidType,
				"reference-to-reference types are not supported in v1", ast.LocOf(node), "")
			return &typeinfo.InvalidType{}
		}
		return &typeinfo.RefType{Target: valueType}
	}
	return &typeinfo.RawPtrType{}
}

func (c *checker) typeBinaryExpr(scope *symbols.Scope, node *ast.BinaryExpr, expected typeinfo.Type) typeinfo.Type {
	operandExpected := expected
	if binaryResultIsBool(node.Op) {
		operandExpected = nil
	}

	var left, right typeinfo.Type
	if node.Op == "<<" || node.Op == ">>" {
		left = c.typeExpr(scope, node.Left, operandExpected)
		rightExpected := typeinfo.Type(nil)
		if rightNumber, ok := node.Right.(*ast.NumberLit); ok && rightNumber.ExplicitType == "" && !numeric.IsFloat(rightNumber.Value) {
			rightExpected = left
		}
		right = c.typeExpr(scope, node.Right, rightExpected)
	} else if isNoneExpr(node.Left) && !isNoneExpr(node.Right) {
		right = c.typeExpr(scope, node.Right, operandExpected)
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
		left = c.typeExpr(scope, node.Left, operandExpected)
		rightExpected := operandExpected
		if isNoneExpr(node.Right) {
			rightExpected = optionalOperandExpected(left)
		}
		right = c.typeExpr(scope, node.Right, rightExpected)
	}
	left = c.requireValueType(node.Left, left, "left operand")
	right = c.requireValueType(node.Right, right, "right operand")

	if typeinfo.IsInvalidOrUnknown(left) || typeinfo.IsInvalidOrUnknown(right) {
		return &typeinfo.InvalidType{}
	}
	if (node.Op == "==" || node.Op == "!=") && (isOptionalType(left) || isOptionalType(right)) &&
		!isNoneExpr(node.Left) && !isNoneExpr(node.Right) {
		c.ctx.Diagnostics.Add(invalidOperationError(node,
			"optional equality currently requires `none` on one side"))
		return &typeinfo.InvalidType{}
	}

	if node.Op == "<<" || node.Op == ">>" {
		if !typeinfo.IsIntegral(left) {
			c.ctx.Diagnostics.Add(invalidOperationError(node.Left,
				"shift left operand must be integral"))
			return &typeinfo.InvalidType{}
		}
		if !typeinfo.IsIntegral(right) {
			c.ctx.Diagnostics.Add(invalidOperationError(node.Right,
				"shift count must be integral"))
			return &typeinfo.InvalidType{}
		}
		if value, ok := consteval.EvaluateExpr(c.ctx, c.module, scope, node.Right, right); ok {
			if count, ok := value.(*constvalue.IntConst); ok && count != nil {
				_, bits, _ := typeinfo.NumericInfo(left)
				normalized, normalizedOK := constvalue.NormalizeInteger(count.Int(),
					typeinfo.TypeText(typeinfo.Underlying(right)))
				if normalizedOK && (normalized.Sign() < 0 || normalized.Cmp(big.NewInt(int64(bits))) >= 0) {
					c.ctx.Diagnostics.Add(invalidOperationError(node.Right,
						fmt.Sprintf("shift count must be between 0 and %d", bits-1)))
					return &typeinfo.InvalidType{}
				}
			}
		}
		return left
	}

	commonType := typeinfo.CommonNumericType(left, right)
	if commonType == nil && !c.assignable(left, right, node.Right) && !c.assignable(right, left, node.Left) {
		c.ctx.Diagnostics.Add(typeMismatchError(node,
			fmt.Sprintf("operand types mismatch: %s vs %s",
				typeinfo.TypeText(left), typeinfo.TypeText(right))))
		return &typeinfo.InvalidType{}
	}

	exprType := left
	if commonType != nil {
		exprType = commonType
	}
	switch node.Op {
	case "&&", "||":
		if !typeinfo.SameType(left, &typeinfo.BoolType{}) || !typeinfo.SameType(right, &typeinfo.BoolType{}) {
			c.ctx.Diagnostics.Add(explicitBoolCastRequiredError(node, "logical operators require bool operands"))
			return nil
		}
		return &typeinfo.BoolType{}
	case "==", "!=", "<", "<=", ">", ">=":
		_, leftShape, leftSequence := indexableSequence(left)
		_, rightShape, rightSequence := indexableSequence(right)
		leftSliceView := leftSequence && (leftShape == indexableSharedSliceView || leftShape == indexableMutableSliceView)
		rightSliceView := rightSequence && (rightShape == indexableSharedSliceView || rightShape == indexableMutableSliceView)
		if leftSliceView || rightSliceView {
			c.ctx.Diagnostics.Add(invalidOperationError(node,
				"slice-view comparison is not supported in current compiler stage"))
			return &typeinfo.InvalidType{}
		}
		if isStringView(left) || isStringView(right) {
			c.ctx.Diagnostics.Add(invalidOperationError(node,
				"string-view comparison is not supported in current compiler stage"))
			return &typeinfo.InvalidType{}
		}
		return &typeinfo.BoolType{}
	}

	if !c.validBinaryTypes(node.Op, exprType) {
		c.ctx.Diagnostics.Add(invalidOperationError(node,
			"unsupported operand type for operator `"+node.Op+"`"))
		return nil
	}
	return exprType
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

func optionalOperandExpected(typ typeinfo.Type) typeinfo.Type {
	if optional, ok := typeinfo.Underlying(typ).(*typeinfo.OptionalType); ok && optional != nil {
		return typ
	}
	return nil
}

func isOptionalType(typ typeinfo.Type) bool {
	_, ok := typeinfo.Underlying(typ).(*typeinfo.OptionalType)
	return ok
}

func (c *checker) typeSelectorExpr(scope *symbols.Scope, node *ast.SelectorExpr) typeinfo.Type {
	if node == nil || node.Expr == nil || node.Name == nil {
		return &typeinfo.InvalidType{}
	}
	baseType := c.typeExpr(scope, node.Expr, nil)
	if baseType == nil || typeinfo.IsInvalidOrUnknown(baseType) {
		return &typeinfo.InvalidType{}
	}
	if field, _, ok := typeinfo.LookupStructField(baseType, node.Name.Name); ok {
		return field.Type
	}
	if method, ok := c.lookupCallableMember(baseType, node.Name.Name); ok {
		if method.Symbol != nil {
			c.module.Semantics.ResolvedSymbols[node.Name.ID()] = method.Symbol
		}
		return method.Type
	}
	d := diagnostics.NewError(fmt.Sprintf("unknown member `%s`", node.Name.Name)).
		WithCode(diagnostics.ErrFieldNotFound).
		WithPrimaryLabel(ast.LocOf(node.Name), "")
	if match, ok := diagnostics.NearestName(node.Name.Name, append(availableFields(baseType), c.availableMethods(baseType)...)); ok {
		d.WithHelp("did you mean `" + match + "`?")
	}
	c.ctx.Diagnostics.Add(d)
	return &typeinfo.InvalidType{}
}

func (c *checker) typeIndexExpr(scope *symbols.Scope, node *ast.IndexExpr) typeinfo.Type {
	if node == nil || node.Expr == nil || node.Index == nil {
		return &typeinfo.InvalidType{}
	}
	baseType := c.typeExpr(scope, node.Expr, nil)
	if typeinfo.IsInvalidOrUnknown(baseType) {
		return &typeinfo.InvalidType{}
	}
	if rangeIndex, ok := node.Index.(*ast.RangeExpr); ok {
		return c.typeRangeIndexExpr(scope, node, rangeIndex, baseType)
	}
	indexType := c.typeExpr(scope, node.Index, typeinfo.DefaultIntegerType())
	indexType = c.requireValueType(node.Index, indexType, "index")
	if typeinfo.IsInvalidOrUnknown(indexType) {
		return &typeinfo.InvalidType{}
	}
	if !typeinfo.IsIntegral(indexType) {
		c.ctx.Diagnostics.Add(invalidOperationError(node.Index,
			"index expression must be an integer"))
		return &typeinfo.InvalidType{}
	}
	if isStringSequence(baseType) {
		c.ctx.Diagnostics.Add(invalidExpressionError(node.Expr,
			"string indexing requires `value |> as_bytes()` or `value |> as_chars()`"))
		return &typeinfo.InvalidType{}
	}
	elem, shape, ok := indexableSequence(baseType)
	if !ok {
		c.ctx.Diagnostics.Add(invalidExpressionError(node.Expr,
			"indexing requires array or slice value"))
		return &typeinfo.InvalidType{}
	}
	if shape != indexableFixedArray {
		return elem
	}
	array := typeinfo.Underlying(baseType).(*typeinfo.ArrayType)
	value, ok := consteval.EvaluateExpr(c.ctx, c.module, scope, node.Index, typeinfo.DefaultIntegerType())
	if !ok {
		return elem
	}
	indexConst, ok := value.(*constvalue.IntConst)
	if !ok || indexConst == nil {
		return elem
	}
	length, lengthErr := strconv.Atoi(array.Len)
	indexText := indexConst.Text()
	indexValue, indexErr := strconv.Atoi(indexText)
	if lengthErr == nil && (indexErr != nil || indexValue < 0 || indexValue >= length) {
		c.ctx.Diagnostics.Add(problems.ArrayIndexOutOfBounds(indexText, array.Len, ast.LocOf(node.Index)))
	}
	return elem
}

func (c *checker) typeRangeIndexExpr(scope *symbols.Scope, node *ast.IndexExpr, rangeIndex *ast.RangeExpr, baseType typeinfo.Type) typeinfo.Type {
	if c == nil || node == nil || rangeIndex == nil {
		return &typeinfo.InvalidType{}
	}
	if isStringSequence(baseType) {
		c.checkRangeBound(scope, rangeIndex.Start)
		c.checkRangeBound(scope, rangeIndex.End)
		return &typeinfo.RefType{Target: &typeinfo.StringType{}}
	}
	elem, shape, ok := indexableSequence(baseType)
	if !ok {
		c.ctx.Diagnostics.Add(invalidExpressionError(node.Expr,
			"slicing requires array or slice value"))
		return &typeinfo.InvalidType{}
	}
	c.checkRangeBound(scope, rangeIndex.Start)
	c.checkRangeBound(scope, rangeIndex.End)
	exprType := func(expr ast.Expr) typeinfo.Type {
		return c.typeExpr(scope, expr, nil)
	}
	if shape == indexableFixedArray || shape == indexableDynamicArray {
		if !place.Addressable(scope, node.Expr, exprType, c.expandedDefaultBinding) {
			c.ctx.Diagnostics.Add(invalidExpressionError(node.Expr,
				"slicing requires addressable array storage"))
			return &typeinfo.InvalidType{}
		}
	}
	mutable := shape == indexableMutableSliceView
	if shape == indexableFixedArray || shape == indexableDynamicArray {
		mutable, _ = place.MutableAddressable(scope, node.Expr, exprType, c.expandedDefaultBinding)
	}
	return &typeinfo.RefType{
		Mutable: mutable,
		Target:  &typeinfo.ArrayType{Shape: typeinfo.ArraySlice, Elem: elem},
	}
}

func isStringSequence(typ typeinfo.Type) bool {
	if _, ok := typeinfo.Underlying(typ).(*typeinfo.StringType); ok {
		return true
	}
	return isStringView(typ)
}

func isStringView(typ typeinfo.Type) bool {
	target, _, ok := typeinfo.ReferenceTarget(typeinfo.Underlying(typ))
	if !ok {
		return false
	}
	_, ok = typeinfo.Underlying(target).(*typeinfo.StringType)
	return ok
}

type indexableSequenceShape uint8

const (
	indexableFixedArray indexableSequenceShape = iota
	indexableDynamicArray
	indexableSharedSliceView
	indexableMutableSliceView
)

func indexableSequence(t typeinfo.Type) (typeinfo.Type, indexableSequenceShape, bool) {
	switch base := typeinfo.Underlying(t).(type) {
	case *typeinfo.ArrayType:
		if base == nil || base.Elem == nil {
			return nil, 0, false
		}
		if base.Shape == typeinfo.ArrayOwner {
			return base.Elem, indexableDynamicArray, true
		}
		if base.Len != "" {
			return base.Elem, indexableFixedArray, true
		}
	case *typeinfo.RefType:
		if base == nil || base.Target == nil {
			return nil, 0, false
		}
		target, ok := typeinfo.Underlying(base.Target).(*typeinfo.ArrayType)
		if !ok || target == nil || (target.Shape != typeinfo.ArrayOwner && target.Shape != typeinfo.ArraySlice) || target.Elem == nil {
			return nil, 0, false
		}
		if base.Mutable {
			return target.Elem, indexableMutableSliceView, true
		}
		return target.Elem, indexableSharedSliceView, true
	}
	return nil, 0, false
}

func (c *checker) checkRangeBound(scope *symbols.Scope, expr ast.Expr) {
	if c == nil || expr == nil {
		return
	}
	boundType := c.typeExpr(scope, expr, typeinfo.DefaultIntegerType())
	boundType = c.requireValueType(expr, boundType, "range bound")
	if typeinfo.IsInvalidOrUnknown(boundType) {
		return
	}
	if !typeinfo.IsIntegral(boundType) {
		c.ctx.Diagnostics.Add(invalidOperationError(expr,
			"range bound must be an integer"))
	}
}

func (c *checker) typeStructLit(scope *symbols.Scope, node *ast.StructLit, expected typeinfo.Type) typeinfo.Type {
	if node == nil {
		return &typeinfo.InvalidType{}
	}
	if node.Type != nil {
		targetType := typeinfo.TypeFromSyntax(node.Type, project.TypeSyntaxOptions(c.ctx, c.module, nil, false))
		targetStruct, ok := typeinfo.Underlying(targetType).(*typeinfo.StructType)
		if !ok || targetStruct == nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidType,
				"composite literal type must be struct", ast.LocOf(node.Type), "")
			return &typeinfo.InvalidType{}
		}
		return c.typeStructLitWithExpected(scope, node, targetStruct, targetType)
	}
	targetStruct, targetType := c.expectedStructType(expected)
	if targetStruct != nil {
		return c.typeStructLitWithExpected(scope, node, targetStruct, targetType)
	}
	return c.typeStructLitAnonymous(scope, node)
}

func (c *checker) expectedStructType(expected typeinfo.Type) (*typeinfo.StructType, typeinfo.Type) {
	if expected == nil {
		return nil, nil
	}
	if strct, ok := typeinfo.Underlying(expected).(*typeinfo.StructType); ok && strct != nil {
		return strct, expected
	}
	return nil, nil
}

func (c *checker) typeStructLitWithExpected(scope *symbols.Scope, node *ast.StructLit, targetStruct *typeinfo.StructType, targetType typeinfo.Type) typeinfo.Type {
	if targetStruct == nil {
		return &typeinfo.InvalidType{}
	}
	fieldsByName := make(map[string]ast.StructLitField, len(node.Fields))
	for _, field := range node.Fields {
		if field.Name == nil || field.Name.Name == "" {
			continue
		}
		if _, exists := fieldsByName[field.Name.Name]; exists {
			c.ctx.Diagnostics.AddError(diagnostics.ErrDuplicateField,
				"duplicate struct literal field `"+field.Name.Name+"`", ast.LocOf(field.Name), "")
			continue
		}
		fieldsByName[field.Name.Name] = field
	}
	for _, targetField := range targetStruct.Fields {
		field, ok := fieldsByName[targetField.Name]
		if !ok {
			c.ctx.Diagnostics.AddError(diagnostics.ErrMissingInitializer,
				"missing struct literal field `"+targetField.Name+"`", ast.LocOf(node), "").
				WithHelp(fmt.Sprintf("required fields: %s", strings.Join(availableFields(targetType), ", ")))
			continue
		}
		valueType := c.typeExpr(scope, field.Value, targetField.Type)
		valueType = c.requireValueType(field.Value, valueType, "struct field initializer")
		if typeinfo.IsInvalidOrUnknown(valueType) {
			continue
		}
		if !c.assignable(targetField.Type, valueType, field.Value) {
			c.ctx.Diagnostics.AddError(diagnostics.ErrTypeMismatch,
				fmt.Sprintf("cannot assign %s to field `%s` of type %s",
					typeinfo.TypeText(valueType), targetField.Name, typeinfo.TypeText(targetField.Type)), ast.LocOf(field.Value), "")
		}
		delete(fieldsByName, targetField.Name)
	}
	for name, field := range fieldsByName {
		c.ctx.Diagnostics.AddError(diagnostics.ErrFieldNotFound,
			"unknown struct literal field `"+name+"`", ast.LocOf(field.Name), "")
	}
	return targetType
}

func (c *checker) typeStructLitAnonymous(scope *symbols.Scope, node *ast.StructLit) typeinfo.Type {
	fields := make([]typeinfo.Field, 0, len(node.Fields))
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
		if typeinfo.IsInvalidOrUnknown(valueType) {
			valueType = &typeinfo.InvalidType{}
		}
		fields = append(fields, typeinfo.Field{Name: field.Name.Name, Type: valueType})
	}
	return &typeinfo.StructType{Fields: fields}
}

func (c *checker) typeArrayLit(scope *symbols.Scope, node *ast.ArrayLit) typeinfo.Type {
	if node == nil {
		return &typeinfo.InvalidType{}
	}
	arrayType := typeinfo.TypeFromSyntax(node.Type, project.TypeSyntaxOptions(c.ctx, c.module, nil, false))
	if typeinfo.IsInvalidOrUnknown(arrayType) {
		return &typeinfo.InvalidType{}
	}
	array, ok := typeinfo.Underlying(arrayType).(*typeinfo.ArrayType)
	if !ok || array == nil || array.Elem == nil || typeinfo.IsInvalidOrUnknown(array.Elem) {
		c.ctx.Diagnostics.Add(invalidTypeError(node.Type, "invalid array literal type"))
		return &typeinfo.InvalidType{}
	}
	if array.Shape == typeinfo.ArrayOwner {
		if c.rejectReferenceStorage(array.Elem, node.Type, "dynamic arrays", true) ||
			c.rejectUnsizedType(array.Elem, node.Type, "dynamic array element") {
			return &typeinfo.InvalidType{}
		}
		if !typeinfo.IsLowerableType(array.Elem) {
			c.ctx.Diagnostics.Add(invalidTypeError(node.Type,
				"dynamic array element type is not lowerable in current compiler stage"))
			return &typeinfo.InvalidType{}
		}
	}
	if !node.InferredLen {
		if nodeLen, err := strconv.Atoi(array.Len); err == nil && nodeLen != len(node.Values) {
			c.ctx.Diagnostics.AddError(diagnostics.ErrTypeMismatch,
				fmt.Sprintf("array literal has %d values but length is %d", len(node.Values), nodeLen), ast.LocOf(node), "")
		}
	}
	for _, value := range node.Values {
		valueType := c.typeExpr(scope, value, array.Elem)
		valueType = c.requireValueType(value, valueType, "array element initializer")
		if typeinfo.IsInvalidOrUnknown(valueType) {
			continue
		}
		if !c.assignable(array.Elem, valueType, value) {
			c.ctx.Diagnostics.Add(typeMismatchError(value,
				fmt.Sprintf("cannot assign %s to array element of type %s",
					typeinfo.TypeText(valueType), typeinfo.TypeText(array.Elem))))
		}
	}
	return arrayType
}

func (c *checker) typeAsExpr(scope *symbols.Scope, node *ast.AsExpr) typeinfo.Type {
	if c == nil || node == nil {
		return nil
	}
	targetType := typeinfo.TypeFromSyntax(node.TypeExpr, project.TypeSyntaxOptions(c.ctx, c.module, nil, false))
	if targetType == nil || typeinfo.IsInvalidOrUnknown(targetType) {
		c.ctx.Diagnostics.Add(invalidTypeError(node.TypeExpr, "invalid target type for cast"))
		return &typeinfo.InvalidType{}
	}
	if c.rejectUnsizedType(targetType, node.TypeExpr, "cast target") {
		return &typeinfo.InvalidType{}
	}
	if node.Expr == nil {
		return &typeinfo.InvalidType{}
	}
	exprType := c.typeExpr(scope, node.Expr, nil)
	exprType = c.requireValueType(node.Expr, exprType, "cast")
	if typeinfo.IsInvalidOrUnknown(exprType) {
		return &typeinfo.InvalidType{}
	}
	if _, ok := targetType.(*typeinfo.BoolType); ok && typeinfo.IsArithmetic(exprType) {
		return targetType
	}
	compat := typeinfo.CheckNumericCompatibility(targetType, exprType)
	if compat == typeinfo.Incompatible {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCast,
			fmt.Sprintf("cannot cast %s to %s",
				typeinfo.TypeText(exprType), typeinfo.TypeText(targetType)), ast.LocOf(node), "")
		return &typeinfo.InvalidType{}
	}
	return targetType
}

func (c *checker) typeNumber(node *ast.NumberLit, expected typeinfo.Type) typeinfo.Type {
	if node == nil {
		return nil
	}
	if typeinfo.IsInvalidOrUnknown(expected) {
		expected = nil
	}
	if node.ExplicitType != "" {
		explicit, ok := typeinfo.NumericTypeFromName(node.ExplicitType, c.ctx.Target)
		if !ok {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidNumber,
				fmt.Sprintf("unsupported numeric literal suffix `%s`", node.ExplicitType), ast.LocOf(node), "").
				WithHelp("integer suffix widths must be between 1 and 8388608; float suffixes are limited to f32 and f64")
			return &typeinfo.InvalidType{}
		}
		if !typeinfo.LiteralFitsType(node.Value, explicit) {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidNumber,
				fmt.Sprintf("literal `%s%s` does not fit %s", node.Value, node.ExplicitType, node.ExplicitType), ast.LocOf(node), "")
			return &typeinfo.InvalidType{}
		}
		return explicit
	}
	if expected != nil {
		numberTarget := expected
		if optional, ok := typeinfo.Underlying(expected).(*typeinfo.OptionalType); ok && optional != nil && optional.Inner != nil {
			numberTarget = optional.Inner
		}
		naturalType := typeinfo.DefaultNumberType(node.Value)
		if typeinfo.CheckNumericCompatibility(numberTarget, naturalType) == typeinfo.Incompatible {
			c.ctx.Diagnostics.Add(typeMismatchError(node,
				fmt.Sprintf("literal `%s` cannot be used as %s", node.Value, typeinfo.TypeText(expected))))
			return nil
		}
		if !typeinfo.LiteralFitsType(node.Value, numberTarget) {
			d := diagnostics.NewError(fmt.Sprintf("literal `%s` does not fit %s", node.Value, typeinfo.TypeText(numberTarget))).
				WithCode(diagnostics.ErrInvalidNumber).
				WithPrimaryLabel(ast.LocOf(node), "")
			if intType, ok := numberTarget.(*typeinfo.IntegerType); ok {
				d.WithHelp(integerRangeHint(intType))
			}
			c.ctx.Diagnostics.Add(d)
			return nil
		}
		return numberTarget
	}
	return typeinfo.DefaultNumberType(node.Value)
}

func integerRangeHint(t *typeinfo.IntegerType) string {
	if t.Signed {
		bits := t.Bits - 1
		return fmt.Sprintf("%s range: -2^%d to 2^%d-1", typeinfo.TypeText(t), bits, bits)
	}
	return fmt.Sprintf("%s range: 0 to 2^%d-1", typeinfo.TypeText(t), t.Bits)
}

func (c *checker) validBinaryTypes(op string, typ typeinfo.Type) bool {
	switch op {
	case "+", "-", "*", "/":
		return typeinfo.IsArithmetic(typ)
	case "%":
		return typeinfo.IsIntegral(typ)
	case "&", "|", "^", "<<", ">>":
		return typeinfo.IsIntegral(typ)
	case "==", "!=":
		return typeinfo.IsEquatable(typ)
	case "<", "<=", ">", ">=":
		return typeinfo.IsArithmetic(typ)
	case "&&", "||":
		return typeinfo.IsCondition(typ)
	default:
		return false
	}
}
