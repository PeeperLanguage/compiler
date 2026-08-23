package problems

import (
	"fmt"

	"compiler/internal/diagnostics"
	"compiler/internal/semantics/symbols"
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

func UnreachableCode(loc *source.Location) *diagnostics.Diagnostic {
	return diagnostics.NewWarning("unreachable code").
		WithCode(diagnostics.WarnUnreachableCode).
		WithPrimaryLabel(loc, "this code is unreachable").
		WithHelp("remove this code or restructure control flow")
}

func Redeclaration(message string, current, previous *source.Location) *diagnostics.Diagnostic {
	d := diagnostics.NewError(message).
		WithCode(diagnostics.ErrRedeclaredSymbol).
		WithPrimaryLabel(current, "redeclared here")
	if previous != nil {
		d.WithSecondaryLabel(previous, "first declared here")
	}
	return d
}

func ReportRedeclaration(diag *diagnostics.DiagnosticBag, scope *symbols.Scope, err string, name string, loc *source.Location) {
	if diag == nil {
		return
	}
	var previous *source.Location
	if scope != nil {
		oldSym, _ := scope.LookupLocal(name)
		if oldSym != nil {
			previous = oldSym.Location
		}
	}
	diag.Add(Redeclaration(err, loc, previous))
}
