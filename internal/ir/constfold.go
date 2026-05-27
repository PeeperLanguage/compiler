package ir

import "math"

func FoldExpr(expr Expr) Expr {
	switch node := expr.(type) {
	case *IntLit, *Ident:
		return expr
	case *Unary:
		arg := FoldExpr(node.Arg)
		if value, ok := constInt(arg); ok {
			switch node.Op {
			case "-":
				if value == math.MinInt32 {
					return &Unary{Op: node.Op, Arg: arg}
				}
				return &IntLit{Value: -value}
			case "!":
				if value == 0 {
					return &IntLit{Value: 1}
				}
				return &IntLit{Value: 0}
			}
		}
		return &Unary{Op: node.Op, Arg: arg}
	case *Binary:
		left := FoldExpr(node.Left)
		right := FoldExpr(node.Right)
		if lv, lok := constInt(left); lok {
			if rv, rok := constInt(right); rok {
				if value, ok := foldBinary(node.Op, lv, rv); ok {
					return &IntLit{Value: value}
				}
			}
		}
		return &Binary{Op: node.Op, Left: left, Right: right}
	default:
		return expr
	}
}

func ConstInt(expr Expr) (int32, bool) {
	return constInt(expr)
}

func constInt(expr Expr) (int32, bool) {
	lit, ok := expr.(*IntLit)
	if !ok || lit == nil {
		return 0, false
	}
	return lit.Value, true
}

func foldBinary(op string, left, right int32) (int32, bool) {
	switch op {
	case "+":
		return checkedInt32(int64(left) + int64(right))
	case "-":
		return checkedInt32(int64(left) - int64(right))
	case "*":
		return checkedInt32(int64(left) * int64(right))
	case "/":
		if right == 0 {
			return 0, false
		}
		return left / right, true
	case "%":
		if right == 0 {
			return 0, false
		}
		return left % right, true
	case "==":
		return boolInt(left == right), true
	case "!=":
		return boolInt(left != right), true
	case "<":
		return boolInt(left < right), true
	case "<=":
		return boolInt(left <= right), true
	case ">":
		return boolInt(left > right), true
	case ">=":
		return boolInt(left >= right), true
	case "&&":
		return boolInt(left != 0 && right != 0), true
	case "||":
		return boolInt(left != 0 || right != 0), true
	default:
		return 0, false
	}
}

func checkedInt32(v int64) (int32, bool) {
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, false
	}
	return int32(v), true
}

func boolInt(v bool) int32 {
	if v {
		return 1
	}
	return 0
}
