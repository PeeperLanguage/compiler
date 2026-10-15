package intrinsics

import (
	"slices"

	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/types"
	"compiler/internal/target"
)

type signatureFactory func(symbols.CompilerOp, types.Type, target.Info) *types.FuncType

type FunctionKind uint8

const (
	FunctionAlloc FunctionKind = iota + 1
	FunctionCollection
	FunctionDynamicArrayOwner
	FunctionFromBytes
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
	{Operation: symbols.CompilerOpFromBytes, Kind: FunctionFromBytes, signature: fromBytesSignature},
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

func ApplicableFunctionSymbols(baseType types.Type, compilerTarget target.Info) []*symbols.Symbol {
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

func (definition FunctionDefinition) Signature(baseType types.Type, compilerTarget target.Info) *types.FuncType {
	if definition.signature == nil || definition.Operation == "" {
		return nil
	}
	return definition.signature(definition.Operation, baseType, compilerTarget)
}

func (definition FunctionDefinition) symbolWithType(fnType *types.FuncType) *symbols.Symbol {
	name := string(definition.Operation)
	sym := symbols.New(symbols.CompilerSymbolID(symbols.SymbolFunc, "intrinsic:"+name), name, symbols.SymbolFunc, nil, nil)
	sym.CompilerOp = definition.Operation
	sym.BindType(fnType)
	sym.IsPub = true
	return sym
}

func dynamicArraySignature(op symbols.CompilerOp, baseType types.Type, compilerTarget target.Info) *types.FuncType {
	var elementType types.Type = &types.NamedType{Name: "T"}
	var arrayType types.Type = &types.ArrayType{Shape: types.ArrayOwner, Elem: elementType}
	if baseType != nil {
		if targetType, isMutable, referenced := types.ReferenceTarget(types.Underlying(baseType)); referenced {
			if !isMutable {
				return nil
			}
			baseType = targetType
		}
		array, ok := types.Underlying(baseType).(*types.ArrayType)
		if !ok || array == nil || array.Shape != types.ArrayOwner || array.Elem == nil {
			return nil
		}
		elementType = array.Elem
		arrayType = baseType
	}
	sizeType, ok := types.NumericTypeFromName("usize", compilerTarget)
	if !ok {
		panic("missing builtin usize type")
	}
	params := []types.Type{&types.RefType{Target: arrayType, IsMutable: true}, sizeType}
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
	return &types.FuncType{Params: params, ParamNames: paramNames}
}

func allocSignature(op symbols.CompilerOp, baseType types.Type, _ target.Info) *types.FuncType {
	if op != symbols.CompilerOpAlloc {
		panic("missing alloc intrinsic signature")
	}
	if baseType != nil && types.ContainsStoredReference(baseType) {
		return nil
	}
	valueType := baseType
	if valueType == nil {
		valueType = &types.NamedType{Name: "T"}
	}
	return &types.FuncType{
		Params:     []types.Type{valueType, &types.AllocatorType{}},
		ParamNames: []string{"value", "allocator"},
		Return:     &types.OwnedPtrType{Target: valueType},
	}
}

func fromBytesSignature(op symbols.CompilerOp, baseType types.Type, _ target.Info) *types.FuncType {
	if op != symbols.CompilerOpFromBytes {
		panic("missing from_bytes intrinsic signature")
	}
	bytes := &types.RefType{Target: &types.ArrayType{Shape: types.ArraySlice, Elem: &types.ByteType{}}}
	if baseType != nil && !types.IsSameType(baseType, bytes) {
		return nil
	}
	return &types.FuncType{
		Params:     []types.Type{bytes, &types.AllocatorType{}},
		ParamNames: []string{"bytes", "allocator"},
		Return:     &types.StringType{},
	}
}

func collectionSignature(op symbols.CompilerOp, baseType types.Type, compilerTarget target.Info) *types.FuncType {
	valueType := baseType
	if valueType == nil {
		valueType = &types.NamedType{Name: "T"}
	} else if targetType, _, referenced := types.ReferenceTarget(types.Underlying(valueType)); referenced {
		valueType = targetType
	}

	base := types.Underlying(valueType)
	if baseType != nil {
		switch op {
		case symbols.CompilerOpLen:
			switch base.(type) {
			case *types.StringType, *types.ArrayType:
			default:
				return nil
			}
		case symbols.CompilerOpAsBytes, symbols.CompilerOpAsChars:
			if _, stringValue := base.(*types.StringType); !stringValue {
				return nil
			}
		default:
			panic("missing collection intrinsic signature")
		}
	}

	fnType := &types.FuncType{
		Params:     []types.Type{&types.RefType{Target: valueType}},
		ParamNames: []string{"value"},
	}
	switch op {
	case symbols.CompilerOpLen:
		sizeType, ok := types.NumericTypeFromName("usize", compilerTarget)
		if !ok {
			panic("missing builtin usize type")
		}
		fnType.Return = sizeType
	case symbols.CompilerOpAsBytes:
		fnType.Return = &types.RefType{Target: &types.ArrayType{Shape: types.ArraySlice, Elem: &types.ByteType{}}}
		fnType.ReturnOrigins = &types.ReturnOriginContract{Sources: []int{0}}
	case symbols.CompilerOpAsChars:
		fnType.Return = &types.ArrayType{Shape: types.ArrayOwner, Elem: &types.CharType{}}
	default:
		panic("missing collection intrinsic signature")
	}
	return fnType
}
