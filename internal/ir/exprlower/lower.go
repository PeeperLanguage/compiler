package exprlower

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/thir"
	"compiler/internal/ir/typelower"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/flowresult"
	"compiler/internal/semantics/intrinsics"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/pkg/numeric"
)

// Context owns source facts required to lower THIR expressions without retaining
// AST or importing module compilation state.
type Context struct {
	Types       *ir.TypeTable
	Diagnostics *diagnostics.DiagnosticBag
	Source      *thir.Module
	Flow        *flowresult.Result
	ModuleID    moduleid.ID
	Entry       bool
}

type lowerer struct {
	ctx      Context
	expected typeinfo.Type
}

func Lower(ctx Context, expr thir.Expr, expected typeinfo.Type) ir.Expr {
	if expr == nil {
		return &ir.InvalidExpr{Message: "nil expression", Type: ir.InvalidType}
	}
	l := &lowerer{ctx: ctx}
	return l.lower(expr, expected, true)
}

// LowerPlace materializes canonical THIR storage and projection evidence.
func LowerPlace(ctx Context, expr thir.Expr) *ir.Place {
	if expr == nil {
		return nil
	}
	return (&lowerer{ctx: ctx}).place(expr)
}

// LowerImplicitReference borrows addressable storage or creates a temporary
// owner whose lifetime MIR extends through the containing expression.
func LowerImplicitReference(ctx Context, expr thir.Expr, result typeinfo.Type) ir.Expr {
	if expr == nil {
		return &ir.InvalidExpr{Message: "nil reference expression", Type: ir.InvalidType}
	}
	l := &lowerer{ctx: ctx}
	if _, _, reference := typeinfo.ReferenceTarget(typeinfo.Underlying(l.effectiveType(expr))); reference {
		return l.lower(expr, nil, true)
	}
	return ir.WithOrigin(l.referenceValue(expr, result), expr.SourceInfo())
}

func (l *lowerer) lower(expr thir.Expr, expected typeinfo.Type, conversions bool) ir.Expr {
	if expr == nil {
		return &ir.InvalidExpr{Message: "nil expression", Type: ir.InvalidType}
	}
	origin := expr.SourceInfo()
	resolved := l.effectiveType(expr)
	if l.ctx.Flow != nil {
		if test, ok := l.ctx.Flow.CaseTest(ast.NodeID(origin.NodeID)); ok {
			subject, _ := l.ctx.Source.Node(ir.NodeID(test.SubjectID)).(thir.Expr)
			membership := &ir.VariantIs{Value: l.lower(subject, nil, true), Case: test.Case, Type: l.typeID(&typeinfo.BoolType{})}
			if test.CaseWhenTrue {
				return ir.WithOrigin(membership, origin)
			}
			return ir.WithOrigin(&ir.Unary{Op: "!", Arg: membership, Type: membership.Type}, origin)
		}
		if payload, _ := l.ctx.Flow.Payload(ast.NodeID(origin.NodeID)); len(payload.Cases) > 0 && expr.ExprPlace() != nil {
			return ir.WithOrigin(&ir.Load{Place: l.place(expr)}, origin)
		}
	}
	if conversions {
		if converted := l.conversion(expr, expected, resolved); converted != nil {
			return ir.WithOrigin(converted, origin)
		}
	}
	previous := l.expected
	l.expected = expected
	result := expr.LowerExpression(l)
	l.expected = previous
	return ir.WithOrigin(result, origin)
}

func (l *lowerer) conversion(expr thir.Expr, expected, resolved typeinfo.Type) ir.Expr {
	if expected == nil || resolved == nil {
		return nil
	}
	conversion := expr.Conversion()
	if conversion != nil && conversion.Compatibility == typeinfo.Compatible && conversion.Kind == typeinfo.ConversionOptional {
		optional, ok := typeinfo.Underlying(expected).(*typeinfo.OptionalType)
		if ok && optional != nil && optional.Inner != nil && !isNone(expr) && typeinfo.SameType(optional.Inner, resolved) {
			return &ir.VariantMake{Case: ir.OptionalPresentCase, Payload: l.lower(expr, optional.Inner, false), Type: l.typeID(expected)}
		}
	}
	if _, ok := typeinfo.InterfaceTypeOf(expected); ok {
		return l.interfaceValue(expr, expected)
	}
	if conversion == nil || conversion.Compatibility != typeinfo.Compatible {
		return nil
	}
	switch conversion.Kind {
	case typeinfo.ConversionNumeric, typeinfo.ConversionReference, typeinfo.ConversionStruct:
		if target, source := l.typeID(expected), l.typeID(resolved); target != source {
			return &ir.Cast{Expr: l.lower(expr, nil, false), Type: target}
		}
	}
	return nil
}

