package ownership

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/semantics/effect"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

// callFrame remembers where a call's loans start, so completing the call can
// give back the temporaries its arguments created.
type callFrame struct {
	call      ast.Node
	temporary int
	reserved  int
}

// storedReference is the reference value carried by an initializer/RHS before
// that expression is evaluated. Evaluation can move the source binding, so
// consumers must snapshot provenance before replaying the site's effects.
type storedReference struct {
	loans   []referenceLoan
	present bool
}

// applyEffects runs one site's published operations in evaluation order.
//
// This is what replaced ownership's own expression walk. Every decision it
// needs for ordinary storage/use behavior is published: what happens to a
// value, whether a reference is taken, and where a call begins and ends. AST
// identity is recovered only for source diagnostics and ownership-specific
// reference/raw-pointer provenance; generic action meaning is not re-derived.
func (a *analyzer) applyEffects(node *site, st state, loans *loanContext) {
	if a == nil || node == nil || node.cfgSite == nil {
		return
	}
	ops := a.effects[node.cfgSite.ID]
	visitor := &ownershipEffectVisitor{
		a: a, node: node, st: st, loans: loans,
		storedReferences: a.captureStoredReferences(ops, st),
		calls:            make([]callFrame, 0, 2),
	}
	for _, op := range ops {
		effect.Visit(op, visitor)
	}
}

type ownershipEffectVisitor struct {
	a                *analyzer
	node             *site
	st               state
	loans            *loanContext
	storedReferences map[ast.NodeID]storedReference
	calls            []callFrame
}

func (v *ownershipEffectVisitor) VisitDefine(op effect.Define) {
	v.a.applyDefineEffect(v.node, op, v.st, v.storedReferences)
}

func (v *ownershipEffectVisitor) VisitWrite(op effect.Write) {
	v.a.applyWriteEffect(v.node, op, v.st, v.loans, v.storedReferences)
}

func (v *ownershipEffectVisitor) VisitUse(op effect.Use) {
	v.a.applyUse(v.node, op, v.st, v.loans)
}

func (v *ownershipEffectVisitor) VisitBorrow(op effect.Borrow) {
	v.a.applyBorrow(v.node, op, v.st, v.loans, v.calls)
}

func (v *ownershipEffectVisitor) VisitIterate(op effect.Iterate) {
	v.a.applyIterateEffect(op, v.st, v.loans)
}

func (*ownershipEffectVisitor) VisitDiscard(effect.Discard) {
	// Cleanup planning consumes Discard separately after evaluation.
}

func (v *ownershipEffectVisitor) VisitCallBegin(op effect.CallBegin) {
	call := v.a.module.TypedASTNodes[op.Node]
	v.calls = append(v.calls, callFrame{
		call: call, temporary: len(v.loans.temporary), reserved: len(v.loans.reserved),
	})
}

func (v *ownershipEffectVisitor) VisitCallEnd(effect.CallEnd) {
	if len(v.calls) == 0 {
		return
	}
	frame := v.calls[len(v.calls)-1]
	v.calls = v.calls[:len(v.calls)-1]
	// Reservations activate as the call starts, which is observable only once
	// its arguments are evaluated; argument temporaries die with the call.
	v.a.activateCallReservations(frame.call, frame.reserved, v.loans)
	v.loans.temporary = v.loans.temporary[:frame.temporary]
	v.loans.reserved = v.loans.reserved[:frame.reserved]
}

// captureStoredReferences snapshots the reference provenance carried by values
// that a Define or Write will store. This runs before any operation at the site
// so a move performed while evaluating the source cannot erase the value that
// is about to enter the destination.
func (a *analyzer) captureStoredReferences(ops []effect.Op, st state) map[ast.NodeID]storedReference {
	visitor := &storedReferenceVisitor{a: a, st: st, values: make(map[ast.NodeID]storedReference)}
	for _, op := range ops {
		effect.Visit(op, visitor)
	}
	return visitor.values
}

type storedReferenceVisitor struct {
	a      *analyzer
	st     state
	values map[ast.NodeID]storedReference
}

