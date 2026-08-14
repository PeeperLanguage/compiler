package intrinsics

import (
	"slices"

	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/target"
)

type signatureFactory func(symbols.CompilerOp, typeinfo.Type, target.Info) *typeinfo.FuncType

type FunctionKind uint8

const (
	FunctionAlloc FunctionKind = iota + 1
	FunctionCollection
	FunctionDynamicArrayOwner
)

type FunctionDefinition struct {
	Operation symbols.CompilerOp
	Kind      FunctionKind
	signature signatureFactory
}

var definitions = []FunctionDefinition{
	{Operation: symbols.CompilerOpAppend, Kind: FunctionDynamicArrayOwner, signature: dynamicArraySignature},
	{Operation: symbols.CompilerOpReserve, Kind: FunctionDynamicArrayOwner, signature: dynamicArraySignature},
	{Operation: symbols.CompilerOpResize, Kind: FunctionDynamicArrayOwner, signature: dynamicArraySignature},
	{Operation: symbols.CompilerOpShrink, Kind: FunctionDynamicArrayOwner, signature: dynamicArraySignature},
	{Operation: symbols.CompilerOpAlloc, Kind: FunctionAlloc, signature: allocSignature},
	{Operation: symbols.CompilerOpLen, Kind: FunctionCollection, signature: collectionSignature},
	{Operation: symbols.CompilerOpAsBytes, Kind: FunctionCollection, signature: collectionSignature},
	{Operation: symbols.CompilerOpAsChars, Kind: FunctionCollection, signature: collectionSignature},
}

// Operations returns every operation exposed by compiler-owned functions.
func Operations() []symbols.CompilerOp {
	operations := make([]symbols.CompilerOp, 0, len(definitions))
	for _, definition := range definitions {
		operations = append(operations, definition.Operation)
	}
	slices.Sort(operations)
	return operations
}

func PredeclaredSymbols(compilerTarget target.Info) []*symbols.Symbol {
	result := make([]*symbols.Symbol, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, definition.symbolWithType(definition.Signature(nil, compilerTarget)))
	}
	return result
}

func ApplicableFunctionSymbols(baseType typeinfo.Type, compilerTarget target.Info) []*symbols.Symbol {
	result := make([]*symbols.Symbol, 0, len(definitions))
	for _, definition := range definitions {
		if fnType := definition.Signature(baseType, compilerTarget); fnType != nil {
			result = append(result, definition.symbolWithType(fnType))
		}
	}
	return result
}

func LookupFunction(op symbols.CompilerOp) (FunctionDefinition, bool) {
	for _, definition := range definitions {
		if definition.Operation == op {
			return definition, true
		}
	}
	return FunctionDefinition{}, false
}

func (definition FunctionDefinition) Signature(baseType typeinfo.Type, compilerTarget target.Info) *typeinfo.FuncType {
	if definition.signature == nil || definition.Operation == "" {
		return nil
	}
	return definition.signature(definition.Operation, baseType, compilerTarget)
}

func (definition FunctionDefinition) symbolWithType(fnType *typeinfo.FuncType) *symbols.Symbol {
	sym := symbols.New(string(definition.Operation), symbols.SymbolFunc, nil, nil)
	sym.CompilerOp = definition.Operation
	sym.Type = fnType
	sym.IsPub = true
	return sym
}

func dynamicArraySignature(op symbols.CompilerOp, baseType typeinfo.Type, compilerTarget target.Info) *typeinfo.FuncType {
	var elementType typeinfo.Type = &typeinfo.NamedType{Name: "T"}
	var arrayType typeinfo.Type = &typeinfo.ArrayType{Shape: typeinfo.ArrayOwner, Elem: elementType}
	if baseType != nil {
		if targetType, mutable, referenced := typeinfo.ReferenceTarget(typeinfo.Underlying(baseType)); referenced {
			if !mutable {
				return nil
			}
			baseType = targetType
		}
		array, ok := typeinfo.Underlying(baseType).(*typeinfo.ArrayType)
		if !ok || array == nil || array.Shape != typeinfo.ArrayOwner || array.Elem == nil {
			return nil
		}
		elementType = array.Elem
		arrayType = baseType
	}
	sizeType, ok := typeinfo.NumericTypeFromName("usize", compilerTarget)
	if !ok {
		panic("missing builtin usize type")
	}
	params := []typeinfo.Type{&typeinfo.RefType{Target: arrayType, Mutable: true}, sizeType}
	paramNames := []string{"values", "size"}
	switch op {
	case symbols.CompilerOpAppend:
		params[1] = elementType
		paramNames[1] = "value"
	case symbols.CompilerOpResize:
		params = append(params, elementType)
		paramNames = append(paramNames, "value")
	case symbols.CompilerOpReserve:
		paramNames[1] = "minimum"
	case symbols.CompilerOpShrink:
	default:
		panic("missing dynamic array intrinsic signature")
	}
	return &typeinfo.FuncType{Params: params, ParamNames: paramNames}
}

func allocSignature(op symbols.CompilerOp, baseType typeinfo.Type, _ target.Info) *typeinfo.FuncType {
	if op != symbols.CompilerOpAlloc {
		panic("missing alloc intrinsic signature")
	}
	if baseType != nil && typeinfo.ContainsStoredReference(baseType) {
		return nil
	}
	valueType := baseType
	if valueType == nil {
		valueType = &typeinfo.NamedType{Name: "T"}
	}
	return &typeinfo.FuncType{
		Params:     []typeinfo.Type{valueType, &typeinfo.AllocatorType{}},
		ParamNames: []string{"value", "allocator"},
		Return:     &typeinfo.OwnedPtrType{Target: valueType},
	}
}

func collectionSignature(op symbols.CompilerOp, baseType typeinfo.Type, compilerTarget target.Info) *typeinfo.FuncType {
	valueType := baseType
	if valueType == nil {
		valueType = &typeinfo.NamedType{Name: "T"}
	} else if targetType, _, referenced := typeinfo.ReferenceTarget(typeinfo.Underlying(valueType)); referenced {
		valueType = targetType
	}

	base := typeinfo.Underlying(valueType)
	if baseType != nil {
		switch op {
		case symbols.CompilerOpLen:
			switch base.(type) {
			case *typeinfo.StringType, *typeinfo.ArrayType:
			default:
				return nil
			}
		case symbols.CompilerOpAsBytes, symbols.CompilerOpAsChars:
			if _, stringValue := base.(*typeinfo.StringType); !stringValue {
				return nil
			}
		default:
			panic("missing collection intrinsic signature")
		}
	}

	fnType := &typeinfo.FuncType{
		Params:     []typeinfo.Type{&typeinfo.RefType{Target: valueType}},
		ParamNames: []string{"value"},
	}
	switch op {
	case symbols.CompilerOpLen:
		sizeType, ok := typeinfo.NumericTypeFromName("usize", compilerTarget)
		if !ok {
			panic("missing builtin usize type")
		}
		fnType.Return = sizeType
	case symbols.CompilerOpAsBytes:
		fnType.Return = &typeinfo.RefType{Target: &typeinfo.ArrayType{Shape: typeinfo.ArraySlice, Elem: &typeinfo.ByteType{}}}
		fnType.ReturnOrigins = &typeinfo.ReturnOriginContract{Sources: []int{0}}
	case symbols.CompilerOpAsChars:
		fnType.Return = &typeinfo.ArrayType{Shape: typeinfo.ArrayOwner, Elem: &typeinfo.CharType{}}
	default:
		panic("missing collection intrinsic signature")
	}
	return fnType
}
