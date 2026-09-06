package hir

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"compiler/pkg/typednil"
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

// validateStmt checks what a child traversal cannot see: an empty slot is
// invisible to a walk that skips nils. It enumerates statement kinds with an
// explicit switch rather than forEachChild so nil slots and required bodies are
// reported with their role; a new statement kind with children must extend this
// switch to keep that reporting.
func validateStmt(fn string, stmt Stmt) []string {
	problems := make([]string, 0)
	if typednil.IsNil(stmt) {
		return append(problems, fmt.Sprintf("function %s holds a nil statement", fn))
	}
	switch node := stmt.(type) {
	case *Block:
		for index, child := range node.Stmts {
			if typednil.IsNil(child) {
				problems = append(problems, fmt.Sprintf("function %s holds a nil statement at block index %d", fn, index))
				continue
			}
			problems = append(problems, validateStmt(fn, child)...)
		}
	case *If:
		problems = append(problems, validateBody(fn, "if", node.Then)...)
		if !typednil.IsNil(node.Else) {
			problems = append(problems, validateStmt(fn, node.Else)...)
		}
	case *For:
		// Init, Bindings and Next are optional; a loop with no body is not.
		problems = append(problems, validateBody(fn, "loop", node.Body)...)
		for _, part := range []*Block{node.Init, node.Bindings, node.Next} {
			if part != nil {
				problems = append(problems, validateStmt(fn, part)...)
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
