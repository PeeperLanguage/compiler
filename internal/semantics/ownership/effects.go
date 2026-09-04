package ownership

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/effect"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

// callFrame remembers where a call's loans start, so completing the call can
// give back the temporaries its arguments created.
type callFrame struct {
	call      *ast.CallExpr
	temporary int
	reserved  int
}

// applyEffects runs one site's published operations in evaluation order.
//
// This is what replaced ownership's own expression walk. Every decision it
// needs is published: what happens to a value, whether a reference is taken,
// and where a call begins and ends. Syntax is recovered only to reuse the
// helpers that already report against it, never to work out meaning.
func (a *analyzer) applyEffects(node *site, st state, loans *loanContext) {
	if a == nil || node == nil || node.cfgSite == nil {
		return
	}
	calls := make([]callFrame, 0, 2)
	for _, op := range a.effects[node.cfgSite.ID] {
		switch op := op.(type) {
		case effect.CallBegin:
			call, _ := a.module.TypedASTNodes[op.Node].(*ast.CallExpr)
			calls = append(calls, callFrame{
				call:      call,
				temporary: len(loans.temporary),
				reserved:  len(loans.reserved),
			})
		case effect.CallEnd:
			if len(calls) == 0 {
				continue
			}
			frame := calls[len(calls)-1]
			calls = calls[:len(calls)-1]
			// Reservations activate as the call starts, which is observable
			// only once its arguments are evaluated; the temporaries those
			// arguments created die with the call.
			a.activateCallReservations(frame.call, frame.reserved, loans)
			loans.temporary = loans.temporary[:frame.temporary]
			loans.reserved = loans.reserved[:frame.reserved]
		case effect.Write:
			// Assigning a whole binding reinitializes it, so a moved one is a
			// legal target. Assigning through a projection is different: it
			// reaches into storage that was moved away.
			if op.Place.Root != nil && len(op.Place.Projections) > 0 {
				a.reportUseAfterMove(op.Place.Root, st, effect.Use{
					Place: op.Place, Node: op.Node, Location: op.Location,
				})
			}
		case effect.Use:
			a.applyUse(node, op, st, loans)
		case effect.Borrow:
			a.applyBorrow(node, op, st, loans, calls)
		}
	}
}

func (a *analyzer) applyUse(node *site, op effect.Use, st state, loans *loanContext) {
	syntax, _ := a.module.TypedASTNodes[op.Node].(ast.Expr)
	if op.Place.Root == nil {
		// A value with no owner. Only a projection out of one has an effect
		// here, and it is that the projection must be bound before use.
		a.planProjectionBaseDrop(syntax, projectionBaseOf(syntax))
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
	if a.planProjectionBaseDrop(syntax, projectionBaseOf(syntax)) {
		return
	}
	a.checkStorageAccess(syntax, loans, storageAccessForUse(a.exprType(syntax), op.Kind))
	if op.Kind == typeinfo.UseRead || !ownershipTrackedType(a.exprType(syntax)) {
		return
	}
	if a.partialVariantPayloadMove(syntax) {
		a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
			"move-only variant payload cannot be moved from partial place; borrow it instead", op.Location, "")
		return
	}
	if _, indexed := syntax.(*ast.IndexExpr); indexed {
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
	syntax, _ := a.module.TypedASTNodes[op.Node].(ast.Expr)
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
	borrowed := borrowedExpr(syntax)
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

// projectionBaseOf returns the expression a projection projects from.
func projectionBaseOf(expr ast.Expr) ast.Expr {
	switch node := expr.(type) {
	case *ast.SelectorExpr:
		return node.Expr
	case *ast.IndexExpr:
		return node.Expr
	}
	return nil
}

// borrowedExpr returns the place an address expression borrows. A borrow
// published for a reference argument names the argument itself.
func borrowedExpr(expr ast.Expr) ast.Expr {
	if address, taken := expr.(*ast.AddressExpr); taken {
		return address.Expr
	}
	return expr
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
