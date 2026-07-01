package ir

import (
	"math/big"
	"strconv"

	"compiler/internal/source"
	"compiler/pkg/numeric"
)

type ConstValue interface {
	constValueNode()
	ToExpr() Expr
	Truthy() (bool, bool)
	TypeText() string
}

type IntConst struct {
	Value  string
	TypeID string
}

type FloatConst struct {
	Value  string
	TypeID string
}

type BoolConst struct {
	Value bool
}

func (*IntConst) constValueNode()   {}
func (*FloatConst) constValueNode() {}
func (*BoolConst) constValueNode()  {}

func (v *IntConst) ToExpr() Expr {
	if v == nil {
		return &IntLit{Value: "0", Type: "i32"}
	}
	return &IntLit{Value: v.Value, Type: v.TypeID}
}

func (v *FloatConst) ToExpr() Expr {
	if v == nil {
		return &FloatLit{Value: "0.0", Type: "f64"}
	}
	return &FloatLit{Value: v.Value, Type: v.TypeID}
}

func (v *BoolConst) ToExpr() Expr {
	return &BoolLit{Value: v != nil && v.Value}
}

func (v *IntConst) Truthy() (bool, bool) {
	if v == nil {
		return false, false
	}
	n, err := numeric.StringToBigInt(v.Value)
	if err != nil {
		return false, false
	}
	return n.Sign() != 0, true
}

func (v *FloatConst) Truthy() (bool, bool) {
	if v == nil {
		return false, false
	}
	f, err := numeric.StringToFloat(v.Value)
	if err != nil {
		return false, false
	}
	return f != 0, true
}

func (v *BoolConst) Truthy() (bool, bool) {
	if v == nil {
		return false, false
	}
	return v.Value, true
}

func (v *IntConst) TypeText() string {
	if v == nil || v.TypeID == "" {
		return "i32"
	}
	return v.TypeID
}

func (v *FloatConst) TypeText() string {
	if v == nil || v.TypeID == "" {
		return "f64"
	}
	return v.TypeID
}

