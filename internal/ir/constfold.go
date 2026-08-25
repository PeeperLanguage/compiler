package ir

import (
	"fmt"

	"compiler/internal/constvalue"
)

// FoldExpr recursively folds value-bearing expressions.
// It can replace an entire expression with a constant (e.g., "40 + 2" -> IntLit{42}).
//
// Use FoldExpr for sub-expressions that carry values: operands, arguments,
// struct fields, array elements.
//
// Use FoldPlace for l-value projections: the root is storage identity
// (not foldable), only index sub-expressions inside are folded.
func FoldExpr(types *TypeTable, expr Expr, env map[string]constvalue.Value) Expr {
	switch node := expr.(type) {
	case nil:
		return nil
	case *InvalidExpr, *IntLit, *FloatLit, *StringLit, *BoolLit, *ZeroValue:
		return expr
	case *VariantMake:
		return &VariantMake{Case: node.Case, Payload: FoldExpr(types, node.Payload, env), Type: node.Type, SourceInfo: node.SourceInfo}
	case *VariantIs:
		value := FoldExpr(types, node.Value, env)
		if constant, ok := ConstValueOf(types, value); ok {
			if variant, ok := constant.(*constvalue.VariantConst); ok {
				return &BoolLit{Value: variant.CaseIndex() == node.Case, Type: node.Type, SourceInfo: node.SourceInfo}
			}
		}
		return &VariantIs{Value: value, Case: node.Case, Type: node.Type, SourceInfo: node.SourceInfo}
	case *Ident:
		if env != nil {
			if value, ok := env[node.Name]; ok && value != nil {
				return constValueExprAt(types, value, node.Type, node.Origin())
			}
		}
		return expr
	case *Unary:
		arg := FoldExpr(types, node.Arg, env)
		if value, ok := ConstValueOf(types, arg); ok {
			if folded, ok := constvalue.FoldUnary(node.Op, value); ok {
				return constValueExprAt(types, folded, node.Type, node.Origin())
			}
		}
		return &Unary{Op: node.Op, Arg: arg, Type: node.Type, SourceInfo: node.SourceInfo}
	case *Binary:
		left := FoldExpr(types, node.Left, env)
		right := FoldExpr(types, node.Right, env)
		lv, lok := ConstValueOf(types, left)
		rv, rok := ConstValueOf(types, right)
		if lok && rok {
			if folded, ok := constvalue.FoldBinary(node.Op, lv, rv); ok {
				return constValueExprAt(types, folded, node.Type, node.Origin())
			}
		}
		return &Binary{Op: node.Op, Left: left, Right: right, Type: node.Type, SourceInfo: node.SourceInfo}
	case *Call:
		return &Call{
			Callee:     FoldExpr(types, node.Callee, env),
			Args:       foldExprs(types, node.Args, env),
			Type:       node.Type,
			SourceInfo: node.SourceInfo,
		}
	case *Load:
		return &Load{Place: FoldPlace(types, node.Place, env), DropRoot: node.DropRoot, SourceInfo: node.SourceInfo}
	case *AddrOf:
		return &AddrOf{Place: FoldPlace(types, node.Place, env), Type: node.Type, SourceInfo: node.SourceInfo}
	case *TempBorrow:
		return &TempBorrow{Value: FoldExpr(types, node.Value, env), Slice: node.Slice, Type: node.Type, SourceInfo: node.SourceInfo}
	case *Len:
		return &Len{Value: FoldExpr(types, node.Value, env), Type: node.Type, SourceInfo: node.SourceInfo}
	case *StringChars:
		return &StringChars{Value: FoldExpr(types, node.Value, env), Type: node.Type, SourceInfo: node.SourceInfo}
	case *SliceView:
		return &SliceView{
			Source:       FoldPlace(types, node.Source, env),
			Start:        FoldExpr(types, node.Start, env),
			End:          FoldExpr(types, node.End, env),
			EndExclusive: node.EndExclusive,
			Type:         node.Type,
			SourceInfo:   node.SourceInfo,
		}
	case *InterfaceMake:
		return &InterfaceMake{
			Value:      FoldExpr(types, node.Value, env),
			Slots:      node.Slots,
			Type:       node.Type,
			SourceInfo: node.SourceInfo,
		}
	case *InterfaceCall:
		return &InterfaceCall{
			Base:       FoldExpr(types, node.Base, env),
			Slot:       node.Slot,
			Args:       foldExprs(types, node.Args, env),
			Consumes:   node.Consumes,
			Type:       node.Type,
			SourceInfo: node.SourceInfo,
		}
	case *Field:
		return &Field{
			Base:       FoldExpr(types, node.Base, env),
			Index:      node.Index,
			DropBase:   node.DropBase,
			Type:       node.Type,
			SourceInfo: node.SourceInfo,
		}
	case *StructLit:
		return &StructLit{Fields: foldExprs(types, node.Fields, env), Type: node.Type, SourceInfo: node.SourceInfo}
	case *ArrayLit:
		return &ArrayLit{Values: foldExprs(types, node.Values, env), Dynamic: node.Dynamic, Type: node.Type, SourceInfo: node.SourceInfo}
	case *DynamicArrayOp:
		return &DynamicArrayOp{
			Op:         node.Op,
			Array:      FoldExpr(types, node.Array, env),
			Length:     FoldExpr(types, node.Length, env),
			Value:      FoldExpr(types, node.Value, env),
			ArrayType:  node.ArrayType,
			Type:       node.Type,
			SourceInfo: node.SourceInfo,
		}
	case *AllocExpr:
		return &AllocExpr{
			Value:      FoldExpr(types, node.Value, env),
			Allocator:  FoldExpr(types, node.Allocator, env),
			Type:       node.Type,
			SourceInfo: node.SourceInfo,
		}
	case *Cast:
		return &Cast{Expr: FoldExpr(types, node.Expr, env), Type: node.Type, SourceInfo: node.SourceInfo}
	case *Print:
		return &Print{Value: FoldExpr(types, node.Value, env), Newline: node.Newline, SourceInfo: node.SourceInfo}
	case *Drop:
		return &Drop{Value: FoldExpr(types, node.Value, env), SourceInfo: node.SourceInfo}
	default:
		panic(fmt.Sprintf("unhandled IR expression %T in constant folding", expr))
	}
}

