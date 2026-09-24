package ownership

import (
	"maps"
	"slices"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	graphcore "compiler/internal/graph"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/thir"
	"compiler/internal/semantics/effect"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
)

type loanID struct {
	node      ir.NodeID
	parameter *symbols.Symbol
}

type referenceLoan struct {
	// path identifies the slot within the holder, not the borrowed storage.
	// One loan can occupy multiple enum fields and survive replacement of one.
	path    []place.OriginProjection
	id      loanID
	origins []place.Origin
	mutable bool
	site    ir.SourceInfo
	loop    ast.NodeID
}

type loanFact struct {
	loan         referenceLoan
	holder       *symbols.Symbol
	keepingAlive ir.SourceInfo
}

type loanContext struct {
	persistent []loanFact
	temporary  []loanFact
	reserved   []loanFact
	remaining  map[*symbols.Symbol]int
	liveOut    map[*symbols.Symbol]ir.SourceInfo
}

type storageAccess uint8

const (
	storageRead storageAccess = iota
	storageSharedBorrow
	storageMutableReservation
	storageMutableBorrow
	storageMutate
	storageConsume
	storageDestroy
)

func (access storageAccess) requiresExclusiveAccess() bool {
	switch access {
	case storageMutableBorrow, storageMutate, storageConsume, storageDestroy:
		return true
	default:
		return false
	}
}

func (a *analyzer) newLoanContext(node *site, st state) *loanContext {
	if node == nil || node.cfgSite == nil {
		return &loanContext{remaining: make(map[*symbols.Symbol]int)}
	}
	ctx := &loanContext{
		remaining: make(map[*symbols.Symbol]int),
		liveOut:   a.symbolLiveOut[node.cfgSite.ID],
	}
	for _, sym := range a.symbolUseSequence(node, referenceHoldingSymbol) {
		ctx.remaining[sym]++
	}
	for sym, value := range st.references {
		keepingAlive, live := a.symbolLiveIn[node.cfgSite.ID][sym]
		if !live {
			for _, loan := range value {
				if loan.loop != 0 {
					live = true
					break
				}
			}
		}
		if !live {
			continue
		}
		for _, loan := range value {
			ctx.persistent = append(ctx.persistent, loanFact{
				loan:         loan,
				holder:       sym,
				keepingAlive: keepingAlive,
			})
		}
	}
	return ctx
}

func (ctx *loanContext) useReference(sym *symbols.Symbol) {
	if ctx == nil || sym == nil || ctx.remaining[sym] == 0 {
		return
	}
	ctx.remaining[sym]--
	if ctx.remaining[sym] > 0 {
		return
	}
	if _, live := ctx.liveOut[sym]; live {
		return
	}
	ctx.removeHolder(sym)
}

func (ctx *loanContext) removeHolder(sym *symbols.Symbol) {
	if ctx == nil || sym == nil {
		return
	}
	kept := ctx.persistent[:0]
	for _, active := range ctx.persistent {
		if active.holder != sym {
			kept = append(kept, active)
		}
	}
	ctx.persistent = kept
}

func (ctx *loanContext) addTemporary(value []referenceLoan) {
	if ctx == nil {
		return
	}
	for _, loan := range value {
		ctx.temporary = append(ctx.temporary, loanFact{loan: loan})
	}
}

func (a *analyzer) checkStorageAccess(
	expr thir.Expr,
	loans *loanContext,
	access storageAccess,
) {
	if a == nil || expr == nil || loans == nil {
		return
	}
	origins := a.originsForExpr(expr)
	if access == storageMutate && a.module != nil && a.module.Flow != nil {
		// Replacing a reference slot mutates the carrier, not its old referent.
		origins = a.module.Flow.StorageOrigins(ast.NodeID(expr.SourceInfo().NodeID))
	}
	a.reportLoanConflict(
		origins,
		referenceHolder(expr),
		access,
		expr.SourceInfo(),
		loans,
	)
}

