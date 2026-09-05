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
// The set is closed by unexported methods, so no kind can be introduced
// outside this package. visit(Visitor) supplies the other half of the contract:
// a new semantic operation extends Visitor and therefore breaks every exhaustive
// consumer at compile time until it makes an explicit decision.
type Op interface {
	effectOp()
	visit(Visitor)
}

// Define brings a binding into existence. Initialized separates `let x = e`,
// which also stores a value, from a declaration that leaves storage empty.
type Define struct {
	Symbol *symbols.Symbol
	// Node is the declaration, which is where a diagnostic about the binding
	// itself belongs.
	Node ast.NodeID
	// Value names the initializer whose value enters Symbol. It is zero for a
	// declaration without an initializer and for OnEntry bindings. Consumers
	// that track reference/pointer provenance can therefore update the binding
	// from this operation without rediscovering declaration syntax.
	Value       ast.NodeID
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

// Write stores to storage that already exists. It names a place for the same
// reason Use does: `a.b = x` writes a field, and an assignment target takes a
// mutating access whether it is a whole binding or a projection out of one.
type Write struct {
	Place Place
	// Node is the assignment target.
	Node ast.NodeID
	// Owner is the source construct performing the replacement. Cleanup plans
	// key pre-assignment drops by this identity, while Node remains the target
	// expression used for diagnostics and place typing.
	Owner ast.NodeID
	// Value is the expression whose value is stored into Place.
	Value    ast.NodeID
	Location *source.Location
}

// Place identifies storage: a root binding and the projections taken from it to
// reach the value being used.
//
// Projections are reused from the canonical place walk rather than restated, so
// a consumer that already reasons about origins needs no translation. Empty
// projections mean the whole binding. A consumer that only cares which binding
// was touched reads Root and ignores the rest.
type Place struct {
	// Root is the binding the storage belongs to. It is nil for a temporary:
	// a value that never lives in a binding, such as a call result.
	Root *symbols.Symbol
	// Temporary names the expression that produced the value when Root is nil.
	// Exactly one of Root and Temporary is set, which the validator enforces.
	//
	// Ownership needs the distinction because a temporary has nobody to own it:
	// a projection out of one has to be bound before use, and a discarded one
	// dies where it is produced.
	Temporary   ast.NodeID
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
//
// It names the place it borrows, so it is the whole access: a consumer that saw
// both a borrow and a separate read of the same place would charge that place
// twice.
type Borrow struct {
	Place Place
	// Node is the source expression that creates the borrow (an AddressExpr or
	// an adapted call argument). Operand is the place expression actually
	// borrowed. Keeping both identities means consumers never have to peel
	// syntax to rediscover that relationship.
	Node     ast.NodeID
	Operand  ast.NodeID
	Location *source.Location
	Mutable  bool
	// Argument marks a borrow handed to a call. It outlives the expression that
	// wrote it, because the callee holds it for as long as the call runs, so a
	// consumer tracking loans records one rather than only checking an access.
	Argument bool
	// Raw marks taking a raw pointer. It reads the place but takes no tracked
	// reference to it, so it neither conflicts with a borrow nor creates one.
	Raw bool
}

// Iterate records the long-lived shared access a sequence loop holds on its
// iterable storage. The ordinary Use for the iterable is still published in
// evaluation order; Iterate adds only the lifetime fact that lasts until the
// loop exit. Range loops publish no Iterate operation.
type Iterate struct {
	Loop     ast.NodeID
	Place    Place
	Node     ast.NodeID
	Carrier  *symbols.Symbol
	Location *source.Location
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
	// Place is what was discarded, so a consumer can tell a dropped temporary
	// from a statement that merely names storage.
	Place    Place
	Node     ast.NodeID
	Location *source.Location
}

func (Define) effectOp()    {}
func (Write) effectOp()     {}
func (Use) effectOp()       {}
func (Borrow) effectOp()    {}
func (Iterate) effectOp()   {}
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