func (v *storedReferenceVisitor) capture(valueID ast.NodeID) {
	if valueID == 0 {
		return
	}
	if _, captured := v.values[valueID]; captured {
		return
	}
	value, _ := v.a.module.TypedASTNodes[valueID].(ast.Expr)
	loans, present := v.a.referenceValueForExpr(value, v.st)
	v.values[valueID] = storedReference{loans: loans, present: present}
}

func (v *storedReferenceVisitor) VisitDefine(op effect.Define)  { v.capture(op.Value) }
func (v *storedReferenceVisitor) VisitWrite(op effect.Write)    { v.capture(op.Value) }
func (*storedReferenceVisitor) VisitUse(effect.Use)             {}
func (*storedReferenceVisitor) VisitBorrow(effect.Borrow)       {}
func (*storedReferenceVisitor) VisitIterate(effect.Iterate)     {}
func (*storedReferenceVisitor) VisitDiscard(effect.Discard)     {}
func (*storedReferenceVisitor) VisitCallBegin(effect.CallBegin) {}
func (*storedReferenceVisitor) VisitCallEnd(effect.CallEnd)     {}

// applyDefineEffect makes a newly defined binding own the value published by
// the semantic producer. The declaration syntax is irrelevant here: any future
// construct that publishes Define inherits the same ownership transition.
func (a *analyzer) applyDefineEffect(node *site, op effect.Define, st state, references map[ast.NodeID]storedReference) {
	if op.Symbol == nil {
		return
	}
	if op.Value != 0 {
		value, _ := a.module.TypedASTNodes[op.Value].(ast.Expr)
		reference := references[op.Value]
		a.updatePointerSymbol(op.Symbol, node.scope, value, st)
		a.updateReferenceSymbol(op.Symbol, reference.loans, reference.present, st)
	} else if !op.OnEntry {
		// An ordinary declaration without an initializer establishes empty
		// storage. Entry bindings already carry state seeded by the edge/function
		// entry and must not have that provenance erased here.
		a.updatePointerSymbol(op.Symbol, node.scope, nil, st)
		a.updateReferenceSymbol(op.Symbol, nil, false, st)
	}
	if ownershipTrackedSymbol(op.Symbol) {
		delete(st.moved, op.Symbol)
		st.live[op.Symbol] = struct{}{}
	}
}

// applyWriteEffect records replacement of existing storage. Place decides
// whether this is whole-binding reinitialization or mutation through a
// projection; Owner gives cleanup a stable identity independent of syntax kind.
func (a *analyzer) applyWriteEffect(
	node *site,
	op effect.Write,
	st state,
	loans *loanContext,
	references map[ast.NodeID]storedReference,
) {
	if op.Owner != 0 {
		delete(a.cleanup.BeforeAssign, ir.NodeID(op.Owner))
	}
	target, _ := a.module.TypedASTNodes[op.Node].(ast.Expr)
	if target == nil {
		return
	}

	// Assigning through a projection reaches into existing storage and cannot
	// revive a root that was already moved away.
	if op.Place.Root == nil || len(op.Place.Projections) > 0 {
		if op.Place.Root != nil && a.reportUseAfterMove(op.Place.Root, st, effect.Use{
			Place: op.Place, Node: op.Node, Location: op.Location,
		}) {
			return
		}
		a.checkStorageAccess(target, loans, storageMutate)
		if op.Owner != 0 && typeinfo.OwnershipCapabilityOf(a.exprType(target)).Drop {
			a.cleanup.BeforeAssign[ir.NodeID(op.Owner)] = struct{}{}
		}
		return
	}

	sym := op.Place.Root
	if _, referenceTarget := referenceMutability(sym); !referenceTarget {
		a.checkStorageAccess(target, loans, storageMutate)
	}
	if typ, ok := symbols.GetSymbolType(sym); ok && typeinfo.OwnershipCapabilityOf(typ).Drop {
		if _, live := st.live[sym]; live && op.Owner != 0 {
			a.cleanup.BeforeAssign[ir.NodeID(op.Owner)] = struct{}{}
		}
	}
	if ownershipTrackedSymbol(sym) {
		delete(st.moved, sym)
		st.live[sym] = struct{}{}
	}
	if op.Value == 0 {
		return
	}
	value, _ := a.module.TypedASTNodes[op.Value].(ast.Expr)
	reference := references[op.Value]
	a.updatePointerSymbol(sym, node.scope, value, st)
	a.updateReferenceSymbol(sym, reference.loans, reference.present, st)
}