func (l *lowerer) effectiveType(expr thir.Expr) typeinfo.Type {
	if l.ctx.Flow != nil {
		if typ := l.ctx.Flow.ExprType(ast.NodeID(expr.SourceInfo().NodeID)); typ != nil {
			return typ
		}
	}
	return expr.ExprType()
}
func (l *lowerer) typeID(t typeinfo.Type) ir.TypeID {
	return typelower.Type(l.ctx.Types, l.ctx.Diagnostics, t)
}
func (l *lowerer) returnType(t typeinfo.Type) ir.TypeID {
	return typelower.ReturnType(l.ctx.Types, l.ctx.Diagnostics, t)
}

func (l *lowerer) LowerInvalidExpr(e *thir.InvalidExpr) ir.Expr {
	return &ir.InvalidExpr{Message: e.Message, Type: ir.InvalidType}
}
func (l *lowerer) LowerNumberLiteral(e *thir.NumberLiteral) ir.Expr {
	t := l.effectiveType(e)
	if typeinfo.IsInvalidOrUnknown(t) {
		return &ir.InvalidExpr{Message: "number literal missing resolved type evidence", Type: ir.InvalidType}
	}
	value := e.Value
	if !numeric.IsFloat(value) {
		if canonical, err := numeric.CanonicalizeIntegerLiteral(value); err == nil {
			value = canonical
		}
	}
	family, _, _ := typeinfo.NumericInfo(t)
	if family == typeinfo.NumericFloat {
		if !numeric.IsFloat(e.Value) {
			value += ".0"
		}
		return &ir.FloatLit{Value: value, Type: l.typeID(t)}
	}
	return &ir.IntLit{Value: value, Type: l.typeID(t)}
}
func (l *lowerer) LowerStringLiteral(e *thir.StringLiteral) ir.Expr {
	return &ir.StringLit{Value: e.Value, Type: l.typeID(l.effectiveType(e))}
}
func (l *lowerer) LowerByteLiteral(e *thir.ByteLiteral) ir.Expr {
	if e.Value == "" {
		return &ir.InvalidExpr{Message: "empty byte literal", Type: ir.InvalidType}
	}
	return &ir.IntLit{Value: fmt.Sprintf("%d", e.Value[0]), Type: l.typeID(l.effectiveType(e))}
}
func (l *lowerer) LowerCharLiteral(e *thir.CharLiteral) ir.Expr {
	value, _ := utf8.DecodeRuneInString(e.Value)
	return &ir.IntLit{Value: fmt.Sprintf("%d", value), Type: l.typeID(l.effectiveType(e))}
}
func (l *lowerer) LowerBoolLiteral(e *thir.BoolLiteral) ir.Expr {
	return &ir.BoolLit{Value: e.Value, Type: l.typeID(l.effectiveType(e))}
}
func (l *lowerer) LowerNoneLiteral(e *thir.NoneLiteral) ir.Expr {
	id := l.typeID(l.effectiveType(e))
	if typ, ok := l.ctx.Types.Type(id); ok {
		if _, optional := typ.OptionalPayload(); optional {
			return &ir.VariantMake{Case: ir.OptionalAbsentCase, Type: id}
		}
	}
	return &ir.InvalidExpr{Message: "`none` requires optional context", Type: ir.InvalidType}
}
func (l *lowerer) LowerIdent(e *thir.Ident) ir.Expr {
	return l.ident(e.Symbol, e.Name, l.effectiveType(e))
}
func (l *lowerer) LowerQualifiedIdent(e *thir.QualifiedIdent) ir.Expr {
	return l.ident(e.Symbol, e.Name, l.effectiveType(e))
}
func (l *lowerer) ident(sym *symbols.Symbol, name string, typ typeinfo.Type) ir.Expr {
	if sym == nil {
		return &ir.InvalidExpr{Message: "unresolved identifier: " + name, Type: ir.InvalidType}
	}
	return &ir.Ident{Name: SymbolName(l.ctx.ModuleID, l.ctx.Entry, sym), Type: l.typeID(typ), SymbolID: sym.ID}
}
func (l *lowerer) LowerUnary(e *thir.Unary) ir.Expr {
	return &ir.Unary{Op: e.Op, Arg: l.lower(e.Value, l.expected, true), Type: l.typeID(l.effectiveType(e))}
}
func (l *lowerer) LowerBinary(e *thir.Binary) ir.Expr {
	if e.StringConcat {
		return &ir.StringConcat{Left: l.lower(e.Left, &typeinfo.StringType{}, true), Right: l.lower(e.Right, &typeinfo.RefType{Target: &typeinfo.StringType{}}, true), Type: l.typeID(l.effectiveType(e))}
	}
	leftType := l.effectiveType(e.Left)
	rightType := l.effectiveType(e.Right)
	leftExpected, rightExpected := l.effectiveType(e), l.effectiveType(e)
	switch e.Op {
	case "<<", ">>":
		leftExpected = leftType
		rightExpected = rightType
	case "==", "!=", "<", "<=", ">", ">=", "&&", "||":
		leftExpected = leftType
		rightExpected = rightType
		if conversion := e.Left.Conversion(); conversion != nil && conversion.Compatibility == typeinfo.Compatible {
			leftExpected = rightType
		}
		if conversion := e.Right.Conversion(); conversion != nil && conversion.Compatibility == typeinfo.Compatible {
			rightExpected = leftType
		}
	}
	return &ir.Binary{Op: e.Op, Left: l.lower(e.Left, leftExpected, true), Right: l.lower(e.Right, rightExpected, true), Type: l.typeID(l.effectiveType(e))}
}
func (l *lowerer) LowerVariant(e *thir.Variant) ir.Expr {
	out := &ir.VariantMake{Case: e.Case, Type: l.typeID(l.effectiveType(e))}
	if e.Payload != nil {
		out.Payload = l.lower(e.Payload, e.PayloadType, true)
	}
	return out
}
func (l *lowerer) LowerStructLiteral(e *thir.StructLiteral) ir.Expr {
	fields := make([]ir.Expr, 0, len(e.Fields))
	semantic, _ := typeinfo.Underlying(l.effectiveType(e)).(*typeinfo.StructType)
	for i, field := range e.Fields {
		var expected typeinfo.Type
		if semantic != nil && i < len(semantic.Fields) {
			expected = semantic.Fields[i].Type
		}
		fields = append(fields, l.lower(field.Value, expected, true))
	}
	return &ir.StructLit{Fields: fields, Type: l.typeID(l.effectiveType(e))}
}
func (l *lowerer) LowerArrayLiteral(e *thir.ArrayLiteral) ir.Expr {
	array, _ := typeinfo.Underlying(l.effectiveType(e)).(*typeinfo.ArrayType)
	if array == nil {
		return &ir.InvalidExpr{Message: "array literal type missing", Type: ir.InvalidType}
	}
	values := make([]ir.Expr, 0, len(e.Values))
	for _, value := range e.Values {
		values = append(values, l.lower(value, array.Elem, true))
	}
	return &ir.ArrayLit{Values: values, Dynamic: array.Shape == typeinfo.ArrayOwner, Type: l.typeID(l.effectiveType(e))}
}
func (l *lowerer) LowerField(e *thir.Field) ir.Expr {
	if e.ExprPlace() != nil {
		return &ir.Load{Place: l.place(e)}
	}
	if e.Access != nil {
		return &ir.Field{Base: l.lower(e.Base, nil, true), Index: e.Access.Field, Type: l.typeID(l.effectiveType(e))}
	}
	return &ir.InvalidExpr{Message: "selector lowering not implemented", Type: ir.InvalidType}
}
func (l *lowerer) LowerIndex(e *thir.Index) ir.Expr {
	if ranged, ok := e.Index.(*thir.Range); ok {
		return l.slice(e, ranged)
	}
	return &ir.Load{Place: l.place(e)}
}
func (l *lowerer) LowerRange(*thir.Range) ir.Expr {
	return &ir.InvalidExpr{Message: "range requires index or loop context", Type: ir.InvalidType}
}
func (l *lowerer) LowerAddress(e *thir.Address) ir.Expr {
	if e.Mode == thir.AddressRaw {
		return &ir.AddrOf{Place: l.place(e.Value), Type: l.typeID(l.effectiveType(e))}
	}
	return l.referenceValue(e.Value, l.effectiveType(e))
}
func (l *lowerer) LowerIs(*thir.Is) ir.Expr {
	return &ir.InvalidExpr{Message: "enum case-test evidence missing", Type: ir.InvalidType}
}
func (l *lowerer) LowerFree(e *thir.Free) ir.Expr {
	return &ir.Drop{Value: l.lower(e.Value, nil, true)}
}
func (l *lowerer) LowerPrint(e *thir.Print) ir.Expr {
	return &ir.Print{Value: l.lower(e.Value, nil, true), Newline: e.Newline}
}
func (l *lowerer) LowerCast(e *thir.Cast) ir.Expr {
	return &ir.Cast{Expr: l.lower(e.Value, l.expected, true), Type: l.typeID(l.effectiveType(e))}
}

