// The transient effect algebra defines the ordered semantic meaning of THIR
// constructs during post-CFG analysis. The producer is the only code that
// interprets a construct into binding effects; downstream analysis stages
// consume this stream without rediscovering syntax. Each stage keeps its own
// lattice, join, direction, and diagnostics.
package analysis

import (
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/thir"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
)

// effectOp is one semantic effect on one binding.
//
// The set is closed by unexported methods, so no kind can be introduced
// outside this package. visit(effectVisitor) supplies the other half of the contract:
// a new semantic operation extends effectVisitor and therefore breaks every exhaustive
// consumer at compile time until it makes an explicit decision.
type effectOp interface {
	effectOp()
	visit(effectVisitor)
}

// effectDefine brings a binding into existence. Initialized separates `let x = e`,
// which also stores a value, from a declaration that leaves storage empty.
type effectDefine struct {
	Symbol *symbols.Symbol
	Source thir.Node
	// Node is the declaration, which is where a diagnostic about the binding
	// itself belongs.
	Node source.NodeID
	// Value names the initializer whose value enters Symbol. It is zero for a
	// declaration without an initializer and for OnEntry bindings. Consumers
	// that track reference/pointer provenance can therefore update the binding
	// from this operation without rediscovering declaration syntax.
	Value         source.NodeID
	ValueExpr     thir.Expr
	IsInitialized bool
	// IsOnEntry marks a binding that already exists when the site begins rather
	// than being established by it: a function parameter, or a match payload
	// binding, which the case edge creates before the arm body runs.
	//
	// Operations at a site are read in evaluation order, so a consumer that
	// replays them needs no distinction. One that treats a site as a set does:
	// liveness must not conclude a binding is dead before a site that merely
	// receives it.
	IsOnEntry bool
}

// effectWrite stores to storage that already exists. It names a place for the same
// reason effectUse does: `a.b = x` writes a field, and an assignment target takes a
// mutating access whether it is a whole binding or a projection out of one.
type effectWrite struct {
	Place  effectPlace
	Target thir.Expr
	// Node is the assignment target.
	Node source.NodeID
	// Owner is the source construct performing the replacement. Cleanup plans
	// key pre-assignment drops by this identity, while Node remains the target
	// expression used for diagnostics and place typing.
	Owner source.NodeID
	// Value is the expression whose value is stored into effectPlace.
	Value     source.NodeID
	ValueExpr thir.Expr
	Location  *source.Location
}

// effectPlace identifies storage: a root binding and the projections taken from it to
// reach the value being used.
//
// Projections are reused from the canonical place walk rather than restated, so
// a consumer that already reasons about origins needs no translation. Empty
// projections mean the whole binding. A consumer that only cares which binding
// was touched reads Root and ignores the rest.
type effectPlace struct {
	// Root is the binding the storage belongs to. It is nil for a temporary:
	// a value that never lives in a binding, such as a call result.
	Root *symbols.Symbol
	// Temporary names the expression that produced the value when Root is nil.
	// Exactly one of Root and Temporary is set, which the validator enforces.
	//
	// Ownership needs the distinction because a temporary has nobody to own it:
	// a projection out of one has to be bound before use, and a discarded one
	// dies where it is produced.
	Temporary     source.NodeID
	TemporaryExpr thir.Expr
	Projections   []place.OriginProjection
}

// effectUse reads a binding's value. Node is the reading identifier rather than the
// enclosing statement, so a diagnostic anchors on the read itself.
//
// Location travels with the operation so a consumer never has to resolve the
// node back to syntax just to report against it. effectWrite carries the same evidence
// for assignment-access diagnostics; effectDefine currently needs no location.
type effectUse struct {
	Place    effectPlace
	Node     source.NodeID
	Source   thir.Expr
	Location *source.Location
	// Kind is what happens to the value here: observed, duplicated, or
	// consumed. The producer decides it from the position the value occupies
	// and, for a call argument, from the typechecker's recorded decision.
	Kind typeinfo.UseKind
}

// effectBorrow takes a reference to a place rather than reading its value. Mutable
// separates `&mut x` from `&x`, which is the difference that decides whether a
// second borrow conflicts.
//
// It names the place it borrows, so it is the whole access: a consumer that saw
// both a borrow and a separate read of the same place would charge that place
// twice.
type effectBorrow struct {
	Place  effectPlace
	Source thir.Expr
	// Node is the source expression that creates the borrow (an AddressExpr or
	// an adapted call argument). Operand is the place expression actually
	// borrowed. Keeping both identities means consumers never have to peel
	// syntax to rediscover that relationship.
	Node        source.NodeID
	Operand     source.NodeID
	OperandExpr thir.Expr
	Location    *source.Location
	IsMutable   bool
	// IsCallArgument marks a borrow handed to a call. It outlives the expression that
	// wrote it, because the callee holds it for as long as the call runs, so a
	// consumer tracking loans records one rather than only checking an access.
	IsCallArgument bool
	// IsRaw marks taking a raw pointer. It reads the place but takes no tracked
	// reference to it, so it neither conflicts with a borrow nor creates one.
	IsRaw bool
}

// effectIterate records the long-lived shared access a sequence loop holds on its
// iterable storage. The ordinary effectUse for the iterable remains in evaluation
// order; effectIterate adds only the lifetime fact that lasts until the loop exit.
// Range loops produce no effectIterate operation.
type effectIterate struct {
	Loop     source.NodeID
	Place    effectPlace
	Node     source.NodeID
	Source   thir.Expr
	Carrier  *symbols.Symbol
	Location *source.Location
}

// effectCallBegin and effectCallEnd bracket the operations a call evaluates. Everything
// between them happens while the call is in progress.
//
// The bracket is a fact about evaluation, not one analysis's bookkeeping: a
// temporary created while computing an argument lives until the call completes,
// and a reservation taken for a receiver activates when the call starts. Any
// consumer modelling temporaries needs that boundary, and a flat sequence of
// uses cannot express it. Calls nest, so the pair nests too.
type effectCallBegin struct {
	Node     source.NodeID
	Source   thir.Expr
	Location *source.Location
}

type effectCallEnd struct {
	Node source.NodeID
}

// effectDiscard is a value produced and dropped, as an expression statement does.
// The value never reaches a binding, so anything owned in it dies here.
type effectDiscard struct {
	Source thir.Expr
	// effectPlace is what was discarded, so a consumer can tell a dropped temporary
	// from a statement that merely names storage.
	Place    effectPlace
	Node     source.NodeID
	Location *source.Location
}

func (effectDefine) effectOp()    {}
func (effectWrite) effectOp()     {}
func (effectUse) effectOp()       {}
func (effectBorrow) effectOp()    {}
func (effectIterate) effectOp()   {}
func (effectDiscard) effectOp()   {}
func (effectCallBegin) effectOp() {}
func (effectCallEnd) effectOp()   {}

// effectStreams holds the transient effect streams for one analysis run.
//
// A cfg.SiteID is only meaningful relative to one graph, so function identity
// is the outer key. Slice order is evaluation order; consumers must not reorder
// it.
type effectStreams map[moduleid.FunctionID]effectSiteOps

// effectSiteOps holds one function's transient effects by CFG site.
type effectSiteOps map[cfg.SiteID][]effectOp