func foldExprs(types *TypeTable, expressions []Expr, env map[string]constvalue.Value) []Expr {
	if expressions == nil {
		return nil
	}
	folded := make([]Expr, len(expressions))
	for index, expr := range expressions {
		folded[index] = FoldExpr(types, expr, env)
	}
	return folded
}

// FoldPlace folds projection operands while preserving root storage identity.
func FoldPlace(types *TypeTable, place *Place, env map[string]constvalue.Value) *Place {
	if place == nil {
		return nil
	}
	projections := make([]PlaceProjection, len(place.Projections))
	for index, projection := range place.Projections {
		projections[index] = projection
		projections[index].Index = FoldExpr(types, projection.Index, env)
	}
	// Place roots carry storage identity; only projection operands are foldable.
	return &Place{
		Root:        place.Root,
		Projections: projections,
		Type:        place.Type,
		Location:    place.Location,
	}
}

func constValueExprAt(types *TypeTable, value constvalue.Value, typ TypeID, origin SourceInfo) Expr {
	switch node := value.(type) {
	case *constvalue.IntConst:
		if node == nil {
			return &IntLit{Value: "0", Type: typ, SourceInfo: origin}
		}
		return &IntLit{Value: node.Text(), Type: typ, SourceInfo: origin}
	case *constvalue.FloatConst:
		if node == nil {
			return &FloatLit{Value: "0.0", Type: typ, SourceInfo: origin}
		}
		return &FloatLit{Value: node.Text(), Type: typ, SourceInfo: origin}
	case *constvalue.BoolConst:
		return &BoolLit{Value: node != nil && node.Bool(), Type: typ, SourceInfo: origin}
	case *constvalue.VariantConst:
		variantType, ok := types.Type(typ)
		if node == nil || !ok || variantType.Kind != TypeVariant {
			return &InvalidExpr{Message: "invalid variant constant", Type: InvalidType, SourceInfo: origin}
		}
		variantCase, ok := variantType.VariantCase(node.CaseIndex())
		if !ok {
			return &InvalidExpr{Message: "invalid variant constant case", Type: InvalidType, SourceInfo: origin}
		}
		fields := node.FieldValues()
		var payload Expr
		if variantCase.Payload != InvalidType {
			payloadType, found := types.Type(variantCase.Payload)
			if !found {
				return &InvalidExpr{Message: "invalid variant constant payload type", Type: InvalidType, SourceInfo: origin}
			}
			if payloadType.Kind == TypeStruct {
				if len(fields) != len(payloadType.Fields) {
					return &InvalidExpr{Message: "invalid variant constant field count", Type: InvalidType, SourceInfo: origin}
				}
				values := make([]Expr, len(fields))
				for index, field := range fields {
					values[index] = constValueExprAt(types, field, payloadType.Fields[index].Type, origin)
				}
				payload = &StructLit{Fields: values, Type: variantCase.Payload, SourceInfo: origin}
			} else if len(fields) == 1 {
				payload = constValueExprAt(types, fields[0], variantCase.Payload, origin)
			} else {
				return &InvalidExpr{Message: "invalid variant constant payload", Type: InvalidType, SourceInfo: origin}
			}
		} else if len(fields) != 0 {
			return &InvalidExpr{Message: "payloadless variant constant has fields", Type: InvalidType, SourceInfo: origin}
		}
		return &VariantMake{Case: node.CaseIndex(), Payload: payload, Type: typ, SourceInfo: origin}
	default:
		return &InvalidExpr{Message: "unknown constant", Type: InvalidType, SourceInfo: origin}
	}
}

func ConstValueOf(types *TypeTable, expr Expr) (constvalue.Value, bool) {
	if types == nil {
		return nil, false
	}
	switch node := expr.(type) {
	case *IntLit:
		if types.Text(node.Type) == "bool" {
			return constvalue.NewBool(node.Value != "0"), true
		}
		return constvalue.NewIntText(node.Value, types.Text(node.TypeID()))
	case *FloatLit:
		return constvalue.NewFloatText(node.Value, types.Text(node.TypeID()))
	case *BoolLit:
		return constvalue.NewBool(node.Value), true
	case *VariantMake:
		variantType, ok := types.Type(node.Type)
		if !ok || variantType.Kind != TypeVariant {
			return nil, false
		}
		variantCase, ok := variantType.VariantCase(node.Case)
		if !ok {
			return nil, false
		}
		var fields []constvalue.Value
		if variantCase.Payload != InvalidType {
			if payload, ok := node.Payload.(*StructLit); ok {
				fields = make([]constvalue.Value, len(payload.Fields))
				for index, field := range payload.Fields {
					value, constant := ConstValueOf(types, field)
					if !constant {
						return nil, false
					}
					fields[index] = value
				}
			} else {
				value, constant := ConstValueOf(types, node.Payload)
				if !constant {
					return nil, false
				}
				fields = []constvalue.Value{value}
			}
		}
		return constvalue.NewVariant(variantType.Identity, types.Text(node.Type), node.Case, fields)
	default:
		return nil, false
	}
}
