package intrinsics

import (
	"fmt"
	"testing"

	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/target"
)

func TestDefinitionsAreUniqueAndConstructSignatures(t *testing.T) {
	seen := make(map[string]struct{})
	compilerTargets := make([]target.Info, 0, 2)
	for _, arch := range []string{"386", "amd64"} {
		compilerTarget, err := target.New("linux", arch)
		if err != nil {
			t.Fatal(err)
		}
		compilerTargets = append(compilerTargets, compilerTarget)
	}
	for _, definition := range definitions {
		key := fmt.Sprintf("%t/%d/%s", definition.predeclared, definition.receiver, definition.name)
		if _, exists := seen[key]; exists {
			t.Fatalf("duplicate intrinsic definition %s", key)
		}
		seen[key] = struct{}{}
		if definition.signature == nil {
			t.Fatalf("intrinsic %s has no signature factory", key)
		}
		baseType := typeinfo.Type(nil)
		if !definition.predeclared {
			switch definition.receiver {
			case receiverString:
				baseType = &typeinfo.StringType{}
			case receiverArray:
				baseType = &typeinfo.ArrayType{Dynamic: true, Elem: &typeinfo.IntegerType{Signed: true, Bits: 32}}
			default:
				t.Fatalf("intrinsic method %s has no receiver shape", key)
			}
		}
		for _, compilerTarget := range compilerTargets {
			fnType := definition.signature(baseType, compilerTarget)
			if fnType == nil || fnType.Return == nil {
				t.Fatalf("intrinsic %s built invalid signature for %s", key, compilerTarget.Arch)
			}
		}
	}
}

func TestFunctionSignatureInstantiatesDynamicArrayOwner(t *testing.T) {
	elementType := &typeinfo.IntegerType{Signed: true, Bits: 32}
	ownerType := &typeinfo.DefinedType{
		Name:       "Numbers",
		Underlying: &typeinfo.ArrayType{Dynamic: true, Elem: elementType},
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
			t.Run(arch+"/"+string(test.op), func(t *testing.T) {
				fnType, ok := FunctionSignature(test.op, ownerType, compilerTarget)
				if !ok {
					t.Fatal("signature not found")
				}
				if fnType.Return != ownerType {
					t.Fatalf("return type = %#v, want exact owner type", fnType.Return)
				}
				if len(fnType.Params) != len(test.paramTypes) {
					t.Fatalf("parameter count = %d, want %d", len(fnType.Params), len(test.paramTypes))
				}
				for i, want := range test.paramTypes {
					if want == nil {
						want = sizeType
					}
					if i == 0 {
						if fnType.Params[i] != want {
							t.Fatalf("parameter %d = %#v, want exact owner type", i, fnType.Params[i])
						}
						continue
					}
					if !typeinfo.SameType(fnType.Params[i], want) {
						t.Fatalf("parameter %d = %s, want %s", i, typeinfo.TypeText(fnType.Params[i]), typeinfo.TypeText(want))
					}
				}
			})
		}
	}
}

func TestFunctionSignatureRejectsInvalidDynamicArrayOwner(t *testing.T) {
	for _, baseType := range []typeinfo.Type{
		&typeinfo.IntegerType{Signed: true, Bits: 32},
		&typeinfo.ArrayType{Len: "2", Elem: &typeinfo.IntegerType{Signed: true, Bits: 32}},
		&typeinfo.RefType{Target: &typeinfo.ArrayType{Dynamic: true, Elem: &typeinfo.IntegerType{Signed: true, Bits: 32}}},
	} {
		if _, ok := FunctionSignature(symbols.CompilerOpAppend, baseType, target.Host()); ok {
			t.Fatalf("invalid owner %s received dynamic-array signature", typeinfo.TypeText(baseType))
		}
	}
	if _, ok := FunctionSignature(symbols.CompilerOp("missing"), nil, target.Host()); ok {
		t.Fatal("unknown operation received function signature")
	}
}

func TestCollectionIntrinsicReceiverShapes(t *testing.T) {
	compilerTarget := target.Host()
	stringType := &typeinfo.StringType{}
	stringRef := &typeinfo.RefType{Target: stringType}
	arrayType := &typeinfo.ArrayType{Dynamic: true, Elem: &typeinfo.IntegerType{Signed: true, Bits: 32}}
	arrayRef := &typeinfo.RefType{Target: arrayType}

	tests := []struct {
		name     string
		receiver typeinfo.Type
		method   string
		op       symbols.CompilerOp
	}{
		{name: "string length", receiver: stringType, method: "len", op: symbols.CompilerOpLen},
		{name: "string bytes", receiver: stringRef, method: "as_bytes", op: symbols.CompilerOpAsBytes},
		{name: "string chars", receiver: stringRef, method: "as_chars", op: symbols.CompilerOpAsChars},
		{name: "array length", receiver: arrayType, method: "len", op: symbols.CompilerOpLen},
		{name: "array view length", receiver: arrayRef, method: "len", op: symbols.CompilerOpLen},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			symbol, ok := Symbol(test.receiver, test.method, compilerTarget)
			if !ok || symbol.CompilerOp != test.op {
				t.Fatalf("symbol(%s) = %#v, %v", test.method, symbol, ok)
			}
			if symbol.Type == nil {
				t.Fatalf("symbol(%s) returned nil function type", test.method)
			}
		})
	}
	if _, ok := Symbol(&typeinfo.IntegerType{Signed: true, Bits: 32}, "len", compilerTarget); ok {
		t.Fatal("scalar receiver unexpectedly exposes collection intrinsic")
	}
}

func TestSymbolsExposeBorrowedByteOrigin(t *testing.T) {
	symbolsForString := Symbols(&typeinfo.StringType{}, target.Host())
	for _, symbol := range symbolsForString {
		if symbol.Name != "as_bytes" {
			continue
		}
		fn, ok := symbol.Type.(*typeinfo.FuncType)
		if !ok || fn.ReturnOrigins == nil || len(fn.ReturnOrigins.Sources) != 1 || fn.ReturnOrigins.Sources[0] != 0 {
			t.Fatalf("as_bytes symbol type = %#v, want receiver return origin", symbol.Type)
		}
		return
	}
	t.Fatal("as_bytes symbol missing")
}
