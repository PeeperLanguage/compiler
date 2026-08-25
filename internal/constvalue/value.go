package constvalue

import (
	"math/big"
	"strconv"

	"compiler/pkg/numeric"
)

type Value interface {
	constValueNode()
	Truthy() bool
	TypeText() string
}

type IntConst struct {
	value  *big.Int
	typeID string
}

type FloatConst struct {
	value  float64
	typeID string
}

type BoolConst struct {
	value bool
}

type StringConst struct {
	value  string
	typeID string
}

// VariantConst keeps semantic case identity and declaration-ordered payload
// fields. Named enums require nominalIdentity; structural variants may leave it
// empty while retaining typeID.
type VariantConst struct {
	nominalIdentity string
	typeID          string
	caseIndex       int
	fieldValues     []Value
}

func (*IntConst) constValueNode()     {}
func (*FloatConst) constValueNode()   {}
func (*BoolConst) constValueNode()    {}
func (*StringConst) constValueNode()  {}
func (*VariantConst) constValueNode() {}

func NewInt(value *big.Int, typeID string) (*IntConst, bool) {
	out, ok := NormalizeInteger(value, typeID)
	if !ok {
		return nil, false
	}
	return &IntConst{value: out, typeID: typeID}, true
}

func NewIntText(text, typeID string) (*IntConst, bool) {
	value, err := numeric.StringToBigInt(text)
	if err != nil {
		return nil, false
	}
	return NewInt(value, typeID)
}

func NewFloat(value float64, typeID string) (*FloatConst, bool) {
	switch typeID {
	case "f32":
		return &FloatConst{value: float64(float32(value)), typeID: typeID}, true
	case "f64":
		return &FloatConst{value: value, typeID: typeID}, true
	default:
		return nil, false
	}
}

func NewFloatText(text, typeID string) (*FloatConst, bool) {
	value, err := numeric.StringToFloat(text)
	if err != nil {
		return nil, false
	}
	return NewFloat(value, typeID)
}

func NewBool(value bool) *BoolConst {
	return &BoolConst{value: value}
}

func NewString(value, typeID string) (*StringConst, bool) {
	switch typeID {
	case "str", "cstr":
		return &StringConst{value: value, typeID: typeID}, true
	default:
		return nil, false
	}
}

func NewVariant(nominalIdentity, typeID string, caseIndex int, fieldValues []Value) (*VariantConst, bool) {
	if typeID == "" || caseIndex < 0 {
		return nil, false
	}
	fields := make([]Value, len(fieldValues))
	for index, field := range fieldValues {
		if field == nil {
			return nil, false
		}
		fields[index] = field
	}
	return &VariantConst{
		nominalIdentity: nominalIdentity,
		typeID:          typeID,
		caseIndex:       caseIndex,
		fieldValues:     fields,
	}, true
}

func (v *IntConst) Int() *big.Int {
	if v == nil || v.value == nil {
		return nil
	}
	return new(big.Int).Set(v.value)
}

func (v *IntConst) Text() string {
	if v == nil || v.value == nil {
		return ""
	}
	return v.value.String()
}

func (v *FloatConst) Float() float64 {
	if v == nil {
		return 0
	}
	return v.value
}

func (v *FloatConst) Text() string {
	if v == nil {
		return ""
	}
	return formatFloatResult(v.value, v.typeID)
}

func (v *BoolConst) Bool() bool {
	return v != nil && v.value
}

func (v *StringConst) Text() string {
	if v == nil {
		return ""
	}
	return v.value
}

func (v *VariantConst) NominalIdentity() string {
	if v == nil {
		return ""
	}
	return v.nominalIdentity
}

func (v *VariantConst) CaseIndex() int {
	if v == nil {
		return -1
	}
	return v.caseIndex
}

func (v *VariantConst) FieldValues() []Value {
	if v == nil {
		return nil
	}
	return append([]Value(nil), v.fieldValues...)
}

