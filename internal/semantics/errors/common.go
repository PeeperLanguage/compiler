package semantic_errors

import (
	"compiler/internal/diagnostics"
	"compiler/internal/project"
	"compiler/internal/semantics/table"
	"compiler/internal/source"
)

func RedeclarationError(ctx *project.CompilerContext, scope *table.Scope, err string, name string, loc *source.Location) *diagnostics.Diagnostic {
	oldSym, _ := scope.LookupLocal(name)
	ctx.Diagnostics.AddError(diagnostics.ErrRedeclaredSymbol, err, loc, "redeclared here").
		WithSecondaryLabel(oldSym.Location, "first declared here")
	return nil
}