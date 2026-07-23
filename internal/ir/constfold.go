package ir

import (
	"compiler/internal/constvalue"
	"compiler/internal/source"
)

// FoldExpr recursively folds value-bearing expressions.
// It can replace an entire expression with a constant (e.g., "40 + 2" → IntLit{42}).
//
// Use FoldExpr for sub-expressions that carry values: operands, arguments,
// struct fields, array elements.
//
// Use foldPlace for l-value projections: the root is storage identity
// (not foldable), only index sub-expressions inside are folded.
func FoldExpr(expr Expr, env map[string]constvalue.Value) Expr {
	switch node := expr.(type) {
	case *IntLit, *FloatLit, *BoolLit:
		return expr
	case *Ident:
		if env != nil {
			if value, ok := env[node.Name]; ok && value != nil {
				return constValueExprAt(value, ExprLocation(node))
			}
		}
		return expr
	case *Unary:
		arg := FoldExpr(node.Arg, env)
		if value, ok := ConstValueOf(arg); ok {
			if folded, ok := constvalue.FoldUnary(node.Op, value); ok {
				return constValueExprAt(folded, ExprLocation(node))
			}
		}
		return &Unary{Op: node.Op, Arg: arg, Type: node.Type}
	case *Binary:
		left := FoldExpr(node.Left, env)
		right := FoldExpr(node.Right, env)
		lv, lok := ConstValueOf(left)
		rv, rok := ConstValueOf(right)
		if lok && rok {
			if folded, ok := constvalue.FoldBinary(node.Op, lv, rv); ok {
				return constValueExprAt(folded, ExprLocation(node))
			}
		}
		return &Binary{Op: node.Op, Left: left, Right: right, Type: node.Type}
	case *Load:
		return &Load{Place: foldPlace(node.Place, env), DropRoot: node.DropRoot, Location: node.Location}
	case *AddrOf:
		return &AddrOf{Place: foldPlace(node.Place, env), Type: node.Type, Location: node.Location}
	case *SliceView:
		return &SliceView{
			Source:       foldPlace(node.Source, env),
			Start:        FoldExpr(node.Start, env),
			End:          FoldExpr(node.End, env),
			EndExclusive: node.EndExclusive,
			Type:         node.Type,
			Location:     node.Location,
		}
	case *ArrayLit:
		values := make([]Expr, 0, len(node.Values))
		for _, value := range node.Values {
			values = append(values, FoldExpr(value, env))
		}
		return &ArrayLit{Values: values, Dynamic: node.Dynamic, Type: node.Type, Location: node.Location}
	case *DynamicArrayOp:
		return &DynamicArrayOp{
			Op:       node.Op,
			Array:    FoldExpr(node.Array, env),
			Length:   FoldExpr(node.Length, env),
			Value:    FoldExpr(node.Value, env),
			Type:     node.Type,
			Location: node.Location,
		}
	case *AllocExpr:
		// alloc(value, allocator) — fold the value and allocator sub-expressions.
		// The Type and Location are identity-bearing, not foldable.
		var foldedAlloc Expr
		if node.Allocator != nil {
			foldedAlloc = FoldExpr(node.Allocator, env)
		}
		return &AllocExpr{
			Value:     FoldExpr(node.Value, env),
			Allocator: foldedAlloc,
			Type:      node.Type,
			Location:  node.Location,
		}
	default:
		return expr
	}
}

func foldPlace(place *Place, env map[string]constvalue.Value) *Place {
	if place == nil {
		return nil
	}
	projections := make([]PlaceProjection, len(place.Projections))
	for index, projection := range place.Projections {
		projections[index] = projection
		projections[index].Index = FoldExpr(projection.Index, env)
	}
	// Place roots carry storage identity; only projection operands are foldable.
	return &Place{
		Root:        place.Root,
		Projections: projections,
		Type:        place.Type,
		Location:    place.Location,
	}
}

func constValueExprAt(value constvalue.Value, loc *source.Location) Expr {
	switch node := value.(type) {
	case *constvalue.IntConst:
		if node == nil {
			return &IntLit{Value: "0", Type: "i32", Location: loc}
		}
		return &IntLit{Value: node.Value, Type: node.TypeID, Location: loc}
	case *constvalue.FloatConst:
		if node == nil {
			return &FloatLit{Value: "0.0", Type: "f64", Location: loc}
		}
		return &FloatLit{Value: node.Value, Type: node.TypeID, Location: loc}
	case *constvalue.BoolConst:
		return &BoolLit{Value: node != nil && node.Value, Location: loc}
	default:
		return &InvalidExpr{Message: "unknown constant", Type: "<invalid>", Location: loc}
	}
}

func ConstValueOf(expr Expr) (constvalue.Value, bool) {
	switch node := expr.(type) {
	case *IntLit:
		if node.Type == "bool" {
			return &constvalue.BoolConst{Value: node.Value != "0"}, true
		}
		return &constvalue.IntConst{Value: node.Value, TypeID: node.TypeText()}, true
	case *FloatLit:
		return &constvalue.FloatConst{Value: node.Value, TypeID: node.TypeText()}, true
	case *BoolLit:
		return &constvalue.BoolConst{Value: node.Value}, true
	default:
		return nil, false
	}
}
