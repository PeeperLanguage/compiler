package typeresolution

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/module"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/types"
	"compiler/internal/target"
)

type testModules map[moduleid.ID]*module.Module

func (m testModules) ModuleByID(id moduleid.ID) (*module.Module, bool) {
	mod, ok := m[id]
	return mod, ok
}

func TestTypeArgumentIdentityUsesCanonicalParameterKey(t *testing.T) {
	parameter := &types.TypeParameterType{Name: "T", OwnerIdentity: "pkg::Box", Index: 1}
	if got, want := typeArgumentIdentity(parameter), types.SemanticKey(parameter); got != want {
		t.Fatalf("parameter identity = %q, want semantic key %q", got, want)
	}
}

func TestTypeArgumentIdentityKeepsNominalDefinedIdentity(t *testing.T) {
	defined := &types.DefinedType{Name: "Box", Identity: "pkg::Box", Kind: types.DefinedKindStruct}
	if got, want := typeArgumentIdentity(defined), "defined:pkg::Box"; got != want {
		t.Fatalf("defined identity = %q, want %q", got, want)
	}
}

func TestQueryTypeQualifiedSyntaxWithoutModuleDoesNotPanic(t *testing.T) {
	node := &ast.ScopeResolution{Segments: []ast.PathSegment{
		{Name: &ast.Ident{Name: "dep"}},
		{Name: &ast.Ident{Name: "Thing"}},
	}}

	got := New(target.Host(), nil).Query(nil, node, Context{})
	if got.Status != QueryAvailable || got.Type == nil || types.TypeText(got.Type) != "dep::Thing" {
		t.Fatalf("qualified query result = %#v, want available unresolved qualified name", got)
	}
}

func TestQueryTypeKeepsInvalidGenericApplicationDistinctFromLoading(t *testing.T) {
	resolver, _, mod, _ := genericQueryContext(t)
	node := &ast.NamedType{Name: "Box"}

	got := resolver.Query(mod, node, Context{})
	if got.Status != QueryInvalid || !types.IsInvalid(got.Type) {
		t.Fatalf("invalid generic query result = %#v, want invalid", got)
	}
}

func TestQueryTypeDoesNotCreateOrDiagnoseGenericInstance(t *testing.T) {
	resolver, diag, mod, _ := genericQueryContext(t)
	node := &ast.AppliedType{
		Name: &ast.Ident{Name: "Box"},
		TypeArgs: []ast.TypeExpr{
			&ast.NamedType{Name: "i32"},
		},
	}

	got := resolver.Query(mod, node, Context{})
	symbol, found := mod.ModuleScope.LookupLocal("Box")
	if !found || symbol == nil {
		t.Fatal("generic type symbol missing")
	}
	if mod.SymbolIndex.IsUsed(symbol) {
		t.Fatal("query marked generic type symbol used")
	}
	if got.Status != QueryLoading || got.Type != nil {
		t.Fatalf("uncached query result = %#v, want loading without type", got)
	}
	if len(resolver.instances) != 0 {
		t.Fatalf("query created %d generic instances", len(resolver.instances))
	}
	if diag.HasErrors() {
		t.Fatalf("query emitted diagnostics: %s", diag.EmitAllToString())
	}
}

func TestQueryTypeDoesNotReturnIncompleteCachedGenericInstance(t *testing.T) {
	resolver, _, mod, base := genericQueryContext(t)
	argument := &types.IntegerType{IsSigned: true, Bits: 32}
	identity := typeInstanceIdentity(base, []types.Type{argument})
	resolver.instances[identity] = namedTypeInstance{
		base: base,
		typ: &types.DefinedType{
			Name:           base.Name,
			Identity:       identity,
			Kind:           base.Kind,
			TypeParameters: base.TypeParameters,
			TypeArguments:  []types.Type{argument},
		},
		ready: make(chan struct{}),
	}
	node := &ast.AppliedType{
		Name: &ast.Ident{Name: "Box"},
		TypeArgs: []ast.TypeExpr{
			&ast.NamedType{Name: "i32"},
		},
	}

	if got := resolver.Query(mod, node, Context{}); got.Status != QueryLoading || got.Type != nil {
		t.Fatalf("incomplete cached query result = %#v, want loading without type", got)
	}
}

