package hir

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"compiler/internal/ir"
	"compiler/pkg/typednil"
)

const maxReportedProblems = 10

// Validate checks the shape of lowered functions and expressions: required
// bodies and statement slots must exist, signatures and value expressions must
// be typed, and explicit invalid nodes must not survive clean-source lowering.
//
// It does not re-derive meaning. Whether the right statement was lowered for a
// construct is lowering's decision; that every statement kind is handled at all
// is held by the dispatch contract in internal/contracts. What is left is the
// shape and evidence consistency, which nothing else checks before MIR/backend.
//
// A failure is a compiler bug, not a source error.
func (m *Module) Validate() error {
	if m == nil || (len(m.Funcs) == 0 && len(m.Externs) == 0) {
		return nil
	}
	problems := make([]string, 0)
	for _, external := range m.Externs {
		problems = append(problems, validateSignature("extern", external.Name, external.Params, external.ReturnType)...)
	}
	for _, fn := range m.Funcs {
		if fn == nil {
			problems = append(problems, "module holds a nil function")
			continue
		}
		problems = append(problems, validateSignature("function", fn.Name, fn.Params, fn.ReturnType)...)
		if fn.Body == nil {
			problems = append(problems, fmt.Sprintf("function %s has no body", fn.Name))
			continue
		}
		problems = append(problems, validateStmt(fn.Name, fn.Body)...)
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	if len(problems) > maxReportedProblems {
		return fmt.Errorf("%s (%d more)", strings.Join(problems[:maxReportedProblems], "; "), len(problems)-maxReportedProblems)
	}
	return errors.New(strings.Join(problems, "; "))
}

type validator struct {
	function string
	problems []string
}

func validateStmt(fn string, stmt Stmt) []string {
	if typednil.IsNil(stmt) {
		return []string{fmt.Sprintf("function %s holds a nil statement", fn)}
	}
	validation := validator{function: fn}
	InspectStmt(stmt, func(stmt Stmt) bool {
		stmt.validateSelf(&validation)
		return true
	})
	return validation.problems
}

func (node *Block) validateSelf(validation *validator) {
	for index, child := range node.Stmts {
		if typednil.IsNil(child) {
			validation.problems = append(validation.problems, fmt.Sprintf("function %s holds a nil statement at block index %d", validation.function, index))
		}
	}
}

func (node *Binding) validateSelf(validation *validator) {
	if node.Type == ir.InvalidType {
		validation.problems = append(validation.problems, fmt.Sprintf("function %s has binding %s with invalid type", validation.function, node.Name))
	}
	if node.Value != nil {
		validation.problems = append(validation.problems, validateExpr(validation.function, "binding value", node.Value)...)
	}
}

func (node *ExprStmt) validateSelf(validation *validator) {
	validation.problems = append(validation.problems, validateExpr(validation.function, "expression statement", node.Value)...)
}

func (node *Assign) validateSelf(validation *validator) {
	if node.Target == nil {
		validation.problems = append(validation.problems, fmt.Sprintf("function %s has assignment with no target", validation.function))
	} else {
		validation.problems = append(validation.problems, validatePlaceShape(validation.function, "assignment target", node.Target)...)
		ir.InspectPlace(node.Target, func(expr ir.Expr) bool {
			validation.problems = append(validation.problems, validateExpr(validation.function, "assignment target", expr)...)
			return false
		})
	}
	validation.problems = append(validation.problems, validateExpr(validation.function, "assignment value", node.Value)...)
}

func (node *Invalid) validateSelf(validation *validator) {
	validation.problems = append(validation.problems, fmt.Sprintf("function %s contains invalid statement: %s", validation.function, node.Message))
}

func (node *Return) validateSelf(validation *validator) {
	if node.Value != nil {
		validation.problems = append(validation.problems, validateExpr(validation.function, "return value", node.Value)...)
	}
}

func (node *If) validateSelf(validation *validator) {
	validation.problems = append(validation.problems, validateExpr(validation.function, "if condition", node.Cond)...)
	if typednil.IsNil(node.Then) {
		validation.problems = append(validation.problems, fmt.Sprintf("function %s has a if with no body", validation.function))
	}
}

func (node *For) validateSelf(validation *validator) {
	if node.Cond != nil {
		validation.problems = append(validation.problems, validateExpr(validation.function, "loop condition", node.Cond)...)
	}
	if typednil.IsNil(node.Body) {
		validation.problems = append(validation.problems, fmt.Sprintf("function %s has a loop with no body", validation.function))
	}
}

func (node *SwitchVariant) validateSelf(validation *validator) {
	validation.problems = append(validation.problems, validateExpr(validation.function, "variant subject", node.Value)...)
	for _, arm := range node.Cases {
		if len(arm.Bindings) > 0 && arm.PayloadType == ir.InvalidType {
			validation.problems = append(validation.problems, fmt.Sprintf("function %s case %d has bindings with invalid payload type", validation.function, arm.Case))
		}
		for index, binding := range arm.Bindings {
			if binding.Type == ir.InvalidType {
				validation.problems = append(validation.problems, fmt.Sprintf("function %s case %d has binding %d with invalid type", validation.function, arm.Case, index))
			}
		}
		if typednil.IsNil(arm.Body) {
			validation.problems = append(validation.problems, fmt.Sprintf("function %s has a case %d with no body", validation.function, arm.Case))
		}
	}
}

func validateSignature(kind, name string, params []ir.Param, returnType ir.TypeID) []string {
	problems := make([]string, 0)
	if returnType == ir.InvalidType {
		problems = append(problems, fmt.Sprintf("%s %s has invalid return type", kind, name))
	}
	for index, param := range params {
		if param.Type == ir.InvalidType {
			problems = append(problems, fmt.Sprintf("%s %s has parameter %d with invalid type", kind, name, index))
		}
	}
	return problems
}

func validateExpr(fn, role string, root ir.Expr) []string {
	if typednil.IsNil(root) {
		return []string{fmt.Sprintf("function %s has %s with no expression", fn, role)}
	}
	problems := make([]string, 0)
	ir.InspectExpr(root, func(expr ir.Expr) bool {
		if typednil.IsNil(expr) {
			problems = append(problems, fmt.Sprintf("function %s has nil expression in %s", fn, role))
			return false
		}
		if invalid, ok := expr.(*ir.InvalidExpr); ok {
			problems = append(problems, fmt.Sprintf("function %s has invalid %s: %s", fn, role, invalid.Message))
			return false
		}
		switch value := expr.(type) {
		case *ir.Print, *ir.Drop:
			return true
		case *ir.Load:
			problems = append(problems, validatePlaceShape(fn, role, value.Place)...)
		case *ir.AddrOf:
			problems = append(problems, validatePlaceShape(fn, role, value.Place)...)
		case *ir.SliceView:
			problems = append(problems, validatePlaceShape(fn, role, value.Place)...)
		case *ir.Unary:
			if value.Type == ir.InvalidType {
				problems = append(problems, fmt.Sprintf("function %s has %s with invalid type", fn, role))
			}
			return true
		}
		if expr.TypeID() == ir.InvalidType {
			problems = append(problems, fmt.Sprintf("function %s has %s with invalid type", fn, role))
		}
		return true
	})
	return problems
}

func validatePlaceShape(fn, role string, place *ir.Place) []string {
	if place == nil {
		return []string{fmt.Sprintf("function %s has %s with no place", fn, role)}
	}
	problems := make([]string, 0)
	if place.Type == ir.InvalidType {
		problems = append(problems, fmt.Sprintf("function %s has %s with invalid place type", fn, role))
	}
	if typednil.IsNil(place.Root) {
		problems = append(problems, fmt.Sprintf("function %s has %s with no place root", fn, role))
	}
	for index, projection := range place.Projections {
		switch projection.Kind {
		case ir.PlaceProjectionDeref, ir.PlaceProjectionField, ir.PlaceProjectionIndex, ir.PlaceProjectionVariantPayload:
		default:
			problems = append(problems, fmt.Sprintf("function %s has %s projection %d with unknown kind %d", fn, role, index, projection.Kind))
		}
		if projection.Type == ir.InvalidType {
			problems = append(problems, fmt.Sprintf("function %s has %s projection %d with invalid type", fn, role, index))
		}
		if projection.Kind == ir.PlaceProjectionIndex && typednil.IsNil(projection.Index) {
			problems = append(problems, fmt.Sprintf("function %s has %s index projection %d with no index", fn, role, index))
		}
	}
	return problems
}
