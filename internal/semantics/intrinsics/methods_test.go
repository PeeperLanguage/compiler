package intrinsics

import (
	"testing"

	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/target"
)

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