func TestQueryTypeReturnsCompleteCachedGenericInstance(t *testing.T) {
	resolver, diag, mod, base := genericQueryContext(t)
	argument := &types.IntegerType{IsSigned: true, Bits: 32}
	identity := typeInstanceIdentity(base, []types.Type{argument})
	cached := &types.DefinedType{
		Name:           base.Name,
		Identity:       identity,
		Kind:           base.Kind,
		TypeParameters: base.TypeParameters,
		TypeArguments:  []types.Type{argument},
	}
	resolver.instances[identity] = namedTypeInstance{base: base, typ: cached, isComplete: true}
	node := &ast.AppliedType{
		Name: &ast.Ident{Name: "Box"},
		TypeArgs: []ast.TypeExpr{
			&ast.NamedType{Name: "i32"},
		},
	}

	if got := resolver.Query(mod, node, Context{}); got.Status != QueryAvailable || got.Type != cached {
		t.Fatalf("cached query result = %#v, want available type %p", got, cached)
	}
	if diag.HasErrors() {
		t.Fatalf("query emitted diagnostics: %s", diag.EmitAllToString())
	}
}

func TestResolveTypeCreatesGenericInstance(t *testing.T) {
	resolver, diag, mod, base := genericQueryContext(t)
	resolver.RegisterTypeDeclaration(mod, &ast.StructDecl{
		Name: &ast.Ident{Name: "Box"},
		Type: &ast.StructType{},
	}, base)
	node := &ast.AppliedType{
		Name: &ast.Ident{Name: "Box"},
		TypeArgs: []ast.TypeExpr{
			&ast.NamedType{Name: "i32"},
		},
	}

	got := resolver.Resolve(diag, mod, node, Context{})
	if types.IsInvalid(got) {
		t.Fatalf("source type resolution returned invalid type: %#v", got)
	}
	if len(resolver.instances) != 1 {
		t.Fatalf("source resolution created %d generic instances, want 1", len(resolver.instances))
	}
	symbol, found := mod.ModuleScope.LookupLocal("Box")
	if !found || symbol == nil || !mod.SymbolIndex.IsUsed(symbol) {
		t.Fatal("source resolution did not mark generic type symbol used")
	}
	if diag.HasErrors() {
		t.Fatalf("source resolution emitted diagnostics: %s", diag.EmitAllToString())
	}
}

func TestResetModulePurgesOnlyOwnedNamedTypeInstances(t *testing.T) {
	resolver := New(target.Host(), nil)
	ownerID := moduleid.ID{Origin: "local", ImportPath: "owner"}
	otherID := moduleid.ID{Origin: "local", ImportPath: "other"}
	ownerBox := moduleid.TypeIdentity(ownerID, "Box") + "<i32>"
	otherBox := moduleid.TypeIdentity(otherID, "Box") + "<i32>"
	resolver.instances[ownerBox] = namedTypeInstance{typ: &types.DefinedType{Name: "Box", Identity: ownerBox}}
	resolver.instances[otherBox] = namedTypeInstance{typ: &types.DefinedType{Name: "Box", Identity: otherBox}}

	resolver.ResetModule(&module.Module{ID: ownerID})

	if _, found := resolver.instances[ownerBox]; found {
		t.Fatal("reset retained instance owned by reset module")
	}
	if _, found := resolver.instances[otherBox]; !found {
		t.Fatal("reset removed instance owned by another module")
	}
}

func genericQueryContext(t *testing.T) (*Resolver, *diagnostics.DiagnosticBag, *module.Module, *types.DefinedType) {
	t.Helper()
	diag := diagnostics.NewDiagnosticBag()
	mod := &module.Module{
		ID:          moduleid.ID{Origin: "local", ImportPath: "main"},
		ModuleScope: symbols.NewScope(nil),
		SymbolIndex: symbols.NewIndex(),
	}
	identity := mod.TypeDeclarationIdentity("Box")
	base := &types.DefinedType{
		Name: "Box", Identity: identity, Kind: types.DefinedKindStruct,
		TypeParameters: []*types.TypeParameterType{{Name: "T", OwnerIdentity: identity, Index: 0}},
	}
	symbol := symbols.New(symbols.ProjectedSymbolID(symbols.SymbolType, "Box"), "Box", symbols.SymbolType, nil, nil)
	symbol.BindType(base)
	if err := mod.ModuleScope.Declare(symbol); err != nil {
		t.Fatalf("declare generic type: %v", err)
	}
	modules := testModules{mod.ID: mod}
	return New(target.Host(), modules), diag, mod, base
}

func TestTypeOwnerReadsTheDeclaringModuleFromTheIdentity(t *testing.T) {
	resolver, _, mod, base := genericQueryContext(t)
	unregistered := moduleid.ID{Origin: "local", ImportPath: "elsewhere"}
	for _, test := range []struct {
		name     string
		identity string
		want     *module.Module
	}{
		{"declaration", base.Identity, mod},
		{"generic instance", base.Identity + "<i32>", mod},
		{"module that is not registered", moduleid.TypeIdentity(unregistered, "Box"), nil},
		{"identity of no module", "Box", nil},
		{"empty identity", "", nil},
	} {
		if got := resolver.typeOwner(test.identity); got != test.want {
			t.Errorf("%s: owner = %p, want %p", test.name, got, test.want)
		}
	}
}
