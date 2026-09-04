// Package effect defines the ordered semantic meaning of source constructs.
//
// The producer in this package is the only code that inspects syntax to decide
// what a construct does to a binding. Definite initialization consumes the
// published stream and never switches on an AST kind, so a construct that maps
// onto these operations needs no case in any consumer.
//
// This package shares evidence, not a solver. Each analysis keeps its own
// lattice, join, direction, and diagnostics, per COMPILER_GUIDELINES.md section 6.
package effect

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/semantics/symbols"
	"compiler/internal/source"
)

// Op is one semantic effect on one binding.
//
// The set is closed by the unexported marker method, so no kind can be
// introduced outside this package. Go cannot make a consumer's type switch
// exhaustive; internal/contracts carries that half.
type Op interface {
	effectOp()
}

// Define brings a binding into existence. Initialized separates `let x = e`,
// which also stores a value, from a declaration that leaves storage empty.
type Define struct {
	Symbol *symbols.Symbol
	// Node is the declaration, which is where a diagnostic about the binding
	// itself belongs.
	Node        ast.NodeID
	Initialized bool
}

// Write stores to a binding that already exists.
type Write struct {
	Symbol *symbols.Symbol
	// Node is the assignment target.
	Node ast.NodeID
}

// Use reads a binding's value. Node is the reading identifier rather than the
// enclosing statement, so a diagnostic anchors on the read itself.
//
// Location travels with the operation so a consumer never has to resolve the
// node back to syntax just to report against it. Define and Write carry no
// location because no current diagnostic anchors on them.
type Use struct {
	Symbol   *symbols.Symbol
	Node     ast.NodeID
	Location *source.Location
}

func (Define) effectOp() {}
func (Write) effectOp()  {}
func (Use) effectOp()    {}

// Result holds published effects for one semantic generation.
//
// A cfg.SiteID is only meaningful relative to one graph, so function identity
// is the outer key. Slice order is evaluation order; consumers must not reorder
// it.
type Result map[ir.NodeID]SiteOps

// SiteOps holds one function's effects, keyed by the site they happen at.
type SiteOps map[cfg.SiteID][]Op

// At returns the effects published for one site, in evaluation order.
func (r Result) At(fn ir.NodeID, site cfg.SiteID) []Op {
	return r[fn][site]
}
