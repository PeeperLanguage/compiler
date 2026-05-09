package ast

import "fmt"

func DeclSummary(decl Decl) string {
	if decl == nil {
		return "<nil decl>"
	}
	return fmt.Sprintf("%T", decl)
}
