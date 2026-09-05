package hir

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

const maxReportedProblems = 10

// Validate checks the shape of a lowered function body: that a function has one,
// that no statement slot is empty, and that a construct carrying a body actually
// has it.
//
// It does not re-derive meaning. Whether the right statement was lowered for a
// construct is lowering's decision; that every statement kind is handled at all
// is held by the dispatch contract in internal/contracts. What is left is the
// shape, which nothing else checks and which the backend otherwise discovers by
// dereferencing a nil.
//
// A failure is a compiler bug, not a source error.
func (m *Module) Validate() error {
	if m == nil || len(m.Funcs) == 0 {
		return nil
	}
	problems := make([]string, 0)
	for _, fn := range m.Funcs {
		if fn == nil {
			problems = append(problems, "module holds a nil function")
			continue
		}
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

// validateStmt walks through the canonical child traversal rather than a switch
// of its own, so a new statement kind is covered here the moment it declares its
// children. What it adds is the checks a traversal cannot make: an empty slot is
// invisible to a walk that skips nils.
func validateStmt(fn string, stmt Stmt) []string {
	problems := make([]string, 0)
	if stmt == nil {
		return append(problems, fmt.Sprintf("function %s holds a nil statement", fn))
	}
	switch node := stmt.(type) {
	case *Block:
		for index, child := range node.Stmts {
			if child == nil {
				problems = append(problems, fmt.Sprintf("function %s holds a nil statement at block index %d", fn, index))
				continue
			}
			problems = append(problems, validateStmt(fn, child)...)
		}
	case *If:
		problems = append(problems, validateBody(fn, "if", node.Then)...)
		if node.Else != nil {
			problems = append(problems, validateStmt(fn, node.Else)...)
		}
	case *For:
		// Init, Bindings and Next are optional; a loop with no body is not.
		problems = append(problems, validateBody(fn, "loop", node.Body)...)
		for role, part := range map[string]*Block{"loop init": node.Init, "loop bindings": node.Bindings, "loop next": node.Next} {
			if part != nil {
				problems = append(problems, validateStmt(fn, part)...)
				_ = role
			}
		}
	case *SwitchVariant:
		for _, arm := range node.Cases {
			problems = append(problems, validateBody(fn, fmt.Sprintf("case %d", arm.Case), arm.Body)...)
		}
	}
	return problems
}

func validateBody(fn, role string, body *Block) []string {
	if body == nil {
		return []string{fmt.Sprintf("function %s has a %s with no body", fn, role)}
	}
	return validateStmt(fn, body)
}
