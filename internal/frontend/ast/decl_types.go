package ast

func DeclaredTypeExpr(decl Decl) (*Ident, TypeExpr, bool) {
	switch node := decl.(type) {
	case *TypeAliasDecl:
		return node.Name, node.Type, true
	case *StructDecl:
		return node.Name, &StructType{Fields: node.Fields, Location: node.Location}, true
	case *InterfaceDecl:
		return node.Name, &InterfaceType{Methods: node.Methods, Location: node.Location}, true
	case *EnumDecl:
		return node.Name, &EnumType{Variants: node.Variants, Location: node.Location}, true
	default:
		return nil, nil, false
	}
}
