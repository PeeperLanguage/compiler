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
	case *OptionalSome:
		return &OptionalSome{Value: FoldExpr(types, node.Value, env), Type: node.Type, NodeID: node.NodeID, Location: node.Location}
	case *Ident:
		if env != nil {
			if value, ok := env[node.Name]; ok && value != nil {
				return constValueExprAt(value, node.Type, node.Origin())
			}
		}
		return expr
	case *Unary:
		arg := FoldExpr(types, node.Arg, env)
		if value, ok := ConstValueOf(types, arg); ok {
			if folded, ok := constvalue.FoldUnary(node.Op, value); ok {
				return constValueExprAt(folded, node.Type, node.Origin())
			}
		}
		return &Unary{Op: node.Op, Arg: arg, Type: node.Type, NodeID: node.NodeID, Location: node.Location}
	case *Binary:
		left := FoldExpr(types, node.Left, env)
		right := FoldExpr(types, node.Right, env)
		lv, lok := ConstValueOf(types, left)
		rv, rok := ConstValueOf(types, right)
		if lok && rok {
			if folded, ok := constvalue.FoldBinary(node.Op, lv, rv); ok {
				return constValueExprAt(folded, node.Type, node.Origin())
			}
		}
		return &Binary{Op: node.Op, Left: left, Right: right, Type: node.Type, NodeID: node.NodeID, Location: node.Location}
	case *Call:
		return &Call{
			Callee:   FoldExpr(types, node.Callee, env),
			Args:     foldExprs(types, node.Args, env),
			Type:     node.Type,
			NodeID:   node.NodeID,
			Location: node.Location,
		}
	case *Load:
		return &Load{Place: FoldPlace(types, node.Place, env), DropRoot: node.DropRoot, NodeID: node.NodeID, Location: node.Location}
	case *AddrOf:
		return &AddrOf{Place: FoldPlace(types, node.Place, env), Type: node.Type, NodeID: node.NodeID, Location: node.Location}
	case *TempBorrow:
		return &TempBorrow{Value: FoldExpr(types, node.Value, env), Slice: node.Slice, Type: node.Type, NodeID: node.NodeID, Location: node.Location}
	case *Len:
		return &Len{Value: FoldExpr(types, node.Value, env), Type: node.Type, NodeID: node.NodeID, Location: node.Location}
	case *StringChars:
		return &StringChars{Value: FoldExpr(types, node.Value, env), Type: node.Type, NodeID: node.NodeID, Location: node.Location}
	case *SliceView:
		return &SliceView{
			Source:       FoldPlace(types, node.Source, env),
			Start:        FoldExpr(types, node.Start, env),
			End:          FoldExpr(types, node.End, env),
			EndExclusive: node.EndExclusive,
			Type:         node.Type,
			NodeID:       node.NodeID,
			Location:     node.Location,
		}
	case *InterfaceMake:
		return &InterfaceMake{
			Value:    FoldExpr(types, node.Value, env),
			Slots:    node.Slots,
			Type:     node.Type,
			NodeID:   node.NodeID,
			Location: node.Location,
		}
	case *InterfaceCall:
		return &InterfaceCall{
			Base:     FoldExpr(types, node.Base, env),
			Slot:     node.Slot,
			Args:     foldExprs(types, node.Args, env),
			Consumes: node.Consumes,
			Type:     node.Type,
			NodeID:   node.NodeID,
			Location: node.Location,
		}
	case *Field:
		return &Field{
			Base:     FoldExpr(types, node.Base, env),
			Index:    node.Index,
			DropBase: node.DropBase,
			Type:     node.Type,
			NodeID:   node.NodeID,
			Location: node.Location,
		}
	case *StructLit:
		return &StructLit{Fields: foldExprs(types, node.Fields, env), Type: node.Type, NodeID: node.NodeID, Location: node.Location}
	case *ArrayLit:
		return &ArrayLit{Values: foldExprs(types, node.Values, env), Dynamic: node.Dynamic, Type: node.Type, NodeID: node.NodeID, Location: node.Location}
	case *DynamicArrayOp:
		return &DynamicArrayOp{
			Op:        node.Op,
			Array:     FoldExpr(types, node.Array, env),
			Length:    FoldExpr(types, node.Length, env),
			Value:     FoldExpr(types, node.Value, env),
			ArrayType: node.ArrayType,
			Type:      node.Type,
			NodeID:    node.NodeID,
			Location:  node.Location,
		}
	case *AllocExpr:
		return &AllocExpr{
			Value:     FoldExpr(types, node.Value, env),
			Allocator: FoldExpr(types, node.Allocator, env),
			Type:      node.Type,
			NodeID:    node.NodeID,
			Location:  node.Location,
		}
	case *Cast:
		return &Cast{Expr: FoldExpr(types, node.Expr, env), Type: node.Type, NodeID: node.NodeID, Location: node.Location}
	case *Print:
		return &Print{Value: FoldExpr(types, node.Value, env), Newline: node.Newline, NodeID: node.NodeID, Location: node.Location}
	case *Drop:
		return &Drop{Value: FoldExpr(types, node.Value, env), NodeID: node.NodeID, Location: node.Location}
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

func constValueExprAt(value constvalue.Value, typ TypeID, origin SourceInfo) Expr {
	switch node := value.(type) {
	case *constvalue.IntConst:
		if node == nil {
			return &IntLit{Value: "0", Type: typ, NodeID: origin.NodeID, Location: origin.Location}
		}
		return &IntLit{Value: node.Text(), Type: typ, NodeID: origin.NodeID, Location: origin.Location}
	case *constvalue.FloatConst:
		if node == nil {
			return &FloatLit{Value: "0.0", Type: typ, NodeID: origin.NodeID, Location: origin.Location}
		}
		return &FloatLit{Value: node.Text(), Type: typ, NodeID: origin.NodeID, Location: origin.Location}
	case *constvalue.BoolConst:
		return &BoolLit{Value: node != nil && node.Bool(), Type: typ, NodeID: origin.NodeID, Location: origin.Location}
	default:
		return &InvalidExpr{Message: "unknown constant", Type: InvalidType, NodeID: origin.NodeID, Location: origin.Location}
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
	default:
		return nil, false
	}
}