func (a *analyzer) reportLoanConflict(
	origins []place.Origin,
	exempt *symbols.Symbol,
	access storageAccess,
	site ir.SourceInfo,
	loans *loanContext,
) {
	if a == nil || len(origins) == 0 || loans == nil {
		return
	}
	conflicts := func(fact loanFact) bool {
		if (exempt != nil && fact.holder == exempt) || !place.OriginsOverlap(origins, fact.loan.origins) {
			return false
		}
		return fact.loan.mutable || access.requiresExclusiveAccess()
	}
	var conflict *loanFact
	reservedConflict := false
	for i := range loans.persistent {
		if conflicts(loans.persistent[i]) {
			conflict = &loans.persistent[i]
			break
		}
	}
	if conflict == nil {
		for i := range loans.temporary {
			if conflicts(loans.temporary[i]) {
				conflict = &loans.temporary[i]
				break
			}
		}
	}
	if conflict == nil && access.requiresExclusiveAccess() {
		conflict = overlappingLoan(origins, loans.reserved, exempt)
		reservedConflict = conflict != nil
	}
	if conflict == nil {
		return
	}

	message := "cannot access storage while it is borrowed"
	switch access {
	case storageRead:
		message = "cannot read storage while it is mutably borrowed"
	case storageSharedBorrow:
		message = "cannot borrow storage while it is mutably borrowed"
	case storageMutableReservation:
		message = "cannot reserve mutable borrow while storage is mutably borrowed"
	case storageMutableBorrow:
		message = "cannot borrow storage mutably while it is already borrowed"
	case storageMutate:
		message = "cannot mutate storage while it is borrowed"
	case storageConsume:
		message = "cannot consume storage while it is borrowed"
	case storageDestroy:
		message = "cannot destroy storage while it is borrowed"
	}
	diag := a.diagnostics.AddError(diagnostics.ErrBorrowConflict, message, site.Location, "conflicting access")
	addLoanConflictLabels(diag, conflict, reservedConflict)
}

func (a *analyzer) activateCallReservations(location *source.Location, mark int, loans *loanContext) {
	if a == nil || location == nil || loans == nil || mark >= len(loans.reserved) {
		return
	}
	current := loans.reserved[mark:]
	for i := range current {
		reservation := &current[i]
		reservedConflict := false
		conflict := overlappingLoan(reservation.loan.origins, loans.persistent, reservation.holder)
		if conflict == nil {
			conflict = overlappingLoan(reservation.loan.origins, loans.temporary, nil)
		}
		if conflict == nil {
			conflict = overlappingLoan(reservation.loan.origins, loans.reserved[:mark], nil)
			reservedConflict = conflict != nil
		}
		if conflict == nil {
			conflict = overlappingLoan(reservation.loan.origins, current[:i], nil)
			reservedConflict = conflict != nil
		}
		if conflict == nil {
			continue
		}

		diag := a.diagnostics.AddError(
			diagnostics.ErrBorrowConflict,
			"cannot activate mutable borrow while storage is borrowed",
			location,
			"mutable borrow activates here",
		)
		if reservation.loan.site.Location != nil {
			diag.WithSecondaryLabel(reservation.loan.site.Location, "mutable borrow reserved here")
		}
		addLoanConflictLabels(diag, conflict, reservedConflict)
	}
}

func addLoanConflictLabels(
	diag *diagnostics.Diagnostic,
	conflict *loanFact,
	reservationConflict bool,
) {
	if diag == nil || conflict == nil {
		return
	}
	borrowKind := "shared borrow created here"
	if reservationConflict {
		borrowKind = "mutable borrow reserved here"
	} else if conflict.loan.mutable {
		borrowKind = "mutable borrow created here"
	}
	if conflict.loan.site.Location != nil {
		diag.WithSecondaryLabel(conflict.loan.site.Location, borrowKind)
	}
	if conflict.keepingAlive.NodeID == 0 || conflict.keepingAlive.NodeID == conflict.loan.site.NodeID ||
		conflict.keepingAlive.Location == nil {
		return
	}
	keepingMessage := "borrow remains live until this use"
	if reservationConflict {
		keepingMessage = "mutable borrow activates when this call starts"
	} else if conflict.holder == nil {
		keepingMessage = "borrow remains active until this call completes"
	}
	diag.WithSecondaryLabel(conflict.keepingAlive.Location, keepingMessage)
}

