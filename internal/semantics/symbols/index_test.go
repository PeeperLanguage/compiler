package symbols

import (
	"testing"

	"compiler/internal/constvalue"
	"compiler/internal/semantics/typeinfo"
)

func TestIndexCatalogSnapshotsProtectOwnedSlices(t *testing.T) {
	index := NewIndex()
	receiver := &typeinfo.DefinedType{Identity: "test::Receiver", Kind: typeinfo.DefinedKindStruct}
	first := New(ProjectedSymbolID(SymbolMethod, "first"), "first", SymbolMethod, nil, nil)
	second := New(ProjectedSymbolID(SymbolMethod, "second"), "second", SymbolMethod, nil, nil)
	index.RegisterMethod(receiver, first)
	index.RegisterMethod(receiver, second)

	methods := index.Methods(receiver)
	methods[0] = second
	methods = append(methods, first)
	freshMethods := index.Methods(receiver)
	if len(freshMethods) != 2 || freshMethods[0] != first || freshMethods[1] != second {
		t.Fatalf("method catalog changed through snapshot: %#v", freshMethods)
	}

	alpha := New(ProjectedSymbolID(SymbolFunc, "alpha"), "alpha", SymbolFunc, nil, nil)
	beta := New(ProjectedSymbolID(SymbolFunc, "beta"), "beta", SymbolFunc, nil, nil)
	index.AddOperationFunction(beta)
	index.AddOperationFunction(alpha)
	index.SortOperationFunctions()

	functions := index.OperationFunctions()
	functions[0] = beta
	functions = append(functions, alpha)
	freshFunctions := index.OperationFunctions()
	if len(freshFunctions) != 2 || freshFunctions[0] != alpha || freshFunctions[1] != beta {
		t.Fatalf("operation catalog changed through snapshot: %#v", freshFunctions)
	}
}

func TestIndexPublishesAndClearsConstants(t *testing.T) {
	index := NewIndex()
	id := ProjectedSymbolID(SymbolConst, "Value")
	value, ok := constvalue.NewIntText("7", "i32")
	if !ok {
		t.Fatal("failed to construct constant")
	}

	if got := index.ConstantValue(id); got != nil {
		t.Fatalf("constant before publish = %#v, want nil", got)
	}
	index.PublishConstant(id, value)
	if got := index.ConstantValue(id); got != value {
		t.Fatalf("published constant = %#v, want %#v", got, value)
	}
	index.ClearConstants()
	if got := index.ConstantValue(id); got != nil {
		t.Fatalf("constant after clear = %#v, want nil", got)
	}
}