func (v *IntConst) Truthy() bool {
	return v != nil && v.value != nil && v.value.Sign() != 0
}

func (v *FloatConst) Truthy() bool {
	return v != nil && v.value != 0
}

func (v *BoolConst) Truthy() bool {
	return v != nil && v.value
}

func (v *StringConst) Truthy() bool {
	return v != nil && v.value != ""
}

func (*VariantConst) Truthy() bool { return false }

func (v *IntConst) TypeText() string {
	if v == nil {
		return ""
	}
	return v.typeID
}

func (v *FloatConst) TypeText() string {
	if v == nil {
		return ""
	}
	return v.typeID
}

func (v *BoolConst) TypeText() string { return "bool" }

func (v *StringConst) TypeText() string {
	if v == nil {
		return ""
	}
	return v.typeID
}

func (v *VariantConst) TypeText() string {
	if v == nil {
		return ""
	}
	return v.typeID
}

func FoldUnary(op string, value Value) (Value, bool) {
	switch v := value.(type) {
	case *IntConst:
		n := v.Int()
		if n == nil {
			return nil, false
		}
		switch op {
		case "-":
			n.Neg(n)
			return NewInt(n, v.TypeText())
		case "~":
			n.Not(n)
			return NewInt(n, v.TypeText())
		case "!":
			return NewBool(n.Sign() == 0), true
		}
	case *FloatConst:
		f := v.Float()
		switch op {
		case "-":
			return NewFloat(-f, v.TypeText())
		case "!":
			return NewBool(f == 0), true
		}
	case *BoolConst:
		if op == "!" {
			return NewBool(!v.Bool()), true
		}
	}
	return nil, false
}

func FoldBinary(op string, left, right Value) (Value, bool) {
	switch lv := left.(type) {
	case *IntConst:
		rv, ok := right.(*IntConst)
		if !ok || (op != "<<" && op != ">>" && lv.TypeText() != rv.TypeText()) {
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
	lv := left.Int()
	if lv == nil {
		return nil, false
	}
	rv := right.Int()
	if rv == nil {
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
		return NewBool(lv.Cmp(rv) == 0), true
	case "!=":
		return NewBool(lv.Cmp(rv) != 0), true
	case "<":
		return NewBool(lv.Cmp(rv) < 0), true
	case "<=":
		return NewBool(lv.Cmp(rv) <= 0), true
	case ">":
		return NewBool(lv.Cmp(rv) > 0), true
	case ">=":
		return NewBool(lv.Cmp(rv) >= 0), true
	case "&&":
		return NewBool(lv.Sign() != 0 && rv.Sign() != 0), true
	case "||":
		return NewBool(lv.Sign() != 0 || rv.Sign() != 0), true
	default:
		return nil, false
	}
	return NewInt(out, left.TypeText())
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
	lv := left.Float()
	rv := right.Float()
	switch op {
	case "+":
		return NewFloat(lv+rv, left.TypeText())
	case "-":
		return NewFloat(lv-rv, left.TypeText())
	case "*":
		return NewFloat(lv*rv, left.TypeText())
	case "/":
		return NewFloat(lv/rv, left.TypeText())
	case "==":
		return NewBool(lv == rv), true
	case "!=":
		return NewBool(lv != rv), true
	case "<":
		return NewBool(lv < rv), true
	case "<=":
		return NewBool(lv <= rv), true
	case ">":
		return NewBool(lv > rv), true
	case ">=":
		return NewBool(lv >= rv), true
	case "&&":
		return NewBool(lv != 0 && rv != 0), true
	case "||":
		return NewBool(lv != 0 || rv != 0), true
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
		return NewBool(left.Bool() == right.Bool()), true
	case "!=":
		return NewBool(left.Bool() != right.Bool()), true
	case "&&":
		return NewBool(left.Bool() && right.Bool()), true
	case "||":
		return NewBool(left.Bool() || right.Bool()), true
	default:
		return nil, false
	}
}