func overlappingLoan(origins []place.Origin, facts []loanFact, exempt *symbols.Symbol) *loanFact {
	for i := range facts {
		if exempt != nil && facts[i].holder == exempt {
			continue
		}
		if place.OriginsOverlap(origins, facts[i].loan.origins) {
			return &facts[i]
		}
	}
	return nil
}

func referenceHolder(expr thir.Expr) *symbols.Symbol {
	if expr == nil {
		return nil
	}
	if address, taken := expr.(*thir.Address); taken {
		expr = address.Value
	}
	if expr == nil || expr.ExprPlace() == nil {
		return nil
	}
	sym := expr.ExprPlace().Root
	if _, reference := referenceMutability(sym); reference {
		return sym
	}
	return nil
}

func (a *analyzer) referenceValueForTHIR(expr thir.Expr, st state) ([]referenceLoan, bool) {
	if a == nil || a.module == nil || expr == nil {
		return []referenceLoan{}, false
	}
	if ident, ok := expr.(*thir.Ident); ok && ident.Symbol != nil && referenceHoldingSymbol(ident.Symbol) {
		if value, found := st.references[ident.Symbol]; found {
			return copyReferenceLoans(value), true
		}
	}
	if a.module.Flow == nil {
		return []referenceLoan{}, false
	}
	id := ast.NodeID(expr.SourceInfo().NodeID)
	if _, mutable, ok := typeinfo.ReferenceValueTarget(a.exprType(expr)); ok {
		if _, projected := expr.(*thir.Field); projected {
			var value []referenceLoan
			for _, storage := range a.module.Flow.StorageOrigins(id) {
				for _, loan := range st.references[storage.Root] {
					if slices.Equal(loan.path, storage.Projections) {
						loan.path = nil
						value = append(value, loan)
					}
				}
			}
			if len(value) > 0 {
				return copyReferenceLoans(value), true
			}
		}
		origins := place.CloneOrigins(a.module.Flow.ValueOrigins(id))
		if len(origins) == 0 {
			return []referenceLoan{}, false
		}
		return []referenceLoan{{id: loanID{node: expr.SourceInfo().NodeID}, origins: origins, mutable: mutable, site: expr.SourceInfo()}}, true
	}
	slots, aggregate := a.module.Flow.AggregateSlots(id)
	if aggregate {
		var loans []referenceLoan
		for _, slot := range slots {
			fieldLoans, found := a.referenceValueForTHIR(slot.ValueExpr, st)
			if !found {
				continue
			}
			for i := range fieldLoans {
				fieldLoans[i].path = append([]place.OriginProjection{slot.Projection}, fieldLoans[i].path...)
			}
			loans = append(loans, fieldLoans...)
		}
		return loans, len(loans) > 0
	}
	return []referenceLoan{}, false
}

// replaceReferenceField consumes flow's exact storage identity. Accepted local
// enum reference fields are direct/optional; nested reference aggregates remain
// rejected by typechecking. Other holders and sibling slots retain their loans.
func (a *analyzer) replaceReferenceField(target thir.Expr, value storedReference, st state) {
	if _, _, reference := typeinfo.ReferenceValueTarget(a.exprType(target)); !reference || a.module.Flow == nil {
		return
	}
	storage := a.module.Flow.StorageOrigins(ast.NodeID(target.SourceInfo().NodeID))
	if len(storage) != 1 || len(storage[0].Projections) == 0 {
		return
	}
	destination := storage[0]
	if !referenceHoldingSymbol(destination.Root) {
		return
	}
	var kept []referenceLoan
	for _, loan := range st.references[destination.Root] {
		if !slices.Equal(loan.path, destination.Projections) {
			kept = append(kept, loan)
		}
	}
	for _, loan := range copyReferenceLoans(value.loans) {
		loan.path = slices.Clone(destination.Projections)
		kept = append(kept, loan)
	}
	a.updateReferenceSymbol(destination.Root, kept, len(kept) > 0, st)
}

func (a *analyzer) originsForExpr(expr thir.Expr) []place.Origin {
	if a == nil || a.module == nil || a.module.Flow == nil || expr == nil {
		return nil
	}
	return place.CloneOrigins(a.module.Flow.ValueOrigins(ast.NodeID(expr.SourceInfo().NodeID)))
}

