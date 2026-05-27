package common

import (
	"compiler/core/diagnostics"
	"compiler/core/source"
	"compiler/internal/analysis/semantics/typeinfo"
	"compiler/internal/frontend/ast"
)

func AddError(diag *diagnostics.DiagnosticBag, filePath string, node ast.Node, code, msg string) {
	if diag == nil || node == nil {
		return
	}
	loc := node.Loc()
	start := source.NewPosition()
	end := source.NewPosition()
	if loc.Start != nil {
		start = *loc.Start
	}
	if loc.End != nil {
		end = *loc.End
	}
	span := source.NewLocation(filePath, start, end)
	diag.Add(diagnostics.NewError(msg).WithCode(code).WithPrimaryLabel(span, msg))
}

func IsI32Type(typ ast.TypeExpr) bool {
	return typeinfo.IsI32(typeinfo.TypeFromSyntax(typ))
}
