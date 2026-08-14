package lsp

import (
	"testing"

	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

func TestRenderSymbolKinds(t *testing.T) {
	i32 := &typeinfo.IntegerType{Bits: 32, Signed: true}
	point := &typeinfo.NamedType{Name: "Point"}
	function := &typeinfo.FuncType{Params: []typeinfo.Type{i32}, ParamNames: []string{"value"}, Return: i32}
	method := &typeinfo.FuncType{Params: []typeinfo.Type{point}, ParamNames: []string{"self"}, Return: i32}
	cases := []struct {
		name    string
		symbol  *symbols.Symbol
		context symbolRenderContext
		want    string
	}{
		{name: "mutable variable", symbol: &symbols.Symbol{Name: "value", Kind: symbols.SymbolVar, Type: i32, ASTNode: &ast.LetDecl{IsMutable: true}}, want: "(var) mut value: i32"},
		{name: "mutable parameter", symbol: &symbols.Symbol{Name: "value", Kind: symbols.SymbolParam, Type: i32, Mutable: true}, want: "(param) mut value: i32"},
		{name: "constant", symbol: &symbols.Symbol{Name: "Limit", Kind: symbols.SymbolConst, Type: i32}, want: "(const) Limit: i32"},
		{name: "static", symbol: &symbols.Symbol{Name: "Global", Kind: symbols.SymbolStatic, Type: i32}, want: "(static) Global: i32"},
		{name: "field", symbol: &symbols.Symbol{Name: "x", Kind: symbols.SymbolField, Type: i32}, want: "(field) x: i32"},
		{name: "variant", symbol: &symbols.Symbol{Name: "Ready", Kind: symbols.SymbolVariant}, want: "(variant) Ready"},
		{name: "type", symbol: &symbols.Symbol{Name: "Point", Kind: symbols.SymbolType, Type: point}, want: "(type) Point"},
		{name: "function", symbol: &symbols.Symbol{Name: "identity", Kind: symbols.SymbolFunc, Type: function}, want: "(func) fn identity(value: i32) -> i32"},
		{name: "method", symbol: &symbols.Symbol{Name: "sum", Kind: symbols.SymbolMethod, Type: method}, want: "(method) fn (self: Point) sum() -> i32"},
		{name: "import", symbol: &symbols.Symbol{Name: "external", Kind: symbols.SymbolImport}, context: symbolRenderContext{ImportPath: "app/external"}, want: "(import) external -> app/external"},
		{name: "error member", symbol: &symbols.Symbol{Name: "missing", Kind: symbols.SymbolError}, want: "(error_member) missing"},
		{name: "unknown", symbol: &symbols.Symbol{Name: "unknown", Kind: symbols.SymbolUnknown}, want: "(unknown) unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderSymbol(tc.symbol, tc.context); got != tc.want {
				t.Fatalf("renderSymbol() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRenderSymbolUsesSemanticTypesAndASTDecorations(t *testing.T) {
	i32 := &typeinfo.IntegerType{Bits: 32, Signed: true}
	typ := &typeinfo.NamedType{Name: "Resolved"}
	decl := &ast.FnDecl{
		TypeParams: []ast.TypeParam{{Name: &ast.Ident{Name: "T"}}},
		Params: []ast.Param{
			{IsMutable: true, Name: &ast.Ident{Name: "source"}, Type: &ast.NamedType{Name: "Stale"}},
			{Name: &ast.Ident{Name: "fallback"}, Type: &ast.NamedType{Name: "Stale"}, Default: &ast.NumberLit{Value: "1"}},
		},
	}
	sym := &symbols.Symbol{
		Name:    "choose",
		Kind:    symbols.SymbolFunc,
		ASTNode: decl,
		Type: &typeinfo.FuncType{
			Params:     []typeinfo.Type{typ, i32},
			ParamNames: []string{"value", "fallback"},
			Return:     typ,
		},
	}
	want := "(func) fn choose<T>(mut value: Resolved, fallback: i32 = 1) -> Resolved"
	if got := renderSymbol(sym, symbolRenderContext{}); got != want {
		t.Fatalf("renderSymbol() = %q, want %q", got, want)
	}
}
