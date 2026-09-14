package constvalue

import (
	"strconv"

	"compiler/internal/moduleid"
	"compiler/pkg/typednil"
)

// SemanticKey returns the stable semantic identity of a constant value. The
// Value interface is sealed, so every new constant kind must define its own
// identity before it can satisfy Value.
func SemanticKey(value Value) string {
	if value == nil || typednil.IsNil(value) {
		return ""
	}
	return value.semanticKey()
}

func (v *IntConst) semanticKey() string {
	if v == nil {
		return ""
	}
	return moduleid.Frame("integer", v.TypeText(), v.Text())
}

func (v *FloatConst) semanticKey() string {
	if v == nil {
		return ""
	}
	return moduleid.Frame("float", v.TypeText(), v.Text())
}

func (v *BoolConst) semanticKey() string {
	if v == nil {
		return ""
	}
	return moduleid.Frame("bool", strconv.FormatBool(v.Bool()))
}

func (v *StringConst) semanticKey() string {
	if v == nil {
		return ""
	}
	return moduleid.Frame("string", v.TypeText(), v.Text())
}

func (v *VariantConst) semanticKey() string {
	if v == nil {
		return ""
	}
	values := v.FieldValues()
	components := make([]string, 0, 4+len(values))
	components = append(components, "variant", v.NominalIdentity(), v.TypeText(), strconv.Itoa(v.CaseIndex()), strconv.Itoa(len(values)))
	for _, field := range values {
		components = append(components, SemanticKey(field))
	}
	return moduleid.Frame(components...)
}