func (l *lowerer) LowerCall(e *thir.Call) ir.Expr {
	if e.CompilerCall != nil {
		return l.compilerCall(e)
	}
	if field, ok := e.Callee.(*thir.Field); ok {
		return l.methodCall(e, field)
	}
	fn, _ := typeinfo.Underlying(e.Callee.ExprType()).(*typeinfo.FuncType)
	args := make([]ir.Expr, 0, len(e.Args))
	for i, arg := range e.Args {
		var expected typeinfo.Type
		if fn != nil && i < len(fn.Params) {
			expected = fn.Params[i]
		}
		args = append(args, l.argument(arg, expected))
	}
	resultType := l.typeID(l.effectiveType(e))
	if resultType == ir.InvalidType && fn != nil {
		resultType = l.returnType(fn.Return)
	}
	return &ir.Call{Callee: l.lower(e.Callee, nil, true), Args: args, Type: resultType}
}
func (l *lowerer) argument(e thir.Expr, expected typeinfo.Type) ir.Expr {
	if implicit := e.ImplicitReferenceType(); implicit != nil {
		return l.referenceValue(e, implicit)
	}
	return l.lower(e, expected, true)
}
func (l *lowerer) methodCall(call *thir.Call, field *thir.Field) ir.Expr {
	baseType := l.effectiveType(field.Base)
	if iface, slot, ok := lookupInterfaceMethod(baseType, field.Name); ok {
		method, _ := l.ctx.Types.InterfaceMethod(l.typeID(baseType), slot)
		args := make([]ir.Expr, 0, len(call.Args))
		for i, arg := range call.Args {
			var expected typeinfo.Type
			if i < len(iface.Params) {
				expected = iface.Params[i].Type
			}
			args = append(args, l.argument(arg, expected))
		}
		return &ir.InterfaceCall{Base: l.lower(field.Base, nil, true), Slot: slot, SlotType: method.SlotType, Args: args, Consumes: iface.Receiver == typeinfo.MethodReceiverValue, Type: method.Return}
	}
	fn, _ := typeinfo.Underlying(field.ExprType()).(*typeinfo.FuncType)
	if field.Symbol == nil || fn == nil || len(fn.Params) == 0 {
		return &ir.InvalidExpr{Message: "unsupported selector call lowering", Type: ir.InvalidType}
	}
	args := []ir.Expr{l.argument(field.Base, fn.Params[0])}
	for index, arg := range call.Args {
		parameter := index + 1
		if parameter >= len(fn.Params) {
			return &ir.InvalidExpr{Message: "selector call argument evidence missing", Type: ir.InvalidType}
		}
		args = append(args, l.argument(arg, fn.Params[parameter]))
	}
	return &ir.Call{Callee: l.ident(field.Symbol, field.Name, fn), Args: args, Type: l.returnType(fn.Return)}
}
func (l *lowerer) compilerCall(call *thir.Call) ir.Expr {
	switch call.CompilerCall.Kind {
	case intrinsics.FunctionAlloc:
		return l.alloc(call)
	case intrinsics.FunctionCollection:
		return l.collection(call)
	case intrinsics.FunctionDynamicArrayOwner:
		return l.dynamicArray(call)
	case intrinsics.FunctionFromBytes:
		return l.fromBytes(call)
	default:
		panic(fmt.Sprintf("unsupported intrinsic function kind %d", call.CompilerCall.Kind))
	}
}
func (l *lowerer) alloc(call *thir.Call) ir.Expr {
	if len(call.Args) < 1 || len(call.Args) > 2 {
		return &ir.InvalidExpr{Message: "alloc requires 1 or 2 effective arguments", Type: ir.InvalidType}
	}
	out := &ir.AllocExpr{Value: l.lower(call.Args[0], nil, true), Type: l.typeID(l.effectiveType(call))}
	if len(call.Args) == 2 {
		out.Allocator = l.lower(call.Args[1], &typeinfo.AllocatorType{}, true)
	}
	return out
}
func (l *lowerer) collection(call *thir.Call) ir.Expr {
	fn, _ := typeinfo.Underlying(call.Callee.ExprType()).(*typeinfo.FuncType)
	if fn == nil || len(fn.Params) != 1 || len(call.Args) != 1 {
		return &ir.InvalidExpr{Message: "collection function type or arguments missing", Type: ir.InvalidType}
	}
	value := l.argument(call.Args[0], fn.Params[0])
	switch call.CompilerCall.Operation {
	case symbols.CompilerOpLen:
		return &ir.Len{Value: value, Type: l.returnType(fn.Return)}
	case symbols.CompilerOpAsBytes:
		return &ir.SliceView{Place: &ir.Place{Root: value, Type: value.TypeID()}, Type: l.returnType(fn.Return)}
	case symbols.CompilerOpAsChars:
		return &ir.StringChars{Value: value, Type: l.returnType(fn.Return)}
	}
	return &ir.InvalidExpr{Message: "unsupported collection function lowering", Type: ir.InvalidType}
}
func (l *lowerer) dynamicArray(call *thir.Call) ir.Expr {
	fn, _ := typeinfo.Underlying(call.Callee.ExprType()).(*typeinfo.FuncType)
	if fn == nil || len(fn.Params) != len(call.Args) || len(call.Args) < 2 {
		return &ir.InvalidExpr{Message: "dynamic-array operation type or arguments missing", Type: ir.InvalidType}
	}
	args := make([]ir.Expr, len(call.Args))
	for i, arg := range call.Args {
		args[i] = l.argument(arg, fn.Params[i])
	}
	owner, _, ok := typeinfo.ReferenceTarget(typeinfo.Underlying(fn.Params[0]))
	if !ok {
		return &ir.InvalidExpr{Message: "dynamic-array owner reference missing", Type: ir.InvalidType}
	}
	out := &ir.DynamicArrayOp{Op: call.CompilerCall.Operation, Array: args[0], ArrayType: l.typeID(owner), Type: l.returnType(nil)}
	switch out.Op {
	case symbols.CompilerOpAppend:
		out.Value = args[1]
	case symbols.CompilerOpReserve, symbols.CompilerOpShrink:
		out.Length = args[1]
	case symbols.CompilerOpResize:
		if len(args) != 3 {
			return &ir.InvalidExpr{Message: "resize operation arguments missing", Type: ir.InvalidType}
		}
		out.Length = args[1]
		out.Value = args[2]
	}
	return out
}
func (l *lowerer) fromBytes(call *thir.Call) ir.Expr {
	fn, _ := typeinfo.Underlying(call.Callee.ExprType()).(*typeinfo.FuncType)
	if fn == nil || len(call.Args) < 1 || len(call.Args) > 2 {
		panic("validated from_bytes call missing intrinsic evidence")
	}
	out := &ir.StringFromBytes{Bytes: l.argument(call.Args[0], fn.Params[0]), Type: l.returnType(fn.Return)}
	if len(call.Args) == 2 {
		out.Allocator = l.argument(call.Args[1], fn.Params[1])
	}
	return out
}

