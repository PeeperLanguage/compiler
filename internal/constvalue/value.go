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

type StringConst struct {
	Value  string
	TypeID string
}

func (*IntConst) constValueNode()    {}
func (*FloatConst) constValueNode()  {}
func (*BoolConst) constValueNode()   {}
func (*StringConst) constValueNode() {}

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

func (v *StringConst) Truthy() (bool, bool) {
	if v == nil {
		return false, false
	}
	return v.Value != "", true
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

func (v *StringConst) TypeText() string {
	if v == nil || v.TypeID == "" {
		return "cstr"
	}
	return v.TypeID
}

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
			n, ok := NormalizeInteger(n, v.TypeText())
			if !ok {
				return nil, false
			}
			return &IntConst{Value: n.String(), TypeID: v.TypeID}, true
		case "~":
			n.Not(n)
			n, ok := NormalizeInteger(n, v.TypeText())
			if !ok {
				return nil, false
			}
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
			return &FloatConst{Value: formatFloatResult(-f, v.TypeText()), TypeID: v.TypeID}, true
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
	case "-":
		out.Sub(lv, rv)
	case "*":
		out.Mul(lv, rv)
	case "/":
		if rv.Sign() == 0 {
			return nil, false
		}
		out.Quo(lv, rv)
	case "%":
		if rv.Sign() == 0 {
			return nil, false
		}
		out.Rem(lv, rv)
	case "&":
		out.And(lv, rv)
	case "|":
		out.Or(lv, rv)
	case "^":
		out.Xor(lv, rv)
	case "<<", ">>":
		_, bits, ok := integerConstantType(left.TypeText())
		countValue, countOK := NormalizeInteger(rv, right.TypeText())
		if !ok || !countOK || countValue.Sign() < 0 || countValue.Cmp(big.NewInt(int64(bits))) >= 0 {
			return nil, false
		}
		count := uint(countValue.Uint64())
		if op == "<<" {
			out.Lsh(lv, count)
		} else {
			out.Rsh(lv, count)
		}
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
	out, ok := NormalizeInteger(out, left.TypeText())
	if !ok {
		return nil, false
	}
	return &IntConst{Value: out.String(), TypeID: left.TypeID}, true
}

func integerConstantType(typeID string) (signed bool, bits int, ok bool) {
	if typeID == "byte" {
		return false, 8, true
	}
	return numeric.ParseIntegerTypeName(typeID)
}

// NormalizeInteger applies finite-width two's-complement semantics; typeID
// must name the canonical underlying integer type.
func NormalizeInteger(value *big.Int, typeID string) (*big.Int, bool) {
	signed, bits, ok := integerConstantType(typeID)
	if !ok || value == nil {
		return nil, false
	}
	modulus := new(big.Int).Lsh(big.NewInt(1), uint(bits))
	out := new(big.Int).Mod(value, modulus)
	if signed && out.Bit(bits-1) != 0 {
		out.Sub(out, modulus)
	}
	return out, true
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
		return &FloatConst{Value: formatFloatResult(lv+rv, left.TypeText()), TypeID: left.TypeID}, true
	case "-":
		return &FloatConst{Value: formatFloatResult(lv-rv, left.TypeText()), TypeID: left.TypeID}, true
	case "*":
		return &FloatConst{Value: formatFloatResult(lv*rv, left.TypeText()), TypeID: left.TypeID}, true
	case "/":
		return &FloatConst{Value: formatFloatResult(lv/rv, left.TypeText()), TypeID: left.TypeID}, true
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

func formatFloatResult(value float64, typeID string) string {
	if typeID == "f32" {
		return strconv.FormatFloat(float64(float32(value)), 'g', -1, 32)
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
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
