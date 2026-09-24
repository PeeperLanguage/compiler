package typeresolution

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/module"
	"compiler/internal/moduleid"
	"compiler/internal/phase"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/target"
)

type testModules map[moduleid.ID]*module.Module

func (m testModules) ModuleByID(id moduleid.ID) (*module.Module, bool) {
	mod, ok := m[id]
	return mod, ok
}

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

	got := New(target.Host(), nil).Query(nil, node, Context{})
	if got.Status != QueryAvailable || got.Type == nil || got.Type.Text() != "dep::Thing" {
		t.Fatalf("qualified query result = %#v, want available unresolved qualified name", got)
	}
}

func TestQueryTypeKeepsInvalidGenericApplicationDistinctFromLoading(t *testing.T) {
	resolver, _, mod, _ := genericQueryContext(t)
	node := &ast.NamedType{Name: "Box"}

	got := resolver.Query(mod, node, Context{})
	if got.Status != QueryInvalid || !typeinfo.IsInvalid(got.Type) {
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
	if symbol.IsUsed() {
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
	argument := &typeinfo.IntegerType{IsSigned: true, Bits: 32}
	identity := typeInstanceIdentity(base, []typeinfo.Type{argument})
	resolver.instances[identity] = namedTypeInstance{
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

	if got := resolver.Query(mod, node, Context{}); got.Status != QueryLoading || got.Type != nil {
		t.Fatalf("incomplete cached query result = %#v, want loading without type", got)
	}
}

func TestQueryTypeReturnsCompleteCachedGenericInstance(t *testing.T) {
	resolver, diag, mod, base := genericQueryContext(t)
	argument := &typeinfo.IntegerType{IsSigned: true, Bits: 32}
	identity := typeInstanceIdentity(base, []typeinfo.Type{argument})
	cached := &typeinfo.DefinedType{
		Name:           base.Name,
		Identity:       identity,
		Kind:           base.Kind,
		TypeParameters: base.TypeParameters,
		TypeArguments:  []typeinfo.Type{argument},
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
	if typeinfo.IsInvalid(got) {
		t.Fatalf("source type resolution returned invalid type: %#v", got)
	}
	if len(resolver.instances) != 1 {
		t.Fatalf("source resolution created %d generic instances, want 1", len(resolver.instances))
	}
	symbol, found := mod.ModuleScope.LookupLocal("Box")
	if !found || symbol == nil || !symbol.IsUsed() {
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
	resolver.instances["owner::Box<i32>"] = namedTypeInstance{
		ownerModuleID: ownerID,
		typ:           &typeinfo.DefinedType{Name: "Box", Identity: "owner::Box<i32>"},
	}
	resolver.instances["other::Box<i32>"] = namedTypeInstance{
		ownerModuleID: otherID,
		typ:           &typeinfo.DefinedType{Name: "Box", Identity: "other::Box<i32>"},
	}

	resolver.ResetModule(&module.Module{ID: ownerID}, phase.Parsed)

	if _, found := resolver.instances["owner::Box<i32>"]; found {
		t.Fatal("reset retained instance owned by reset module")
	}
	if _, found := resolver.instances["other::Box<i32>"]; !found {
		t.Fatal("reset removed instance owned by another module")
	}
}

func genericQueryContext(t *testing.T) (*Resolver, *diagnostics.DiagnosticBag, *module.Module, *typeinfo.DefinedType) {
	t.Helper()
	diag := diagnostics.NewDiagnosticBag()
	mod := &module.Module{
		ID:          moduleid.ID{Origin: "local", ImportPath: "main"},
		ModuleScope: symbols.NewScope(nil),
	}
	base := &typeinfo.DefinedType{
		Name: "Box", Identity: "main::Box", Kind: typeinfo.DefinedKindStruct,
		TypeParameters: []*typeinfo.TypeParameterType{{Name: "T", OwnerIdentity: "main::Box", Index: 0}},
	}
	symbol := symbols.New("Box", symbols.SymbolType, nil, nil)
	symbol.BindType(base)
	if err := mod.ModuleScope.Declare(symbol); err != nil {
		t.Fatalf("declare generic type: %v", err)
	}
	modules := testModules{mod.ID: mod}
	return New(target.Host(), modules), diag, mod, base
}