func (v *BoolConst) TypeText() string { return "bool" }

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
			if folded, ok := foldUnary(node.Op, value); ok {
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
			if folded, ok := foldBinary(node.Op, lv, rv); ok {
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
	expr := value.ToExpr()
	switch node := expr.(type) {
	case *IntLit:
		node.Location = loc
	case *FloatLit:
		node.Location = loc
	case *BoolLit:
		node.Location = loc
	}
	return expr
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

func foldUnary(op string, value ConstValue) (ConstValue, bool) {
	switch v := value.(type) {
	case *IntConst:
		n, err := numeric.StringToBigInt(v.Value)
		if err != nil {
			return nil, false
		}
		switch op {
		case "-":
			n.Neg(n)
			return &IntConst{Value: n.String(), TypeID: v.TypeID}, true
		case "!":
			return &BoolConst{Value: n.Sign() == 0}, true
		}
	case *FloatConst:
		f, err := numeric.StringToFloat(v.Value)
		if err != nil {
			return nil, false
		}
		switch op {
		case "-":
			return &FloatConst{Value: strconv.FormatFloat(-f, 'g', -1, 64), TypeID: v.TypeID}, true
		case "!":
			return &BoolConst{Value: f == 0}, true
		}
	case *BoolConst:
		if op == "!" {
			return &BoolConst{Value: !v.Value}, true
		}
	}
	return nil, false
}

func foldBinary(op string, left, right ConstValue) (ConstValue, bool) {
	switch lv := left.(type) {
	case *IntConst:
		rv, ok := right.(*IntConst)
		if !ok || lv.TypeText() != rv.TypeText() {
			return nil, false
		}
		return foldIntBinary(op, lv, rv)
	case *FloatConst:
		rv, ok := right.(*FloatConst)
		if !ok || lv.TypeText() != rv.TypeText() {
			return nil, false
		}
		return foldFloatBinary(op, lv, rv)
	case *BoolConst:
		rv, ok := right.(*BoolConst)
		if !ok {
			return nil, false
		}
		return foldBoolBinary(op, lv, rv)
	default:
		return nil, false
	}
}

func foldIntBinary(op string, left, right *IntConst) (ConstValue, bool) {
	lv, err := numeric.StringToBigInt(left.Value)
	if err != nil {
		return nil, false
	}
	rv, err := numeric.StringToBigInt(right.Value)
	if err != nil {
		return nil, false
	}
	out := new(big.Int)
	switch op {
	case "+":
		out.Add(lv, rv)
		return &IntConst{Value: out.String(), TypeID: left.TypeID}, true
	case "-":
		out.Sub(lv, rv)
		return &IntConst{Value: out.String(), TypeID: left.TypeID}, true
	case "*":
		out.Mul(lv, rv)
		return &IntConst{Value: out.String(), TypeID: left.TypeID}, true
	case "/":
		if rv.Sign() == 0 {
			return nil, false
		}
		out.Div(lv, rv)
		return &IntConst{Value: out.String(), TypeID: left.TypeID}, true
	case "%":
		if rv.Sign() == 0 {
			return nil, false
		}
		out.Rem(lv, rv)
		return &IntConst{Value: out.String(), TypeID: left.TypeID}, true
	case "==":
		return &BoolConst{Value: lv.Cmp(rv) == 0}, true
	case "!=":
		return &BoolConst{Value: lv.Cmp(rv) != 0}, true
	case "<":
		return &BoolConst{Value: lv.Cmp(rv) < 0}, true
	case "<=":
		return &BoolConst{Value: lv.Cmp(rv) <= 0}, true
	case ">":
		return &BoolConst{Value: lv.Cmp(rv) > 0}, true
	case ">=":
		return &BoolConst{Value: lv.Cmp(rv) >= 0}, true
	case "&&":
		return &BoolConst{Value: lv.Sign() != 0 && rv.Sign() != 0}, true
	case "||":
		return &BoolConst{Value: lv.Sign() != 0 || rv.Sign() != 0}, true
	default:
		return nil, false
	}
}

func foldFloatBinary(op string, left, right *FloatConst) (ConstValue, bool) {
	lv, err := numeric.StringToFloat(left.Value)
	if err != nil {
		return nil, false
	}
	rv, err := numeric.StringToFloat(right.Value)
	if err != nil {
		return nil, false
	}
	switch op {
	case "+":
		return &FloatConst{Value: strconv.FormatFloat(lv+rv, 'g', -1, 64), TypeID: left.TypeID}, true
	case "-":
		return &FloatConst{Value: strconv.FormatFloat(lv-rv, 'g', -1, 64), TypeID: left.TypeID}, true
	case "*":
		return &FloatConst{Value: strconv.FormatFloat(lv*rv, 'g', -1, 64), TypeID: left.TypeID}, true
	case "/":
		return &FloatConst{Value: strconv.FormatFloat(lv/rv, 'g', -1, 64), TypeID: left.TypeID}, true
	case "==":
		return &BoolConst{Value: lv == rv}, true
	case "!=":
		return &BoolConst{Value: lv != rv}, true
	case "<":
		return &BoolConst{Value: lv < rv}, true
	case "<=":
		return &BoolConst{Value: lv <= rv}, true
	case ">":
		return &BoolConst{Value: lv > rv}, true
	case ">=":
		return &BoolConst{Value: lv >= rv}, true
	case "&&":
		return &BoolConst{Value: lv != 0 && rv != 0}, true
	case "||":
		return &BoolConst{Value: lv != 0 || rv != 0}, true
	default:
		return nil, false
	}
}

func foldBoolBinary(op string, left, right *BoolConst) (ConstValue, bool) {
	switch op {
	case "==":
		return &BoolConst{Value: left.Value == right.Value}, true
	case "!=":
		return &BoolConst{Value: left.Value != right.Value}, true
	case "&&":
		return &BoolConst{Value: left.Value && right.Value}, true
	case "||":
		return &BoolConst{Value: left.Value || right.Value}, true
	default:
		return nil, false
	}
}
