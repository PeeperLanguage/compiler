package module

import (
	"testing"

	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/typeinfo"
)

func TestTypeDeclarationIdentitiesAreStable(t *testing.T) {
	mod := &Module{}
	base := &typeinfo.DefinedType{Identity: "test::Type", Kind: typeinfo.DefinedKindStruct}
	declaration := TypeDeclaration{Syntax: &ast.StructDecl{}, Base: base}
	for _, identity := range []string{"test::Zed", "test::Alpha", "test::Middle"} {
		mod.RecordTypeDeclaration(identity, declaration)
	}

	got := mod.TypeDeclarationIdentities()
	want := []string{"test::Alpha", "test::Middle", "test::Zed"}
	if len(got) != len(want) {
		t.Fatalf("identities = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("identities = %v, want %v", got, want)
		}
	}
}
