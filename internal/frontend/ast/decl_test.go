package ast

import "testing"

func TestNamedTypeDeclarationsImplementTypeDecl(t *testing.T) {
	tests := []struct {
		name string
		decl TypeDecl
	}{
		{
			name: "alias",
			decl: &TypeAliasDecl{
				Name: &Ident{Name: "Alias"},
				Type: &NamedType{Name: "i32"},
			},
		},
		{
			name: "struct",
			decl: &StructDecl{
				Name: &Ident{Name: "Point"},
				Type: &StructType{Fields: []TypeField{{Name: &Ident{Name: "x"}, Type: &NamedType{Name: "i32"}}}},
			},
		},
		{
			name: "interface",
			decl: &InterfaceDecl{
				Name: &Ident{Name: "Reader"},
				Type: &InterfaceType{Methods: []TypeMethod{{Name: &Ident{Name: "read"}}}},
			},
		},
		{
			name: "enum",
			decl: &EnumDecl{
				Name: &Ident{Name: "Color"},
				Type: &EnumType{Variants: []EnumVariant{{Name: &Ident{Name: "Red"}}}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, typ := tt.decl.DeclName(), tt.decl.UnderlyingType()
			if name == nil || name.Name == "" {
				t.Fatalf("expected declaration name")
			}
			if typ == nil {
				t.Fatalf("expected synthesized type expr")
			}
		})
	}
}

func TestNonTypeDeclarationsDoNotImplementTypeDecl(t *testing.T) {
	tests := []struct {
		name string
		decl Decl
	}{
		{name: "import", decl: &ImportDecl{}},
		{name: "let", decl: &LetDecl{}},
		{name: "const", decl: &ConstDecl{}},
		{name: "fn", decl: &FnDecl{}},
		{name: "bad", decl: &BadDecl{}},
		{name: "impl", decl: &ImplDecl{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := tt.decl.(TypeDecl); ok {
				t.Fatalf("did not expect %T to implement TypeDecl", tt.decl)
			}
		})
	}
}