func (l *lowerer) referenceValue(expr thir.Expr, result typeinfo.Type) ir.Expr {
	id := l.typeID(result)
	target, _, ok := typeinfo.ReferenceTarget(typeinfo.Underlying(result))
	if !ok {
		return &ir.InvalidExpr{Message: "reference lowering requires reference type", Type: ir.InvalidType}
	}
	slice := false
	switch t := typeinfo.Underlying(target).(type) {
	case *typeinfo.StringType:
		slice = true
	case *typeinfo.ArrayType:
		slice = t.Shape == typeinfo.ArraySlice
	}
	if expr.ExprPlace() == nil {
		return &ir.TempBorrow{Value: l.lower(expr, target, true), Slice: slice, Type: id}
	}
	place := l.place(expr)
	if slice {
		return &ir.SliceView{Place: place, Type: id}
	}
	return &ir.AddrOf{Place: place, Type: id}
}
func (l *lowerer) slice(index *thir.Index, r *thir.Range) ir.Expr {
	var start, end ir.Expr
	if r.Start != nil {
		start = l.lower(r.Start, typeinfo.DefaultIntegerType(), true)
	}
	if r.End != nil {
		end = l.lower(r.End, typeinfo.DefaultIntegerType(), true)
	}
	resultType := l.effectiveType(index)
	source := l.place(index.Base)
	if target, _, reference := typeinfo.ReferenceTarget(typeinfo.Underlying(resultType)); reference {
		if _, stringRange := typeinfo.Underlying(target).(*typeinfo.StringType); stringRange {
			root := l.referenceValue(index.Base, resultType)
			source = &ir.Place{Root: root, Type: root.TypeID(), Location: index.Base.SourceInfo().Location}
		}
	}
	return &ir.SliceView{Place: source, Start: start, End: end, EndExclusive: r.EndExclusive, Type: l.typeID(resultType)}
}
func (l *lowerer) place(expr thir.Expr) *ir.Place {
	p := expr.ExprPlace()
	if p == nil {
		return &ir.Place{
			Root:     l.lower(expr, nil, false),
			Type:     l.typeID(l.effectiveType(expr)),
			Location: expr.SourceInfo().Location,
		}
	}

	var root ir.Expr
	var rootType typeinfo.Type
	if p.Root != nil {
		root = l.ident(p.Root, p.Root.Name, p.Root.Type)
		rootType = p.Root.Type
	} else {
		root = l.lower(p.Temporary, nil, true)
		rootType = l.effectiveType(p.Temporary)
	}
	out := &ir.Place{Root: root, Type: l.typeID(rootType), Location: expr.SourceInfo().Location}
	for _, projection := range p.Projections {
		l.appendPayloadProjections(out, projection.BaseSource)
		if projection.DereferenceType != nil {
			out.Type = l.typeID(projection.DereferenceType)
			out.Projections = append(out.Projections, ir.PlaceProjection{
				Kind: ir.PlaceProjectionDeref, Type: out.Type, Location: projection.BaseSource.Location,
			})
		}
		fieldIndex := projection.Field
		projectionType := projection.Type
		if l.ctx.Flow != nil && projection.Kind == thir.PlaceField {
			if access, found := l.ctx.Flow.VariantField(ast.NodeID(projection.Source.NodeID)); found {
				fieldIndex = access.Field
				projectionType = access.Type
			}
		}
		item := ir.PlaceProjection{
			FieldIndex: fieldIndex,
			Type:       l.typeID(projectionType),
			Location:   projection.Source.Location,
		}
		switch projection.Kind {
		case thir.PlaceField:
			item.Kind = ir.PlaceProjectionField
		case thir.PlaceIndex:
			item.Kind = ir.PlaceProjectionIndex
			if projection.ConstantIndex != nil {
				item.Index = &ir.IntLit{
					Value: projection.ConstantIndex.Text,
					Type:  l.typeID(projection.ConstantIndex.Type),
				}
			} else {
				item.Index = l.lower(projection.Index, typeinfo.DefaultIntegerType(), true)
			}
		}
		out.Type = item.Type
		out.Projections = append(out.Projections, item)
	}
	l.appendPayloadProjections(out, expr.SourceInfo())
	return out
}

