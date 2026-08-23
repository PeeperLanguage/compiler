package ast

import "testing"

func TestEnumVariantMemberSplitsLocalAndImportedPaths(t *testing.T) {
	tests := []struct {
		name     string
		path     *ScopeResolution
		wantType string
		wantCase string
	}{
		{
			name: "local generic",
			path: &ScopeResolution{Segments: []PathSegment{
				{Name: &Ident{Name: "Result"}, TypeArgs: []TypeExpr{&NamedType{Name: "i32"}}},
				{Name: &Ident{Name: "Ok"}},
			}},
			wantType: "Result<i32>",
			wantCase: "Ok",
		},
		{
			name: "imported generic",
			path: &ScopeResolution{Segments: []PathSegment{
				{Name: &Ident{Name: "result"}},
				{Name: &Ident{Name: "Result"}, TypeArgs: []TypeExpr{&NamedType{Name: "i32"}}},
				{Name: &Ident{Name: "Pending"}},
			}},
			wantType: "result::Result<i32>",
			wantCase: "Pending",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			typePath, variant, ok := test.path.EnumVariantMember()
			if !ok || TypeText(typePath) != test.wantType || variant == nil || variant.Name != test.wantCase {
				t.Fatalf("split = (%s, %#v, %v), want (%s, %s, true)", TypeText(typePath), variant, ok, test.wantType, test.wantCase)
			}
		})
	}

	invalid := &ScopeResolution{Segments: []PathSegment{
		{Name: &Ident{Name: "Result"}},
		{Name: &Ident{Name: "Ok"}, TypeArgs: []TypeExpr{&NamedType{Name: "i32"}}},
	}}
	if _, _, ok := invalid.EnumVariantMember(); ok {
		t.Fatal("type arguments on variant segment must not form variant member path")
	}
}
