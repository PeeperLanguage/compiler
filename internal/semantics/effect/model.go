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
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
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
	// OnEntry marks a binding that already exists when the site begins rather
	// than being established by it: a function parameter, or a match payload
	// binding, which the case edge creates before the arm body runs.
	//
	// Operations at a site are read in evaluation order, so a consumer that
	// replays them needs no distinction. One that treats a site as a set does:
	// liveness must not conclude a binding is dead before a site that merely
	// receives it.
	OnEntry bool
}

// Write stores to a binding that already exists.
type Write struct {
	Symbol *symbols.Symbol
	// Node is the assignment target.
	Node ast.NodeID
}

// Place identifies storage: a root binding and the projections taken from it to
// reach the value being used.
//
// Projections are reused from the canonical place walk rather than restated, so
// a consumer that already reasons about origins needs no translation. Empty
// projections mean the whole binding. A consumer that only cares which binding
// was touched reads Root and ignores the rest.
type Place struct {
	Root        *symbols.Symbol
	Projections []place.OriginProjection
}

// Use reads a binding's value. Node is the reading identifier rather than the
// enclosing statement, so a diagnostic anchors on the read itself.
//
// Location travels with the operation so a consumer never has to resolve the
// node back to syntax just to report against it. Define and Write carry no
// location because no current diagnostic anchors on them.
type Use struct {
	Place    Place
	Node     ast.NodeID
	Location *source.Location
	// Kind is what happens to the value here: observed, duplicated, or
	// consumed. The producer decides it from the position the value occupies
	// and, for a call argument, from the typechecker's published decision.
	Kind typeinfo.UseKind
}

// Borrow takes a reference to a place rather than reading its value. Mutable
// separates `&mut x` from `&x`, which is the difference that decides whether a
// second borrow conflicts.
type Borrow struct {
	Node     ast.NodeID
	Location *source.Location
	Mutable  bool
}

// CallBegin and CallEnd bracket the operations a call evaluates. Everything
// between them happens while the call is in progress.
//
// The bracket is a fact about evaluation, not one analysis's bookkeeping: a
// temporary created while computing an argument lives until the call completes,
// and a reservation taken for a receiver activates when the call starts. Any
// consumer modelling temporaries needs that boundary, and a flat sequence of
// uses cannot express it. Calls nest, so the pair nests too.
type CallBegin struct {
	Node     ast.NodeID
	Location *source.Location
}

type CallEnd struct {
	Node ast.NodeID
}

// Discard is a value produced and dropped, as an expression statement does.
// The value never reaches a binding, so anything owned in it dies here.
type Discard struct {
	Node     ast.NodeID
	Location *source.Location
}

func (Define) effectOp()    {}
func (Write) effectOp()     {}
func (Use) effectOp()       {}
func (Borrow) effectOp()    {}
func (Discard) effectOp()   {}
func (CallBegin) effectOp() {}
func (CallEnd) effectOp()   {}

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
