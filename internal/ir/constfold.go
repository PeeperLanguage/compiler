package ir

import (
	"compiler/internal/constvalue"
	"compiler/internal/source"
)

type ConstValue = constvalue.Value
type IntConst = constvalue.IntConst
type FloatConst = constvalue.FloatConst
type BoolConst = constvalue.BoolConst

func FoldExpr(expr Expr, env map[string]ConstValue) Expr {
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
	case *Index:
		return &Index{
			Base:     FoldExpr(node.Base, env),
			Index:    FoldExpr(node.Index, env),
			Type:     node.Type,
			Location: node.Location,
		}
	case *ArrayLit:
		values := make([]Expr, 0, len(node.Values))
		for _, value := range node.Values {
			values = append(values, FoldExpr(value, env))
		}
		return &ArrayLit{Values: values, Type: node.Type, Location: node.Location}
	default:
		return expr
	}
}

func constValueExprAt(value ConstValue, loc *source.Location) Expr {
	switch node := value.(type) {
	case *IntConst:
		if node == nil {
			return &IntLit{Value: "0", Type: "i32", Location: loc}
		}
		return &IntLit{Value: node.Value, Type: node.TypeID, Location: loc}
	case *FloatConst:
		if node == nil {
			return &FloatLit{Value: "0.0", Type: "f64", Location: loc}
		}
		return &FloatLit{Value: node.Value, Type: node.TypeID, Location: loc}
	case *BoolConst:
		return &BoolLit{Value: node != nil && node.Value, Location: loc}
	default:
		return &InvalidExpr{Message: "unknown constant", Type: "<invalid>", Location: loc}
	}
}

func ConstValueOf(expr Expr) (ConstValue, bool) {
	switch node := expr.(type) {
	case *IntLit:
		if node.Type == "bool" {
			return &BoolConst{Value: node.Value != "0"}, true
		}
		return &IntConst{Value: node.Value, TypeID: node.TypeText()}, true
	case *FloatLit:
		return &FloatConst{Value: node.Value, TypeID: node.TypeText()}, true
	case *BoolLit:
		return &BoolConst{Value: node.Value}, true
	default:
		return nil, false
	}
}
