package problems

import (
	"compiler/internal/diagnostics"
	"compiler/internal/project"
	"compiler/internal/semantics/table"
	"compiler/internal/source"
)

func ReportRedeclaration(ctx *project.CompilerContext, scope *table.Scope, err string, name string, loc *source.Location) {
	if ctx == nil || ctx.Diagnostics == nil {
		return
	}
	d := ctx.Diagnostics.AddError(diagnostics.ErrRedeclaredSymbol, err, loc, "redeclared here")
	if scope == nil {
		return
	}
	oldSym, _ := scope.LookupLocal(name)
	if oldSym != nil && oldSym.Location != nil {
		d.WithSecondaryLabel(oldSym.Location, "first declared here")
	}
}
