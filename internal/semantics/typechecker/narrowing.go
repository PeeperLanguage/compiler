package typechecker

import (
	"maps"
	"slices"
	"strings"

	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

// Case narrowing: a place proven to hold exactly one case reads as that case's
// payload. Named enums and optionals share this path. The checker follows only
// the block structure (a guarded block, its else, a match arm, and what the
// branches of an if or match agree on where they join) and proposes the
// payload type. Flow analysis verifies every such read against its facts, so
// nothing here has to be right about mutation, calls or loops.

// payloadStep, appended to a narrowedPlace path, names the payload of that
// place; a match arm that tests the payload narrows it too.
const payloadStep = "!"

// narrowedPlace names storage a case test can narrow: a variable and a path of
// fields and indexes (a constant or a variable) from it.
type narrowedPlace struct {
	root *symbols.Symbol
	path string
}

func (c *checker) narrowedPlaceOf(expr ast.Expr) (narrowedPlace, bool) {
	path := ""
	for {
		projection, projected := place.Project(expr)
		if !projected {
			break
		}
		if projection.Step.Kind == place.OriginField {
			path = "." + projection.Step.Field + path
		} else if index, isConstant := c.evidence.ConstantIndex(expr.ID()); isConstant {
			path = "[" + index.Text + "]" + path
		} else if variable, isVariable := projection.Index.(*ast.Ident); isVariable {
			// Flow rejects the read if the index variable changed since the test.
			path = "[" + variable.Name + "]" + path
		} else {
			return narrowedPlace{}, false
		}
		expr = projection.Base
	}
	ident, isIdent := expr.(*ast.Ident)
	if !isIdent {
		return narrowedPlace{}, false
	}
	sym := c.module.SymbolIndex.Symbol(ident)
	if target, isAlias := c.aliases[sym]; isAlias {
		return narrowedPlace{root: target.root, path: target.path + path}, true
	}
	return narrowedPlace{root: sym, path: path}, sym != nil
}

// joinNarrowing returns what holds where control paths meet: a place keeps
// every case some path leaves possible, and is forgotten if a path does not
// track it. A nil path is one control never arrives from.
func joinNarrowing(paths ...map[narrowedPlace][]int) map[narrowedPlace][]int {
	var joined map[narrowedPlace][]int
	for _, path := range paths {
		if path == nil {
			continue
		}
		if joined == nil {
			joined = maps.Clone(path)
			continue
		}
		for tested, possible := range joined {
			other, tracked := path[tested]
			if !tracked {
				delete(joined, tested)
				continue
			}
			joined[tested] = slices.Compact(slices.Sorted(slices.Values(slices.Concat(possible, other))))
		}
	}
	if joined == nil {
		return make(map[narrowedPlace][]int)
	}
	return joined
}

// narrowedBy returns the narrowing in known, restricted by what cond being
// isTrue says about the places it tests. known is left as it was.
func (c *checker) narrowedBy(known map[narrowedPlace][]int, cond ast.Expr, isTrue bool) map[narrowedPlace][]int {
	if not, ok := cond.(*ast.UnaryExpr); ok && not.Op == "!" {
		return c.narrowedBy(known, not.Expr, !isTrue)
	}
	result := maps.Clone(known)
	var tests ast.Expr
	switch test := cond.(type) {
	case *ast.IsExpr:
		tests = test.Value
	case *ast.BinaryExpr:
		// `a && b` being true, or `a || b` being false, settles both operands.
		if test.Op == "&&" && isTrue || test.Op == "||" && !isTrue {
			return c.narrowedBy(c.narrowedBy(known, test.Left, isTrue), test.Right, isTrue)
		}
		tests = test.Left
		if isNoneExpr(tests) {
			tests = test.Right
		}
	default:
		return result
	}
	tested, hasEvidence := c.evidence.CaseTest(cond.ID())
	subject, isPlace := c.narrowedPlaceOf(tests)
	if !hasEvidence || !isPlace {
		return result
	}
	possible, tracked := known[subject]
	if !tracked {
		for caseIndex := range tested.CaseCount {
			possible = append(possible, caseIndex)
		}
	}
	keepsTested := tested.MatchesWhenTrue == isTrue
	result[subject] = slices.DeleteFunc(slices.Clone(possible), func(caseIndex int) bool {
		return (caseIndex == tested.Case) != keepsTested
	})
	return result
}

// casePayload returns the payload type expr reads when it names a place
// narrowed to one case, reading through a payload that is narrowed as well. It
// returns nil where expr is the whole carrier: a test or match subject, an
// assignment target, or a destination typed as the carrier.
func (c *checker) casePayload(expr ast.Expr, carrier, expected typeinfo.Type) typeinfo.Type {
	// Most expressions are checked with nothing narrowed; leave before
	// resolving a place for them.
	if len(c.narrowed) == 0 || expr == c.wholeCarrierExpr {
		return nil
	}
	subject, isPlace := c.narrowedPlaceOf(expr)
	if !isPlace {
		return nil
	}
	var payload typeinfo.Type
	for read := carrier; expected == nil || !typeinfo.IsSameType(read, expected); subject.path += payloadStep {
		possible := c.narrowed[subject]
		descriptor, isVariant := typeinfo.VariantDescriptorOf(read)
		if len(possible) != 1 || !isVariant || descriptor.Cases[possible[0]].Payload == nil {
			break
		}
		read = descriptor.Cases[possible[0]].Payload
		payload = read
	}
	if payload != nil {
		c.evidence.MarkCasePayload(expr.ID(), carrier)
	}
	return payload
}

// forgetNarrowing drops what is known about target and everything stored under
// it, after an assignment that may have changed its case.
func (c *checker) forgetNarrowing(target ast.Expr) {
	assigned, ok := c.narrowedPlaceOf(target)
	if !ok {
		return
	}
	for known := range c.narrowed {
		// A longer path is stored under target only when the next step starts
		// right after it: `.a` covers `.a.b` and `.a[0]`, not `.ab`.
		rest, isUnder := strings.CutPrefix(known.path, assigned.path)
		if known.root == assigned.root && isUnder && (rest == "" || strings.ContainsRune(".[!", rune(rest[0]))) {
			delete(c.narrowed, known)
		}
	}
}

// alwaysLeaves reports whether control never runs past stmt.
func alwaysLeaves(stmt ast.Stmt) bool {
	switch node := stmt.(type) {
	case *ast.ReturnStmt, *ast.BreakStmt, *ast.ContinueStmt:
		return true
	case *ast.BlockStmt:
		return node != nil && len(node.Stmts) > 0 && alwaysLeaves(node.Stmts[len(node.Stmts)-1])
	case *ast.IfStmt:
		return node.Else != nil && alwaysLeaves(node.Then) && alwaysLeaves(node.Else)
	case *ast.MatchStmt:
		return len(node.Arms) > 0 && !slices.ContainsFunc(node.Arms, func(arm *ast.MatchArm) bool {
			return !alwaysLeaves(arm.Body)
		})
	default:
		return false
	}
}
