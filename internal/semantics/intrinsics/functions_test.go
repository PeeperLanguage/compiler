package intrinsics

import (
	"testing"

	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/target"
)

func TestDefinitionsAreUniqueFreeFunctions(t *testing.T) {
	operations := make(map[symbols.CompilerOp]struct{}, len(definitions))
	for _, definition := range definitions {
		if definition.Operation == "" || definition.Kind == 0 || definition.signature == nil {
			t.Fatalf("incomplete intrinsic definition: %#v", definition)
		}
		if _, duplicate := operations[definition.Operation]; duplicate {
			t.Fatalf("duplicate intrinsic operation %q", definition.Operation)
		}
		operations[definition.Operation] = struct{}{}
		for _, arch := range []string{"386", "amd64"} {
			compilerTarget, err := target.New("linux", arch)
			if err != nil {
				t.Fatal(err)
			}
			fnType := definition.Signature(nil, compilerTarget)
			if fnType == nil || len(fnType.Params) == 0 || len(fnType.ParamNames) != len(fnType.Params) {
				t.Fatalf("%s signature for %s = %#v", definition.Operation, arch, fnType)
			}
		}
	}

	predeclared := PredeclaredSymbols(target.Host())
	if len(predeclared) != len(definitions) {
		t.Fatalf("predeclared count = %d, want %d", len(predeclared), len(definitions))
	}
	for _, sym := range predeclared {
		if sym == nil || sym.Kind != symbols.SymbolFunc || sym.CompilerOp == "" || sym.Type == nil {
			t.Fatalf("invalid predeclared function: %#v", sym)
		}
	}
}

func TestApplicableFunctionSymbolsUseOperandShape(t *testing.T) {
	i32 := &typeinfo.IntegerType{Signed: true, Bits: 32}
	owner := &typeinfo.ArrayType{Shape: typeinfo.ArrayOwner, Elem: i32}
	byteSlice := &typeinfo.RefType{Target: &typeinfo.ArrayType{Shape: typeinfo.ArraySlice, Elem: &typeinfo.ByteType{}}}
	tests := []struct {
		name string
		typ  typeinfo.Type
		want []string
	}{
		{name: "scalar", typ: i32, want: []string{"alloc"}},
		{name: "string", typ: &typeinfo.StringType{}, want: []string{"alloc", "len", "as_bytes", "as_chars"}},
		{name: "owner", typ: owner, want: []string{"append", "reserve", "resize", "shrink", "alloc", "len"}},
		{name: "slice", typ: &typeinfo.RefType{Target: &typeinfo.ArrayType{Shape: typeinfo.ArraySlice, Elem: i32}}, want: []string{"alloc", "len"}},
		{name: "byte slice", typ: byteSlice, want: []string{"alloc", "len", "from_bytes"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ApplicableFunctionSymbols(test.typ, target.Host())
			if len(got) != len(test.want) {
				t.Fatalf("applicable functions = %#v, want %v", got, test.want)
			}
			for i, want := range test.want {
				if got[i].Name != want || got[i].Kind != symbols.SymbolFunc {
					t.Fatalf("function %d = %#v, want %q", i, got[i], want)
				}
			}
		})
	}
}

func TestFromBytesSignature(t *testing.T) {
	definition, ok := LookupFunction(symbols.CompilerOpFromBytes)
	fnType := definition.Signature(nil, target.Host())
	if !ok || definition.Kind != FunctionFromBytes || fnType == nil || len(fnType.Params) != 2 ||
		typeinfo.TypeText(fnType.Params[0]) != "&[..]byte" || typeinfo.TypeText(fnType.Params[1]) != "Allocator" ||
		typeinfo.TypeText(fnType.Return) != "str" {
		t.Fatalf("from_bytes signature = %#v", fnType)
	}
}

