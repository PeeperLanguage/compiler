package constvalue

import (
	"math/big"
	"strconv"

	"compiler/pkg/numeric"
)

type Value interface {
	constValueNode()
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

func FoldUnary(op string, value Value) (Value, bool) {
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

func FoldBinary(op string, left, right Value) (Value, bool) {
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

func foldIntBinary(op string, left, right *IntConst) (Value, bool) {
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

func foldFloatBinary(op string, left, right *FloatConst) (Value, bool) {
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

func foldBoolBinary(op string, left, right *BoolConst) (Value, bool) {
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
