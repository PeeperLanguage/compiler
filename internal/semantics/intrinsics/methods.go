package intrinsics

import (
	"slices"

	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/target"
)

type receiverShape uint8

const (
	receiverNone receiverShape = iota
	receiverString
	receiverArray
)

type signatureFactory func(typeinfo.Type, target.Info) *typeinfo.FuncType

type definition struct {
	name        string
	op          symbols.CompilerOp
	predeclared bool
	receiver    receiverShape
	signature   signatureFactory
}

var definitions = []definition{
	{name: "append", op: symbols.CompilerOpAppend, predeclared: true, signature: dynamicArraySignature(symbols.CompilerOpAppend)},
	{name: "reserve", op: symbols.CompilerOpReserve, predeclared: true, signature: dynamicArraySignature(symbols.CompilerOpReserve)},
	{name: "resize", op: symbols.CompilerOpResize, predeclared: true, signature: dynamicArraySignature(symbols.CompilerOpResize)},
	{name: "shrink", op: symbols.CompilerOpShrink, predeclared: true, signature: dynamicArraySignature(symbols.CompilerOpShrink)},
	{name: "alloc", op: symbols.CompilerOpAlloc, predeclared: true, signature: allocSignature},
	{name: "len", op: symbols.CompilerOpLen, receiver: receiverString, signature: collectionMethodSignature(symbols.CompilerOpLen)},
	{name: "as_bytes", op: symbols.CompilerOpAsBytes, receiver: receiverString, signature: collectionMethodSignature(symbols.CompilerOpAsBytes)},
	{name: "as_chars", op: symbols.CompilerOpAsChars, receiver: receiverString, signature: collectionMethodSignature(symbols.CompilerOpAsChars)},
	{name: "len", op: symbols.CompilerOpLen, receiver: receiverArray, signature: collectionMethodSignature(symbols.CompilerOpLen)},
}

// Operations returns every operation exposed by compiler-owned intrinsics.
func Operations() []symbols.CompilerOp {
	seen := make(map[symbols.CompilerOp]struct{}, len(definitions))
	for _, definition := range definitions {
		seen[definition.op] = struct{}{}
	}
	operations := make([]symbols.CompilerOp, 0, len(seen))
	for op := range seen {
		operations = append(operations, op)
	}
	slices.Sort(operations)
	return operations
}

func PredeclaredSymbols(compilerTarget target.Info) []*symbols.Symbol {
	result := make([]*symbols.Symbol, 0)
	for _, definition := range definitions {
		if definition.predeclared {
			result = append(result, definition.symbol(nil, compilerTarget))
		}
	}
	return result
}

func FunctionSignature(op symbols.CompilerOp, baseType typeinfo.Type, compilerTarget target.Info) (*typeinfo.FuncType, bool) {
	for _, definition := range definitions {
		if definition.predeclared && definition.op == op {
			fnType := definition.signature(baseType, compilerTarget)
			return fnType, fnType != nil
		}
	}
	return nil, false
}

func Symbol(baseType typeinfo.Type, name string, compilerTarget target.Info) (*symbols.Symbol, bool) {
	shape, ok := shapeOf(baseType)
	if !ok {
		return nil, false
	}
	for _, definition := range definitions {
		if !definition.predeclared && definition.receiver == shape && definition.name == name {
			return definition.symbol(baseType, compilerTarget), true
		}
	}
	return nil, false
}

func Symbols(baseType typeinfo.Type, compilerTarget target.Info) []*symbols.Symbol {
	shape, ok := shapeOf(baseType)
	if !ok {
		return nil
	}
	result := make([]*symbols.Symbol, 0)
	for _, definition := range definitions {
		if !definition.predeclared && definition.receiver == shape {
			result = append(result, definition.symbol(baseType, compilerTarget))
		}
	}
	return result
}

func shapeOf(baseType typeinfo.Type) (receiverShape, bool) {
	if baseType == nil || typeinfo.IsInvalidOrUnknown(baseType) {
		return receiverNone, false
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
		return receiverNone, false
	}
}

func (definition definition) symbol(baseType typeinfo.Type, compilerTarget target.Info) *symbols.Symbol {
	kind := symbols.SymbolFunc
	if !definition.predeclared {
		kind = symbols.SymbolMethod
	}
	sym := symbols.New(definition.name, kind, nil, ast.LocOf(nil))
	sym.CompilerOp = definition.op
	sym.Type = definition.signature(baseType, compilerTarget)
	sym.IsPub = true
	return sym
}

func dynamicArraySignature(op symbols.CompilerOp) signatureFactory {
	return func(baseType typeinfo.Type, compilerTarget target.Info) *typeinfo.FuncType {
		var elementType typeinfo.Type = &typeinfo.NamedType{Name: "T"}
		var arrayType typeinfo.Type = &typeinfo.ArrayType{Dynamic: true, Elem: elementType}
		if baseType != nil {
			array, ok := typeinfo.Underlying(baseType).(*typeinfo.ArrayType)
			if !ok || array == nil || !array.Dynamic || array.Elem == nil {
				return nil
			}
			elementType = array.Elem
			arrayType = baseType
		}
		sizeType, ok := typeinfo.NumericTypeFromName("usize", compilerTarget)
		if !ok {
			panic("missing builtin usize type")
		}
		params := []typeinfo.Type{arrayType, sizeType}
		switch op {
		case symbols.CompilerOpAppend:
			params[1] = elementType
		case symbols.CompilerOpResize:
			params = append(params, elementType)
		case symbols.CompilerOpReserve, symbols.CompilerOpShrink:
		default:
			panic("missing dynamic array intrinsic signature")
		}
		return &typeinfo.FuncType{Params: params, Return: arrayType}
	}
}

func allocSignature(_ typeinfo.Type, _ target.Info) *typeinfo.FuncType {
	valueType := &typeinfo.NamedType{Name: "T"}
	return &typeinfo.FuncType{
		Params: []typeinfo.Type{valueType, &typeinfo.AllocatorType{}},
		Return: &typeinfo.OwnedPtrType{Target: valueType},
	}
}

func collectionMethodSignature(op symbols.CompilerOp) signatureFactory {
	return func(baseType typeinfo.Type, compilerTarget target.Info) *typeinfo.FuncType {
		receiver := baseType
		if targetType, _, ok := typeinfo.ReferenceTarget(typeinfo.Underlying(baseType)); ok {
			receiver = &typeinfo.RefType{Target: targetType}
		} else {
			receiver = &typeinfo.RefType{Target: baseType}
		}
		fnType := &typeinfo.FuncType{Params: []typeinfo.Type{receiver}, ParamNames: []string{"self"}}
		switch op {
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
			panic("missing collection intrinsic signature")
		}
		return fnType
	}
}
