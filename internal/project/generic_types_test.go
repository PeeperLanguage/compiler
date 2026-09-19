package project

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

func TestTypeArgumentIdentityUsesCanonicalParameterKey(t *testing.T) {
	parameter := &typeinfo.TypeParameterType{Name: "T", OwnerIdentity: "pkg::Box", Index: 1}
	if got, want := typeArgumentIdentity(parameter), typeinfo.SemanticKey(parameter); got != want {
		t.Fatalf("parameter identity = %q, want semantic key %q", got, want)
	}
}

func TestTypeArgumentIdentityKeepsNominalDefinedIdentity(t *testing.T) {
	defined := &typeinfo.DefinedType{Name: "Box", Identity: "pkg::Box", Kind: typeinfo.DefinedKindStruct}
	if got, want := typeArgumentIdentity(defined), "defined:pkg::Box"; got != want {
		t.Fatalf("defined identity = %q, want %q", got, want)
	}
}

func TestQueryTypeQualifiedSyntaxWithoutModuleDoesNotPanic(t *testing.T) {
	node := &ast.ScopeResolution{Segments: []ast.PathSegment{
		{Name: &ast.Ident{Name: "dep"}},
		{Name: &ast.Ident{Name: "Thing"}},
	}}

	got := QueryType(nil, nil, node, TypeContext{})
	if got.Status != TypeQueryAvailable || got.Type == nil || got.Type.Text() != "dep::Thing" {
		t.Fatalf("qualified query result = %#v, want available unresolved qualified name", got)
	}
}

func TestQueryTypeKeepsInvalidGenericApplicationDistinctFromLoading(t *testing.T) {
	ctx, module, _ := genericQueryContext(t)
	node := &ast.NamedType{Name: "Box"}

	got := QueryType(ctx, module, node, TypeContext{})
	if got.Status != TypeQueryInvalid || !typeinfo.IsInvalid(got.Type) {
		t.Fatalf("invalid generic query result = %#v, want invalid", got)
	}
}

func TestQueryTypeDoesNotCreateOrDiagnoseGenericInstance(t *testing.T) {
	ctx, module, _ := genericQueryContext(t)
	node := &ast.AppliedType{
		Name: &ast.Ident{Name: "Box"},
		TypeArgs: []ast.TypeExpr{
			&ast.NamedType{Name: "i32"},
		},
	}

	got := QueryType(ctx, module, node, TypeContext{})
	symbol, found := module.ModuleScope.LookupLocal("Box")
	if !found || symbol == nil {
		t.Fatal("generic type symbol missing")
	}
	if symbol.IsUsed() {
		t.Fatal("query marked generic type symbol used")
	}
	if got.Status != TypeQueryLoading || got.Type != nil {
		t.Fatalf("uncached query result = %#v, want loading without type", got)
	}
	if len(ctx.typeInstances) != 0 {
		t.Fatalf("query created %d generic instances", len(ctx.typeInstances))
	}
	if ctx.Diagnostics.HasErrors() {
		t.Fatalf("query emitted diagnostics: %s", ctx.Diagnostics.EmitAllToString())
	}
}

func TestQueryTypeDoesNotReturnIncompleteCachedGenericInstance(t *testing.T) {
	ctx, module, base := genericQueryContext(t)
	argument := &typeinfo.IntegerType{Signed: true, Bits: 32}
	identity := typeInstanceIdentity(base, []typeinfo.Type{argument})
	ctx.typeInstances[identity] = namedTypeInstance{
		base: base,
		typ: &typeinfo.DefinedType{
			Name:           base.Name,
			Identity:       identity,
			Kind:           base.Kind,
			TypeParameters: base.TypeParameters,
			TypeArguments:  []typeinfo.Type{argument},
		},
		ready: make(chan struct{}),
	}
	node := &ast.AppliedType{
		Name: &ast.Ident{Name: "Box"},
		TypeArgs: []ast.TypeExpr{
			&ast.NamedType{Name: "i32"},
		},
	}

	if got := QueryType(ctx, module, node, TypeContext{}); got.Status != TypeQueryLoading || got.Type != nil {
		t.Fatalf("incomplete cached query result = %#v, want loading without type", got)
	}
}

func TestQueryTypeReturnsCompleteCachedGenericInstance(t *testing.T) {
	ctx, module, base := genericQueryContext(t)
	argument := &typeinfo.IntegerType{Signed: true, Bits: 32}
	identity := typeInstanceIdentity(base, []typeinfo.Type{argument})
	cached := &typeinfo.DefinedType{
		Name:           base.Name,
		Identity:       identity,
		Kind:           base.Kind,
		TypeParameters: base.TypeParameters,
		TypeArguments:  []typeinfo.Type{argument},
	}
	ctx.typeInstances[identity] = namedTypeInstance{base: base, typ: cached, complete: true}
	node := &ast.AppliedType{
		Name: &ast.Ident{Name: "Box"},
		TypeArgs: []ast.TypeExpr{
			&ast.NamedType{Name: "i32"},
		},
	}

	if got := QueryType(ctx, module, node, TypeContext{}); got.Status != TypeQueryAvailable || got.Type != cached {
		t.Fatalf("cached query result = %#v, want available type %p", got, cached)
	}
	if ctx.Diagnostics.HasErrors() {
		t.Fatalf("query emitted diagnostics: %s", ctx.Diagnostics.EmitAllToString())
	}
}

func TestResolveTypeCreatesGenericInstance(t *testing.T) {
	ctx, module, base := genericQueryContext(t)
	ctx.RegisterTypeDeclaration(module, &ast.StructDecl{
		Name: &ast.Ident{Name: "Box"},
		Type: &ast.StructType{},
	}, base)
	node := &ast.AppliedType{
		Name: &ast.Ident{Name: "Box"},
		TypeArgs: []ast.TypeExpr{
			&ast.NamedType{Name: "i32"},
		},
	}

	got := ResolveType(ctx, module, node, TypeContext{})
	if typeinfo.IsInvalid(got) {
		t.Fatalf("source type resolution returned invalid type: %#v", got)
	}
	if len(ctx.typeInstances) != 1 {
		t.Fatalf("source resolution created %d generic instances, want 1", len(ctx.typeInstances))
	}
	symbol, found := module.ModuleScope.LookupLocal("Box")
	if !found || symbol == nil || !symbol.IsUsed() {
		t.Fatal("source resolution did not mark generic type symbol used")
	}
	if ctx.Diagnostics.HasErrors() {
		t.Fatalf("source resolution emitted diagnostics: %s", ctx.Diagnostics.EmitAllToString())
	}
}

func genericQueryContext(t *testing.T) (*CompilerContext, *Module, *typeinfo.DefinedType) {
	t.Helper()
	ctx := New(".", ".peep", diagnostics.NewDiagnosticBag())
	module := &Module{
		ID:          moduleid.ID{Origin: string(ModuleOriginLocal), ImportPath: "main"},
		ModuleScope: symbols.NewScope(nil),
	}
	base := &typeinfo.DefinedType{
		Name: "Box", Identity: "main::Box", Kind: typeinfo.DefinedKindStruct,
		TypeParameters: []*typeinfo.TypeParameterType{{Name: "T", OwnerIdentity: "main::Box", Index: 0}},
	}
	symbol := symbols.New("Box", symbols.SymbolType, nil, nil)
	symbol.BindType(base)
	if err := module.ModuleScope.Declare(symbol); err != nil {
		t.Fatalf("declare generic type: %v", err)
	}
	return ctx, module, base
}