func (l *lowerer) appendPayloadProjections(place *ir.Place, source ir.SourceInfo) {
	if l.ctx.Flow == nil || place == nil || source.NodeID == 0 {
		return
	}
	id := ast.NodeID(source.NodeID)
	payload, _ := l.ctx.Flow.Payload(id)
	if !payload.AppliesTo(l.ctx.Flow.StorageOrigins(id)) {
		return
	}
	for _, caseIndex := range payload.Cases {
		variant, ok := l.ctx.Types.Type(place.Type)
		if !ok {
			break
		}
		variantCase, ok := variant.VariantCase(caseIndex)
		if !ok {
			break
		}
		place.Type = variantCase.Payload
		place.Projections = append(place.Projections, ir.PlaceProjection{
			Kind: ir.PlaceProjectionVariantPayload, Case: caseIndex,
			Type: place.Type, Location: source.Location,
		})
	}
}

func (l *lowerer) interfaceValue(expr thir.Expr, expected typeinfo.Type) ir.Expr {
	iface, ok := typeinfo.InterfaceTypeOf(expected)
	if !ok {
		return nil
	}
	resolved := l.effectiveType(expr)
	if _, already := typeinfo.InterfaceTypeOf(resolved); already {
		return nil
	}
	data := typeinfo.Underlying(resolved)
	if target, _, reference := typeinfo.ReferenceTarget(data); reference {
		data = target
	} else if target, pointer := typeinfo.PointerTarget(data); pointer {
		data = target
	}
	interfaceID := l.typeID(expected)
	implementations := expr.InterfaceImplementations()
	if len(implementations) != len(iface.Methods) {
		return &ir.InvalidExpr{Message: "missing interface implementation evidence", Type: ir.InvalidType}
	}
	slots := make([]ir.InterfaceSlot, 0, len(iface.Methods))
	for index, method := range iface.Methods {
		implementation := implementations[index]
		lowered, ok := l.ctx.Types.InterfaceMethod(interfaceID, index)
		if !ok || implementation.Symbol == nil || implementation.Symbol.Name != method.Name ||
			implementation.CallableType == nil || lowered.Name != method.Name || lowered.SlotType == ir.InvalidType {
			return &ir.InvalidExpr{Message: "missing interface method implementation", Type: ir.InvalidType}
		}
		slots = append(slots, ir.InterfaceSlot{
			InterfaceType: interfaceID,
			MethodName:    method.Name,
			SlotType:      lowered.SlotType,
			FuncName:      SymbolName(l.ctx.ModuleID, l.ctx.Entry, implementation.Symbol),
			FuncType:      l.typeID(implementation.CallableType),
			DataType:      l.typeID(data),
		})
	}
	return &ir.InterfaceMake{Value: l.lower(expr, nil, false), Slots: slots, Type: interfaceID}
}
func lookupInterfaceMethod(t typeinfo.Type, name string) (*typeinfo.Method, int, bool) {
	iface, ok := typeinfo.InterfaceTypeOf(t)
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
func isNone(expr thir.Expr) bool { _, ok := expr.(*thir.NoneLiteral); return ok }

func SymbolName(module moduleid.ID, entry bool, sym *symbols.Symbol) string {
	if sym == nil {
		return ""
	}
	if sym.CompilerOp == "" && (sym.Kind == symbols.SymbolFunc || sym.Kind == symbols.SymbolMethod) {
		name, external := CallableName(module, entry, sym)
		if external {
			return name
		}
		return fmt.Sprintf("%s$%d", name, sym.ID)
	}
	return fmt.Sprintf("%s$%d", sym.Name, sym.ID)
}
func CallableName(module moduleid.ID, entry bool, sym *symbols.Symbol) (string, bool) {
	if sym == nil || (sym.Kind != symbols.SymbolFunc && sym.Kind != symbols.SymbolMethod) {
		return "", false
	}
	if fn, ok := sym.ASTNode.(*ast.FnDecl); ok {
		if name, external := ast.FunctionLinkName(fn, sym.Name); external {
			return name, true
		}
	}
	if entry && sym.Kind == symbols.SymbolFunc && sym.Name == "main" && sym.DefiningModule == module {
		return "main", false
	}
	receiver := ""
	if typ, ok := symbols.GetSymbolType(sym); ok && sym.Kind == symbols.SymbolMethod {
		if fn, ok := typ.(*typeinfo.FuncType); ok && len(fn.Params) > 0 {
			if target, ok := typeinfo.ReceiverTarget(fn.Params[0]); ok {
				receiver = typeinfo.TypeText(target)
			}
		}
	}
	components := [...]string{sym.DefiningModule.Origin, sym.DefiningModule.Namespace, sym.DefiningModule.Dependency, sym.DefiningModule.ImportPath, string(sym.Kind), sym.Name, receiver}
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
