package typechecker

import (
	"fmt"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
)

func invalidExpressionError(node ast.Node, message string) *diagnostics.Diagnostic {
	return diagnostics.NewError(message).
		WithPrimaryLabel(ast.LocOf(node), "").
		WithCode(diagnostics.ErrInvalidExpression)
}

func explicitBoolCastRequiredError(expr ast.Expr, message string) *diagnostics.Diagnostic {
	e := diagnostics.NewError(message).
		WithPrimaryLabel(ast.LocOf(expr), "").
		WithCode(diagnostics.ErrInvalidOperation).
		WithNote("condition is a boolean type. It either can be `true` or `false`").
		WithHelp("use `as bool` for explicit conversion")
	return e
}

func invalidOperationError(node ast.Node, message string) *diagnostics.Diagnostic {
	return diagnostics.NewError(message).
		WithPrimaryLabel(ast.LocOf(node), "").
		WithCode(diagnostics.ErrInvalidOperation)
}

func invalidTypeError(node ast.Node, message string) *diagnostics.Diagnostic {
	return diagnostics.NewError(message).
		WithPrimaryLabel(ast.LocOf(node), "").
		WithCode(diagnostics.ErrInvalidType)
}

func typeMismatchError(node ast.Node, message string) *diagnostics.Diagnostic {
	return diagnostics.NewError(message).
		WithPrimaryLabel(ast.LocOf(node), "").
		WithCode(diagnostics.ErrTypeMismatch)
}

func notCallableError(node ast.Node, message string) *diagnostics.Diagnostic {
	return diagnostics.NewError(message).
		WithPrimaryLabel(ast.LocOf(node), "").
		WithCode(diagnostics.ErrNotCallable)
}

func wrongArgumentCountError(node ast.Node, got, want int) *diagnostics.Diagnostic {
	return diagnostics.NewError(fmt.Sprintf("wrong number of arguments: got %d, want %d", got, want)).
		WithPrimaryLabel(ast.LocOf(node), "").
		WithCode(diagnostics.ErrWrongArgumentCount)
}