func (a *analyzer) validateReferenceReturn(stmt *thir.Return, st state) {
	if a == nil || a.function == nil || stmt == nil || stmt.Value == nil {
		return
	}
	if _, _, reference := typeinfo.ReferenceValueTarget(a.module.EffectiveExprType(ast.NodeID(stmt.Value.SourceInfo().NodeID))); !reference {
		return
	}
	value, found := a.referenceValueForTHIR(stmt.Value, st)
	if !found {
		return
	}
	functionType, _ := symbols.GetSymbolType(a.function.Symbol)
	fnType, _ := functionType.(*typeinfo.FuncType)
	if fnType == nil || fnType.ReturnOrigins == nil {
		return
	}
	allowed := make(map[*symbols.Symbol]struct{}, len(fnType.ReturnOrigins.Sources))
	for _, slot := range fnType.ReturnOrigins.Sources {
		if slot >= 0 && slot < len(a.function.Params) && a.function.Params[slot].Symbol != nil {
			allowed[a.function.Params[slot].Symbol] = struct{}{}
		}
	}
	for _, origin := range referenceOrigins(value) {
		if _, declared := allowed[origin.Root]; declared {
			continue
		}
		diagnostic := a.diagnostics.AddError(
			diagnostics.ErrInvalidReturn,
			"returned reference originates outside declared `from` sources",
			stmt.Value.SourceInfo().Location,
			"undeclared return origin",
		)
		if a.function.ReturnOriginsLocation != nil {
			diagnostic.WithSecondaryLabel(a.function.ReturnOriginsLocation, "declared return origins")
		}
		return
	}
}

func (a *analyzer) updateReferenceSymbol(sym *symbols.Symbol, value []referenceLoan, hasValue bool, st state) {
	if sym == nil {
		return
	}
	mutable, reference := referenceMutability(sym)
	if (!reference && !referenceHoldingSymbol(sym)) || !hasValue {
		delete(st.references, sym)
		return
	}
	value = copyReferenceLoans(value)
	if reference {
		for i := range value {
			value[i].mutable = mutable
		}
	}
	st.references[sym] = value
}

func referenceMutability(sym *symbols.Symbol) (bool, bool) {
	typ, ok := symbols.GetSymbolType(sym)
	if !ok {
		return false, false
	}
	_, mutable, reference := typeinfo.ReferenceValueTarget(typ)
	return mutable, reference
}

func referenceHoldingSymbol(sym *symbols.Symbol) bool {
	typ, ok := symbols.GetSymbolType(sym)
	return ok && typeinfo.ContainsReference(typ)
}

func copyReferenceLoans(value []referenceLoan) []referenceLoan {
	copyValue := make([]referenceLoan, len(value))
	copy(copyValue, value)
	for i := range copyValue {
		copyValue[i].origins = place.CloneOrigins(copyValue[i].origins)
		copyValue[i].path = slices.Clone(copyValue[i].path)
	}
	return copyValue
}

func sameReferenceValues(left, right map[*symbols.Symbol][]referenceLoan) bool {
	if len(left) != len(right) {
		return false
	}
	for sym, leftValue := range left {
		rightValue, ok := right[sym]
		if !ok || !sameReferenceLoans(leftValue, rightValue) {
			return false
		}
	}
	return true
}

func mergeReferenceValues(dst, src map[*symbols.Symbol][]referenceLoan) bool {
	changed := false
	for sym, srcValue := range src {
		dstValue, exists := dst[sym]
		if !exists {
			dst[sym] = copyReferenceLoans(srcValue)
			changed = true
			continue
		}
		for _, srcLoan := range srcValue {
			index := referenceLoanIndex(dstValue, srcLoan)
			if index < 0 {
				srcLoan.origins = place.CloneOrigins(srcLoan.origins)
				srcLoan.path = slices.Clone(srcLoan.path)
				dstValue = append(dstValue, srcLoan)
				changed = true
				continue
			}
			merged := place.MergeOrigins(dstValue[index].origins, srcLoan.origins)
			if !place.SameOrigins(dstValue[index].origins, merged) {
				dstValue[index].origins = merged
				changed = true
			}
		}
		dst[sym] = dstValue
	}
	return changed
}

func referenceOrigins(loans []referenceLoan) []place.Origin {
	var origins []place.Origin
	for _, loan := range loans {
		origins = place.MergeOrigins(origins, loan.origins)
	}
	return origins
}

