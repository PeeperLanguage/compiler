package ast

func ImportPathFromDecl(imp *ImportDecl) (string, bool) {
	if imp == nil || imp.Path == nil {
		return "", false
	}
	switch node := imp.Path.(type) {
	case *StringLit:
		return node.Value, true
	case *Ident:
		return node.Name, true
	default:
		return "", false
	}
}
