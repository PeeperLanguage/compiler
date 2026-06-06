package common

import (
	"compiler/core/diagnostics"
	"compiler/core/source"
	"compiler/internal/analysis/semantics/symbols"
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

func AddWarning(diag *diagnostics.DiagnosticBag, sym *symbols.Symbol, code, msg string, labels ...diagnostics.Label) {
	if diag == nil || sym == nil {
		return
	}
	d := diagnostics.NewWarning(msg).WithCode(code)
	if sym.Location != nil {
		d.WithPrimaryLabel(sym.Location, msg)
		for _, label := range labels {
			d.WithLabel(label.Location, label.Message, label.Style)
		}
	}
	diag.Add(d)
}

