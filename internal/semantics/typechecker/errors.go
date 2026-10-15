package typechecker

import (
	"fmt"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/source"
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
		Note("condition is a boolean type. It either can be `true` or `false`").
		Help("use `as bool` for explicit conversion")
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

func invalidAttributeError(attr ast.Attribute, message string) *diagnostics.Diagnostic {
	return diagnostics.NewError(message).
		WithPrimaryLabel(attr.Location, "").
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

// endlessLoopWarning suggests the plain `for` for a `for true`, with the edit
// that removes the condition and the space before it.
func endlessLoopWarning(condition *source.Location) *diagnostics.Diagnostic {
	warning := diagnostics.NewWarning("`for true` is an endless loop").
		WithCode(diagnostics.WarnEndlessLoopCondition).
		WithPrimaryLabel(condition, "this condition is always true")
	if condition == nil || condition.Filename == nil || condition.Start == nil || condition.End == nil {
		return warning.Help("write `for` with no condition")
	}
	start := *condition.Start
	if start.Column > 1 {
		start.Column--
		start.Index--
	}
	return warning.Help("write `for` with no condition", diagnostics.Fix.Remove(source.NewLocation(*condition.Filename, start, *condition.End)))
}
