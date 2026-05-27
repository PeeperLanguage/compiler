package cfg

import (
	"compiler/colors"
	"compiler/core/diagnostics"
	"compiler/core/source"
	"compiler/internal/ir/hir"
	"slices"
)

func mustReturnBlock(b *hir.Block) bool {
	if b == nil {
		return false
	}
	return slices.ContainsFunc(b.Stmts, mustReturnStmt)
}

func mustReturnStmt(s hir.Stmt) bool {
	switch n := s.(type) {
	case *hir.Return:
		return true
	case *hir.Block:
		return mustReturnBlock(n)
	case *hir.If:
		if n.Else == nil {
			return false
		}
		return mustReturnBlock(n.Then) && mustReturnStmt(n.Else)
	default:
		return false
	}
}

func reportMissingReturn(fn *hir.Function, diag *diagnostics.DiagnosticBag) {
	if fn == nil || diag == nil {
		return
	}
	msg := "not all control paths return a value"
	d := diagnostics.NewError(msg).WithCode(diagnostics.ErrMissingReturn)

	missing := collectMissingReturns(fn.Body)
	if len(missing) > 0 && missing[0] != nil {
		d.WithPrimaryLabel(missing[0], msg)
	} else {
		d.WithPrimaryLabel(fn.Location, msg)
	}
	d.WithSecondaryLabel(fn.Location, "expected `"+fn.ReturnType+"` because of this return type")

	for _, loc := range missing {
		if loc == nil {
			continue
		}
		l := loc
		d.WithSecondaryLabel(l, "this branch does not return a value")
	}
	d.WithText("note", "some branch completes without a `return`, execution can fall off end of function", colors.CYAN)
	d.WithHelp("fulfill the return or add a fallback return on parent scope")
	diag.Add(d)
}

// collectMissingReturns returns minimal set of locations for branches that can fall through.
// Strategy:
// - only examine "tail position" of blocks (where fallthrough possible)
// - descend into nested if-chains to pinpoint innermost missing branch
func collectMissingReturns(b *hir.Block) []*source.Location {
	if b == nil {
		return nil
	}
	if len(b.Stmts) == 0 {
		return []*source.Location{b.Location}
	}
	if slices.ContainsFunc(b.Stmts, mustReturnStmt) {
		return nil
	}
	last := b.Stmts[len(b.Stmts)-1]
	return collectMissingStmt(last, b.Location)
}

func collectMissingStmt(s hir.Stmt, fallback *source.Location) []*source.Location {
	switch n := s.(type) {
	case *hir.If:
		out := make([]*source.Location, 0)
		if !mustReturnBlock(n.Then) {
			out = append(out, collectMissingReturns(n.Then)...)
			if len(out) == 0 {
				out = append(out, n.Then.Location)
			}
		}
		if n.Else == nil {
			// Missing else path. Point at whole if.
			out = append(out, n.Location)
			return out
		}
		if !mustReturnStmt(n.Else) {
			out = append(out, collectMissingStmt(n.Else, n.Location)...)
			if len(out) == 0 {
				out = append(out, stmtLoc(n.Else, n.Location))
			}
		}
		return out
	case *hir.Block:
		if mustReturnBlock(n) {
			return nil
		}
		return collectMissingReturns(n)
	default:
		// Tail statement not returning. Point at enclosing block tail.
		return []*source.Location{fallback}
	}
}

func stmtLoc(s hir.Stmt, fallback *source.Location) *source.Location {
	if s == nil {
		return fallback
	}
	switch n := s.(type) {
	case *hir.Block:
		return n.Location
	case *hir.If:
		return n.Location
	case *hir.Return:
		return n.Location
	default:
		return fallback
	}
}
