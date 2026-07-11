package problems

import (
	"fmt"

	"compiler/internal/diagnostics"
	"compiler/internal/project"
	"compiler/internal/semantics/table"
	"compiler/internal/source"
)

func ArrayIndexOutOfBounds(index, length string, loc *source.Location) *diagnostics.Diagnostic {
	msg := fmt.Sprintf("array index out of bounds: index %s for length %s", index, length)
	d := diagnostics.NewError(msg).WithCode(diagnostics.ErrArrayOutOfBounds)
	if loc != nil {
		d.WithPrimaryLabel(loc, msg)
	}
	return d
}

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