// applyIterateEffect installs the long-lived shared access a sequence loop
// holds on its iterable. Iteration kind and carrier identity were decided by
// typechecking and published by effects; ownership does not inspect ForStmt or
// the iteration plan.
func (a *analyzer) applyIterateEffect(op effect.Iterate, st state, loans *loanContext) {
	if op.Carrier == nil || op.Node == 0 {
		return
	}
	iterable, _ := a.module.TypedASTNodes[op.Node].(ast.Expr)
	if iterable == nil {
		return
	}
	a.checkStorageAccess(iterable, loans, storageSharedBorrow)
	origins := a.originsForExpr(iterable)
	if op.Place.Root != nil {
		if value, found := st.references[op.Place.Root]; found {
			origins = referenceOrigins(value)
		}
	} else if value, hasValue := a.referenceValueForExpr(iterable, st); hasValue {
		origins = referenceOrigins(value)
	}
	if len(origins) == 0 {
		return
	}
	st.references[op.Carrier] = []referenceLoan{{
		id: loanID{node: iterable}, origins: origins, site: iterable, loop: op.Loop,
	}}
}

func (a *analyzer) applyUse(node *site, op effect.Use, st state, loans *loanContext) {
	syntax, _ := a.module.TypedASTNodes[op.Node].(ast.Expr)
	if op.Place.Root == nil {
		// A value with no owner. Only a projection out of one has an effect
		// here, and it is that the projection must be bound before use. The
		// effect place already names the temporary base; do not peel syntax.
		base, _ := a.module.TypedASTNodes[op.Place.Temporary].(ast.Expr)
		a.planProjectionBaseDrop(syntax, base)
		return
	}
	if a.reportUseAfterMove(op.Place.Root, st, op) {
		return
	}
	if len(op.Place.Projections) == 0 {
		a.applyWholeUse(node, op, st, loans, syntax)
		return
	}
	a.applyProjectedUse(op, st, loans, syntax)
}

// applyWholeUse is the effect of using a binding entire: its move state changes,
// and the storage it names is accessed.
func (a *analyzer) applyWholeUse(node *site, op effect.Use, st state, loans *loanContext, syntax ast.Expr) {
	sym := op.Place.Root
	a.applyUseKind(sym, op, st, syntax)
	if _, reference := referenceMutability(sym); reference {
		loans.useReference(sym)
		return
	}
	if syntax != nil {
		a.checkStorageAccess(syntax, loans, storageAccessForUse(a.exprType(syntax), op.Kind))
	}
	if referenceHoldingSymbol(sym) {
		loans.useReference(sym)
	}
}

// applyProjectedUse is the effect of using part of a binding. Consuming a part
// is what a partial move is, and the language does not allow it out of a
// move-only place.
func (a *analyzer) applyProjectedUse(op effect.Use, st state, loans *loanContext, syntax ast.Expr) {
	if syntax == nil {
		return
	}
	// Reaching through a binding spends it the same way naming it does, so a
	// reference reached through here is one use closer to its last.
	if sym := op.Place.Root; sym != nil {
		if _, reference := referenceMutability(sym); reference || referenceHoldingSymbol(sym) {
			loans.useReference(sym)
		}
	}
	a.checkStorageAccess(syntax, loans, storageAccessForUse(a.exprType(syntax), op.Kind))
	if op.Kind == typeinfo.UseRead || !ownershipTrackedType(a.exprType(syntax)) {
		return
	}
	if a.partialVariantPayloadMove(op.Node) {
		a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
			"move-only variant payload cannot be moved from partial place; borrow it instead", op.Location, "")
		return
	}
	if projection, projected := place.Project(syntax); projected && projection.Step.Kind == place.OriginIndex {
		a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
			"move-only indexed element cannot be used by value; borrow it with `&` or `&mut`", op.Location, "")
		return
	}
	a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
		"move-only subexpression must be bound before it can be consumed", op.Location, "")
}

