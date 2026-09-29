package symbols

import (
	"testing"

	"compiler/internal/constvalue"
)

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