func TestFunctionSignatureInstantiatesDynamicArrayOwner(t *testing.T) {
	elementType := &typeinfo.IntegerType{Signed: true, Bits: 32}
	ownerType := &typeinfo.DefinedType{
		Name:       "Numbers",
		Underlying: &typeinfo.ArrayType{Shape: typeinfo.ArrayOwner, Elem: elementType},
	}
	tests := []struct {
		op         symbols.CompilerOp
		paramTypes []typeinfo.Type
	}{
		{op: symbols.CompilerOpAppend, paramTypes: []typeinfo.Type{ownerType, elementType}},
		{op: symbols.CompilerOpReserve, paramTypes: []typeinfo.Type{ownerType, nil}},
		{op: symbols.CompilerOpResize, paramTypes: []typeinfo.Type{ownerType, nil, elementType}},
		{op: symbols.CompilerOpShrink, paramTypes: []typeinfo.Type{ownerType, nil}},
	}
	for _, arch := range []string{"386", "amd64"} {
		compilerTarget, err := target.New("linux", arch)
		if err != nil {
			t.Fatal(err)
		}
		sizeType, ok := typeinfo.NumericTypeFromName("usize", compilerTarget)
		if !ok {
			t.Fatal("missing usize type")
		}
		for _, test := range tests {
			definition, ok := LookupFunction(test.op)
			fnType := definition.Signature(ownerType, compilerTarget)
			if !ok || definition.Kind != FunctionDynamicArrayOwner || fnType == nil || fnType.Return != nil || len(fnType.Params) != len(test.paramTypes) {
				t.Fatalf("%s signature = %#v", test.op, fnType)
			}
			for i, want := range test.paramTypes {
				if want == nil {
					want = sizeType
				}
				if i == 0 {
					want = &typeinfo.RefType{Target: want, Mutable: true}
				}
				if !typeinfo.SameType(fnType.Params[i], want) {
					t.Fatalf("%s parameter %d = %s, want %s", test.op, i, typeinfo.TypeText(fnType.Params[i]), typeinfo.TypeText(want))
				}
			}
		}
	}
}

func TestCollectionFunctionSignatures(t *testing.T) {
	compilerTarget := target.Host()
	stringType := &typeinfo.StringType{}
	stringRef := &typeinfo.RefType{Target: stringType}
	lenDefinition, ok := LookupFunction(symbols.CompilerOpLen)
	lenType := lenDefinition.Signature(stringType, compilerTarget)
	sizeType, sizeOK := typeinfo.NumericTypeFromName("usize", compilerTarget)
	if !ok || lenDefinition.Kind != FunctionCollection || !sizeOK || lenType == nil || len(lenType.Params) != 1 || typeinfo.TypeText(lenType.Params[0]) != "&str" || !typeinfo.SameType(lenType.Return, sizeType) {
		t.Fatalf("len(str) signature = %#v", lenType)
	}
	byteDefinition, ok := LookupFunction(symbols.CompilerOpAsBytes)
	byteType := byteDefinition.Signature(stringRef, compilerTarget)
	if !ok || byteDefinition.Kind != FunctionCollection || byteType == nil || typeinfo.TypeText(byteType.Params[0]) != "&str" || typeinfo.TypeText(byteType.Return) != "&[..]byte" ||
		byteType.ReturnOrigins == nil || len(byteType.ReturnOrigins.Sources) != 1 || byteType.ReturnOrigins.Sources[0] != 0 {
		t.Fatalf("as_bytes signature = %#v", byteType)
	}
	charDefinition, ok := LookupFunction(symbols.CompilerOpAsChars)
	charType := charDefinition.Signature(stringType, compilerTarget)
	if !ok || charDefinition.Kind != FunctionCollection || charType == nil || typeinfo.TypeText(charType.Return) != "[]char" {
		t.Fatalf("as_chars signature = %#v", charType)
	}

	i32 := &typeinfo.IntegerType{Signed: true, Bits: 32}
	for _, arrayType := range []typeinfo.Type{
		&typeinfo.ArrayType{Len: "4", Shape: typeinfo.ArrayFixed, Elem: i32},
		&typeinfo.ArrayType{Shape: typeinfo.ArrayOwner, Elem: i32},
		&typeinfo.RefType{Target: &typeinfo.ArrayType{Shape: typeinfo.ArraySlice, Elem: i32}},
	} {
		if lenDefinition.Signature(arrayType, compilerTarget) == nil {
			t.Fatalf("len rejected %s", typeinfo.TypeText(arrayType))
		}
	}
	if lenDefinition.Signature(i32, compilerTarget) != nil {
		t.Fatal("len accepted scalar")
	}
	if byteDefinition.Signature(i32, compilerTarget) != nil {
		t.Fatal("as_bytes accepted scalar")
	}
	if _, ok := LookupFunction(symbols.CompilerOp("missing")); ok {
		t.Fatal("unknown operation received signature")
	}
}
