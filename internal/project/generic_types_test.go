package project

import (
	"testing"

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