func sameReferenceLoans(left, right []referenceLoan) bool {
	if len(left) != len(right) {
		return false
	}
	for _, leftLoan := range left {
		index := referenceLoanIndex(right, leftLoan)
		if index < 0 {
			return false
		}
		rightLoan := right[index]
		if leftLoan.mutable != rightLoan.mutable || leftLoan.site != rightLoan.site || leftLoan.loop != rightLoan.loop ||
			!place.SameOrigins(leftLoan.origins, rightLoan.origins) {
			return false
		}
	}
	return true
}

func referenceLoanIndex(loans []referenceLoan, candidate referenceLoan) int {
	for i := range loans {
		if loans[i].id == candidate.id && slices.Equal(loans[i].path, candidate.path) {
			return i
		}
	}
	return -1
}

func (a *analyzer) computeSymbolLiveness() {
	if a == nil || a.sites == nil {
		return
	}
	a.symbolLiveIn = make(map[cfg.SiteID]map[*symbols.Symbol]ir.SourceInfo, len(a.order))
	a.symbolLiveOut = make(map[cfg.SiteID]map[*symbols.Symbol]ir.SourceInfo, len(a.order))
	work := graphcore.NewWorklist[cfg.SiteID]()
	for _, id := range slices.Backward(a.order) {
		work.Add(id)
	}
	for {
		id, pending := work.Next()
		if !pending {
			break
		}

		out := make(map[*symbols.Symbol]ir.SourceInfo)
		node := a.sites[id]
		if node == nil || node.cfgSite == nil {
			continue
		}
		for _, edge := range a.graph.SiteEdges.OutEdges(node.cfgSite.ID) {
			mergeSymbolLiveSets(out, a.symbolLiveIn[edge.To])
		}
		uses, definitions := a.symbolUsesAndDefinitions(node)
		in := maps.Clone(out)
		for sym := range definitions {
			delete(in, sym)
		}
		mergeSymbolLiveSets(in, uses)

		if maps.Equal(a.symbolLiveIn[id], in) && maps.Equal(a.symbolLiveOut[id], out) {
			continue
		}
		a.symbolLiveIn[id] = in
		a.symbolLiveOut[id] = out
		for _, edge := range a.graph.SiteEdges.InEdges(node.cfgSite.ID) {
			work.Add(edge.From)
		}
	}
}

// symbolUsesAndDefinitions reports what one site defines and what it reads,
// both from published effects.
//
// A write also counts as a use when the written symbol needs dropping: the
// pre-assignment drop reads the old value, so the target must stay live up to
// the assignment that replaces it. That is ownership policy and stays here.
func (a *analyzer) symbolUsesAndDefinitions(node *site) (map[*symbols.Symbol]ir.SourceInfo, map[*symbols.Symbol]struct{}) {
	visitor := &livenessEffectVisitor{
		uses:        make(map[*symbols.Symbol]ir.SourceInfo),
		definitions: make(map[*symbols.Symbol]struct{}),
	}
	if a == nil || a.module == nil || node == nil || node.cfgSite == nil {
		return visitor.uses, visitor.definitions
	}
	for _, op := range a.effects[node.cfgSite.ID] {
		effect.Visit(op, visitor)
	}
	return visitor.uses, visitor.definitions
}

type livenessEffectVisitor struct {
	uses        map[*symbols.Symbol]ir.SourceInfo
	definitions map[*symbols.Symbol]struct{}
}

func (v *livenessEffectVisitor) recordUse(sym *symbols.Symbol, at ir.SourceInfo) {
	if sym == nil || at.NodeID == 0 {
		return
	}
	if previous, seen := v.uses[sym]; seen {
		v.uses[sym] = earlierSource(previous, at)
		return
	}
	v.uses[sym] = at
}

func (v *livenessEffectVisitor) VisitDefine(op effect.Define) {
	// A binding that merely arrives at this site was established by the edge
	// into it, so killing liveness here would end a borrow one site too early.
	if !op.IsOnEntry && trackedLiveSymbol(op.Symbol) {
		v.definitions[op.Symbol] = struct{}{}
	}
}