// applyBorrow records a reference taken to a place. A mutable borrow taken while
// a call is being evaluated is a reservation rather than a borrow: it does not
// take effect until the call it is an argument to actually starts.
func (a *analyzer) applyBorrow(node *site, op effect.Borrow, st state, loans *loanContext, calls []callFrame) {
	if op.Place.Root != nil && a.reportUseAfterMove(op.Place.Root, st, effect.Use{
		Place: op.Place, Node: op.Node, Location: op.Location, Kind: typeinfo.UseRead,
	}) {
		return
	}
	access := storageSharedBorrow
	if op.Mutable {
		access = storageMutableBorrow
		if op.Argument {
			// A mutable borrow handed to a call does not take effect until the
			// call starts, so it is reserved here and activated there.
			access = storageMutableReservation
		}
	}
	if op.Raw {
		// A raw pointer is not a tracked reference: it neither conflicts with a
		// live borrow nor becomes one.
		return
	}
	if sym := op.Place.Root; sym != nil {
		if _, reference := referenceMutability(sym); reference || referenceHoldingSymbol(sym) {
			loans.useReference(sym)
		}
	}
	borrowed, _ := a.module.TypedASTNodes[op.Operand].(ast.Expr)
	if borrowed == nil {
		return
	}
	a.checkStorageAccess(borrowed, loans, access)
	if op.Argument {
		a.installArgumentLoan(borrowed, op, loans, calls)
	}
}

// installArgumentLoan records the loan a call holds on an argument for as long
// as it runs. A mutable one is reserved until the call starts; a shared one is
// a temporary that dies when the call completes.
func (a *analyzer) installArgumentLoan(borrowed ast.Expr, op effect.Borrow, loans *loanContext, calls []callFrame) {
	origins := a.originsForExpr(borrowed)
	if len(origins) == 0 {
		return
	}
	var call ast.Node
	if len(calls) > 0 && calls[len(calls)-1].call != nil {
		call = calls[len(calls)-1].call
	}
	loan := referenceLoan{
		id:      loanID{node: borrowed},
		origins: origins,
		mutable: op.Mutable,
		site:    borrowed,
	}
	if op.Mutable {
		loans.reserved = append(loans.reserved, loanFact{
			loan:         loan,
			holder:       a.referenceHolder(borrowed),
			keepingAlive: call,
		})
		return
	}
	loans.addTemporary([]referenceLoan{loan}, call)
}

func (a *analyzer) reportUseAfterMove(sym *symbols.Symbol, st state, op effect.Use) bool {
	site, moved := st.moved[sym]
	if !moved {
		return false
	}
	diag := a.ctx.Diagnostics.AddError(diagnostics.ErrUseAfterMove, "value used after move", op.Location, "")
	if site != nil {
		diag.WithSecondaryLabel(ast.LocOf(site), "moved here")
	}
	return true
}

// applyUseKind is what happens to a binding's value at one use: a move leaves it
// dead, and a copy is rejected for anything the language will not duplicate. A
// read leaves it as it was.
func (a *analyzer) applyUseKind(sym *symbols.Symbol, op effect.Use, st state, syntax ast.Expr) {
	if !ownershipTrackedSymbol(sym) {
		return
	}
	switch op.Kind {
	case typeinfo.UseCopy:
		if symType, typed := symbols.GetSymbolType(sym); typed {
			if _, mutable, ok := typeinfo.ReferenceTarget(typeinfo.Underlying(symType)); ok && mutable {
				a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
					"mutable reference cannot be copied; pass it directly to transfer or reborrow", op.Location, "")
				return
			}
		}
		a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
			"copy of move-only value requires a consuming context", op.Location, "")
	case typeinfo.UseMove:
		st.moved[sym] = syntax
		delete(st.live, sym)
	}
}
