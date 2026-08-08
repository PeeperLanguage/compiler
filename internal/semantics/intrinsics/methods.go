package intrinsics

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/target"
)

type receiverShape uint8

const (
	receiverString receiverShape = iota
	receiverArray
)

type definition struct {
	name string
	op   symbols.CompilerOp
}

var methods = map[receiverShape][]definition{
	receiverString: {
		{name: "len", op: symbols.CompilerOpLen},
		{name: "as_bytes", op: symbols.CompilerOpAsBytes},
		{name: "as_chars", op: symbols.CompilerOpAsChars},
	},
	receiverArray: {
		{name: "len", op: symbols.CompilerOpLen},
	},
}

func Symbol(baseType typeinfo.Type, name string, compilerTarget target.Info) (*symbols.Symbol, bool) {
	for _, method := range definitionsFor(baseType) {
		if method.name == name {
			return method.symbol(baseType, compilerTarget), true
		}
	}
	return nil, false
}

func Symbols(baseType typeinfo.Type, compilerTarget target.Info) []*symbols.Symbol {
	definitions := definitionsFor(baseType)
	if definitions == nil {
		return nil
	}
	result := make([]*symbols.Symbol, 0, len(definitions))
	for _, method := range definitions {
		result = append(result, method.symbol(baseType, compilerTarget))
	}
	return result
}

func definitionsFor(baseType typeinfo.Type) []definition {
	shape, ok := shapeOf(baseType)
	if !ok {
		return nil
	}
	return methods[shape]
}

func shapeOf(baseType typeinfo.Type) (receiverShape, bool) {
	if baseType == nil || typeinfo.IsInvalidOrUnknown(baseType) {
		return 0, false
	}
	base := typeinfo.Underlying(baseType)
	if targetType, _, ok := typeinfo.ReferenceTarget(base); ok {
		base = typeinfo.Underlying(targetType)
	}
	switch base.(type) {
	case *typeinfo.StringType:
		return receiverString, true
	case *typeinfo.ArrayType:
		return receiverArray, true
	default:
		return 0, false
	}
}

func (method definition) symbol(baseType typeinfo.Type, compilerTarget target.Info) *symbols.Symbol {
	receiver := baseType
	if targetType, _, ok := typeinfo.ReferenceTarget(typeinfo.Underlying(baseType)); ok {
		receiver = &typeinfo.RefType{Target: targetType}
	} else {
		receiver = &typeinfo.RefType{Target: baseType}
	}
	fnType := &typeinfo.FuncType{
		Params:     []typeinfo.Type{receiver},
		ParamNames: []string{"self"},
	}
	switch method.op {
	case symbols.CompilerOpLen:
		sizeType, ok := typeinfo.NumericTypeFromName("usize", compilerTarget)
		if !ok {
			panic("missing builtin usize type")
		}
		fnType.Return = sizeType
	case symbols.CompilerOpAsBytes:
		fnType.Return = &typeinfo.RefType{Target: &typeinfo.ArrayType{Dynamic: true, Elem: &typeinfo.ByteType{}}}
		fnType.ReturnOrigins = &typeinfo.ReturnOriginContract{Sources: []int{0}}
	case symbols.CompilerOpAsChars:
		fnType.Return = &typeinfo.ArrayType{Dynamic: true, Elem: &typeinfo.CharType{}}
	default:
		panic("missing intrinsic method type")
	}
	sym := symbols.New(method.name, symbols.SymbolMethod, nil, ast.LocOf(nil))
	sym.CompilerOp = method.op
	sym.Type = fnType
	sym.IsPub = true
	return sym
}