func (v *livenessEffectVisitor) VisitWrite(op effect.Write) {
	if op.Place.Root == nil || !trackedLiveSymbol(op.Place.Root) {
		return
	}
	if len(op.Place.Projections) > 0 {
		v.recordUse(op.Place.Root, ir.SourceInfo{NodeID: ir.NodeID(op.Node), Location: op.Location})
		return
	}
	v.definitions[op.Place.Root] = struct{}{}
	if typ, typed := symbols.GetSymbolType(op.Place.Root); typed && typeinfo.OwnershipCapabilityOf(typ).NeedsDrop {
		v.recordUse(op.Place.Root, ir.SourceInfo{NodeID: ir.NodeID(op.Node), Location: op.Location})
	}
}

func (v *livenessEffectVisitor) VisitUse(op effect.Use) {
	if trackedLiveSymbol(op.Place.Root) {
		v.recordUse(op.Place.Root, ir.SourceInfo{NodeID: ir.NodeID(op.Node), Location: op.Location})
	}
}

func (v *livenessEffectVisitor) VisitBorrow(op effect.Borrow) {
	if trackedLiveSymbol(op.Place.Root) {
		v.recordUse(op.Place.Root, ir.SourceInfo{NodeID: ir.NodeID(op.Node), Location: op.Location})
	}
}

func (*livenessEffectVisitor) VisitIterate(effect.Iterate)     {}
func (*livenessEffectVisitor) VisitDiscard(effect.Discard)     {}
func (*livenessEffectVisitor) VisitCallBegin(effect.CallBegin) {}
func (*livenessEffectVisitor) VisitCallEnd(effect.CallEnd)     {}

// symbolUseSequence returns the symbols this site reads, in evaluation order.
//
// It reads published effects rather than walking the statement itself. The
// walk it replaced enumerated eight statement kinds and missed ForStmt.Iterable,
// which applyStmt does handle, so liveness and borrow-ending saw a different
// program than the effect analysis did. One producer means they cannot disagree.
func (a *analyzer) symbolUseSequence(node *site, include func(*symbols.Symbol) bool) []*symbols.Symbol {
	if a == nil || a.module == nil || node == nil || node.cfgSite == nil || include == nil {
		return nil
	}
	visitor := &useSequenceEffectVisitor{include: include}
	for _, op := range a.effects[node.cfgSite.ID] {
		effect.Visit(op, visitor)
	}
	return visitor.uses
}

type useSequenceEffectVisitor struct {
	include func(*symbols.Symbol) bool
	uses    []*symbols.Symbol
}

func (v *useSequenceEffectVisitor) record(at effect.Place) {
	if v.include(at.Root) {
		v.uses = append(v.uses, at.Root)
	}
}

func (*useSequenceEffectVisitor) VisitDefine(effect.Define)       {}
func (*useSequenceEffectVisitor) VisitWrite(effect.Write)         {}
func (v *useSequenceEffectVisitor) VisitUse(op effect.Use)        { v.record(op.Place) }
func (v *useSequenceEffectVisitor) VisitBorrow(op effect.Borrow)  { v.record(op.Place) }
func (*useSequenceEffectVisitor) VisitIterate(effect.Iterate)     {}
func (*useSequenceEffectVisitor) VisitDiscard(effect.Discard)     {}
func (*useSequenceEffectVisitor) VisitCallBegin(effect.CallBegin) {}
func (*useSequenceEffectVisitor) VisitCallEnd(effect.CallEnd)     {}

func mergeSymbolLiveSets(dst, src map[*symbols.Symbol]ir.SourceInfo) {
	for sym, site := range src {
		if previous, found := dst[sym]; !found {
			dst[sym] = site
		} else {
			dst[sym] = earlierSource(previous, site)
		}
	}
}

func trackedLiveSymbol(sym *symbols.Symbol) bool {
	return referenceHoldingSymbol(sym) || ownershipTrackedSymbol(sym)
}

func earlierSource(left, right ir.SourceInfo) ir.SourceInfo {
	if left.NodeID == 0 {
		return right
	}
	if right.NodeID == 0 {
		return left
	}
	if left.Location == nil || left.Location.Start == nil {
		return right
	}
	if right.Location == nil || right.Location.Start == nil || left.Location.Start.Index <= right.Location.Start.Index {
		return left
	}
	return right
}
