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

var predeclaredOperations = []symbols.CompilerOp{
	symbols.CompilerOpAppend,
	symbols.CompilerOpReserve,
	symbols.CompilerOpResize,
	symbols.CompilerOpShrink,
	symbols.CompilerOpAlloc,
}

// Operations returns every operation exposed by compiler-owned intrinsic
// registries. Pipeline completeness tests consume this list directly.
func Operations() []symbols.CompilerOp {
	seen := make(map[symbols.CompilerOp]struct{})
	for _, op := range predeclaredOperations {
		seen[op] = struct{}{}
	}
	for _, definitions := range methods {
		for _, method := range definitions {
			seen[method.op] = struct{}{}
		}
	}
	operations := make([]symbols.CompilerOp, 0, len(seen))
	for op := range seen {
		operations = append(operations, op)
	}
	slices.Sort(operations)
	return operations
}

func PredeclaredSymbols(compilerTarget target.Info) []*symbols.Symbol {
	elementType := &typeinfo.NamedType{Name: "T"}
	arrayType := &typeinfo.ArrayType{Dynamic: true, Elem: elementType}
	sizeType, ok := typeinfo.NumericTypeFromName("usize", compilerTarget)
	if !ok {
		panic("missing builtin usize type")
	}
	result := make([]*symbols.Symbol, 0, len(predeclaredOperations))
	for _, op := range predeclaredOperations {
		var fnType *typeinfo.FuncType
		switch op {
		case symbols.CompilerOpAppend:
			fnType = &typeinfo.FuncType{Params: []typeinfo.Type{arrayType, elementType}, Return: arrayType}
		case symbols.CompilerOpReserve, symbols.CompilerOpShrink:
			fnType = &typeinfo.FuncType{Params: []typeinfo.Type{arrayType, sizeType}, Return: arrayType}
		case symbols.CompilerOpResize:
			fnType = &typeinfo.FuncType{Params: []typeinfo.Type{arrayType, sizeType, elementType}, Return: arrayType}
		case symbols.CompilerOpAlloc:
			valueType := &typeinfo.NamedType{Name: "T"}
			fnType = &typeinfo.FuncType{
				Params: []typeinfo.Type{valueType, &typeinfo.AllocatorType{}},
				Return: &typeinfo.OwnedPtrType{Target: valueType},
			}
		default:
			panic("missing predeclared intrinsic type")
		}
		sym := symbols.New(string(op), symbols.SymbolFunc, nil, nil)
		sym.CompilerOp = op
		sym.Type = fnType
		sym.IsPub = true
		result = append(result, sym)
	}
	return result
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
