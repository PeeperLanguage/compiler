package ownership

import (
	"maps"
	"slices"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	graphcore "compiler/internal/graph"
	"compiler/internal/ir/cfg"
	"compiler/internal/project"
	"compiler/internal/semantics/effect"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

type loanID struct {
	node      ast.Node
	parameter *symbols.Symbol
}

type referenceLoan struct {
	id      loanID
	origins []place.Origin
	mutable bool
	site    ast.Node
	loop    ast.NodeID
}

type symbolUse struct {
	symbol *symbols.Symbol
	site   ast.Node
}

type loanFact struct {
	loan         referenceLoan
	holder       *symbols.Symbol
	keepingAlive ast.Node
}

type loanContext struct {
	persistent []loanFact
	temporary  []loanFact
	reserved   []loanFact
	remaining  map[*symbols.Symbol]int
	liveOut    map[*symbols.Symbol]ast.Node
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
	for _, use := range a.symbolUseSequence(node, referenceHoldingSymbol) {
		ctx.remaining[use.symbol]++
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

func (ctx *loanContext) addTemporary(value []referenceLoan, call ast.Node) {
	if ctx == nil {
		return
	}
	for _, loan := range value {
		ctx.temporary = append(ctx.temporary, loanFact{loan: loan, keepingAlive: call})
	}
}

func (a *analyzer) checkStorageAccess(
	expr ast.Expr,
	loans *loanContext,
	access storageAccess,
) {
	if a == nil || expr == nil || loans == nil {
		return
	}
	a.reportLoanConflict(
		a.originsForExpr(expr),
		a.referenceHolder(expr),
		access,
		expr,
		loans,
	)
}

func (a *analyzer) reportLoanConflict(
	origins []place.Origin,
	exempt *symbols.Symbol,
	access storageAccess,
	site ast.Node,
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
	diag := a.ctx.Diagnostics.AddError(diagnostics.ErrBorrowConflict, message, ast.LocOf(site), "conflicting access")
	addLoanConflictLabels(diag, conflict, reservedConflict, nil)
}

func (a *analyzer) activateCallReservations(call ast.Node, mark int, loans *loanContext) {
	if a == nil || call == nil || loans == nil || mark >= len(loans.reserved) {
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

		diag := a.ctx.Diagnostics.AddError(
			diagnostics.ErrBorrowConflict,
			"cannot activate mutable borrow while storage is borrowed",
			ast.LocOf(call),
			"mutable borrow activates here",
		)
		if reservation.loan.site != nil {
			diag.WithSecondaryLabel(ast.LocOf(reservation.loan.site), "mutable borrow reserved here")
		}
		addLoanConflictLabels(diag, conflict, reservedConflict, call)
	}
}

func addLoanConflictLabels(
	diag *diagnostics.Diagnostic,
	conflict *loanFact,
	reservationConflict bool,
	excludedKeepingAlive ast.Node,
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
	if conflict.loan.site != nil {
		diag.WithSecondaryLabel(ast.LocOf(conflict.loan.site), borrowKind)
	}
	if conflict.keepingAlive == nil || conflict.keepingAlive == conflict.loan.site ||
		conflict.keepingAlive == excludedKeepingAlive {
		return
	}
	keepingMessage := "borrow remains live until this use"
	if reservationConflict {
		keepingMessage = "mutable borrow activates when this call starts"
	} else if conflict.holder == nil {
		keepingMessage = "borrow remains active until this call completes"
	}
	diag.WithSecondaryLabel(ast.LocOf(conflict.keepingAlive), keepingMessage)
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

func (a *analyzer) referenceHolder(expr ast.Expr) *symbols.Symbol {
	if a == nil || a.module == nil || a.module.Bindings == nil || expr == nil {
		return nil
	}
	if address, taken := expr.(*ast.AddressExpr); taken {
		if address == nil {
			return nil
		}
		expr = address.Expr
	}
	root, _, ok := place.Decompose(expr)
	if !ok {
		return nil
	}
	ident, ok := root.(*ast.Ident)
	if !ok || ident == nil {
		return nil
	}
	sym := a.module.Bindings.NodeSymbols[ident.ID()]
	if _, reference := referenceMutability(sym); reference {
		return sym
	}
	return nil
}

func (a *analyzer) referenceValueForExpr(expr ast.Expr, st state) ([]referenceLoan, bool) {
	if a == nil || expr == nil {
		return []referenceLoan{}, false
	}
	if ident, ok := expr.(*ast.Ident); ok {
		sym := a.module.Bindings.NodeSymbols[ident.ID()]
		if typ, typed := symbols.GetSymbolType(sym); typed && typeinfo.ContainsStoredReference(typ) {
			if value, found := st.references[sym]; found {
				return copyReferenceLoans(value), true
			}
		}
	}
	_, mutable, ok := typeinfo.ReferenceValueTarget(a.exprType(expr))
	if ok {
		origins := a.originsForExpr(expr)
		if len(origins) == 0 {
			return []referenceLoan{}, false
		}
		return []referenceLoan{{
			id:      loanID{node: expr},
			origins: origins,
			mutable: mutable,
			site:    expr,
		}}, true
	}
	if literal, ok := expr.(*ast.StructLit); ok {
		var loans []referenceLoan
		for _, field := range literal.Fields {
			fieldLoans, found := a.referenceValueForExpr(field.Value, st)
			if found {
				loans = append(loans, fieldLoans...)
			}
		}
		return loans, len(loans) > 0
	}
	construction, constructed := a.module.Typechecking.VariantConstructions[expr.ID()]
	if !constructed || construction.Payload == nil {
		return []referenceLoan{}, false
	}
	return a.referenceValueForExpr(construction.Value, st)
}

func (a *analyzer) originsForExpr(expr ast.Expr) []place.Origin {
	if a == nil || a.module == nil || a.module.Flow == nil || expr == nil {
		return nil
	}
	return place.CloneOrigins(a.module.Flow.ResolvedValueOrigins[expr.ID()])
}

func (a *analyzer) validateReferenceReturn(scope *symbols.Scope, stmt *ast.ReturnStmt, st state) {
	if a == nil || a.function == nil || scope == nil || stmt == nil || stmt.Value == nil {
		return
	}
	if _, _, reference := typeinfo.ReferenceValueTarget(a.exprType(stmt.Value)); !reference {
		return
	}
	value, found := a.referenceValueForExpr(stmt.Value, st)
	if !found {
		return
	}
	fnType := typeinfo.FuncTypeFromDeclWithOptions(
		a.function,
		project.TypeSyntaxOptions(a.ctx, a.module, nil, false),
	)
	if fnType == nil || fnType.ReturnOrigins == nil {
		return
	}
	params := a.function.ParamsWithReceiver()
	allowed := make(map[*symbols.Symbol]struct{}, len(fnType.ReturnOrigins.Sources))
	for _, slot := range fnType.ReturnOrigins.Sources {
		if slot < 0 || slot >= len(params) || params[slot].Name == nil {
			continue
		}
		if sym, ok := a.functionScope.LookupNode(params[slot].Name); ok && sym != nil {
			allowed[sym] = struct{}{}
		}
	}
	for _, origin := range referenceOrigins(value) {
		if _, declared := allowed[origin.Root]; declared {
			continue
		}
		diagnostic := a.ctx.Diagnostics.AddError(
			diagnostics.ErrInvalidReturn,
			"returned reference originates outside declared `from` sources",
			ast.LocOf(stmt.Value),
			"undeclared return origin",
		)
		if a.function.ReturnOrigins != nil {
			diagnostic.WithSecondaryLabel(a.function.ReturnOrigins.Location, "declared return origins")
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
			index := referenceLoanIndex(dstValue, srcLoan.id)
			if index < 0 {
				srcLoan.origins = place.CloneOrigins(srcLoan.origins)
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
		index := referenceLoanIndex(right, leftLoan.id)
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

func referenceLoanIndex(loans []referenceLoan, id loanID) int {
	for i := range loans {
		if loans[i].id == id {
			return i
		}
	}
	return -1
}

func (a *analyzer) computeSymbolLiveness() {
	if a == nil || a.sites == nil {
		return
	}
	a.symbolLiveIn = make(map[cfg.SiteID]map[*symbols.Symbol]ast.Node, len(a.order))
	a.symbolLiveOut = make(map[cfg.SiteID]map[*symbols.Symbol]ast.Node, len(a.order))
	work := graphcore.NewWorklist[cfg.SiteID]()
	for _, id := range slices.Backward(a.order) {
		work.Add(id)
	}
	for {
		id, pending := work.Next()
		if !pending {
			break
		}

		out := make(map[*symbols.Symbol]ast.Node)
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
func (a *analyzer) symbolUsesAndDefinitions(node *site) (map[*symbols.Symbol]ast.Node, map[*symbols.Symbol]struct{}) {
	visitor := &livenessEffectVisitor{
		a:           a,
		uses:        make(map[*symbols.Symbol]ast.Node),
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
	a           *analyzer
	uses        map[*symbols.Symbol]ast.Node
	definitions map[*symbols.Symbol]struct{}
}

func (v *livenessEffectVisitor) recordUse(sym *symbols.Symbol, at ast.NodeID) {
	if sym == nil || v.a == nil || v.a.module == nil {
		return
	}
	syntax, found := v.a.module.TypedASTNodes[at]
	if !found {
		return
	}
	if previous, seen := v.uses[sym]; seen {
		v.uses[sym] = earlierNode(previous, syntax)
		return
	}
	v.uses[sym] = syntax
}

func (v *livenessEffectVisitor) VisitDefine(op effect.Define) {
	// A binding that merely arrives at this site was established by the edge
	// into it, so killing liveness here would end a borrow one site too early.
	if !op.OnEntry && trackedLiveSymbol(op.Symbol) {
		v.definitions[op.Symbol] = struct{}{}
	}
}

func (v *livenessEffectVisitor) VisitWrite(op effect.Write) {
	if op.Place.Root == nil || !trackedLiveSymbol(op.Place.Root) {
		return
	}
	v.definitions[op.Place.Root] = struct{}{}
	if typ, typed := symbols.GetSymbolType(op.Place.Root); typed && typeinfo.OwnershipCapabilityOf(typ).Drop {
		v.recordUse(op.Place.Root, op.Node)
	}
}

func (v *livenessEffectVisitor) VisitUse(op effect.Use) {
	if trackedLiveSymbol(op.Place.Root) {
		v.recordUse(op.Place.Root, op.Node)
	}
}

func (v *livenessEffectVisitor) VisitBorrow(op effect.Borrow) {
	if trackedLiveSymbol(op.Place.Root) {
		v.recordUse(op.Place.Root, op.Node)
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
func (a *analyzer) symbolUseSequence(node *site, include func(*symbols.Symbol) bool) []symbolUse {
	if a == nil || a.module == nil || node == nil || node.cfgSite == nil || include == nil {
		return nil
	}
	visitor := &useSequenceEffectVisitor{a: a, include: include}
	for _, op := range a.effects[node.cfgSite.ID] {
		effect.Visit(op, visitor)
	}
	return visitor.uses
}

type useSequenceEffectVisitor struct {
	a       *analyzer
	include func(*symbols.Symbol) bool
	uses    []symbolUse
}

func (v *useSequenceEffectVisitor) record(at effect.Place, node ast.NodeID) {
	if !v.include(at.Root) {
		return
	}
	syntax, found := v.a.module.TypedASTNodes[node]
	if !found {
		return
	}
	v.uses = append(v.uses, symbolUse{symbol: at.Root, site: syntax})
}

func (*useSequenceEffectVisitor) VisitDefine(effect.Define)       {}
func (*useSequenceEffectVisitor) VisitWrite(effect.Write)         {}
func (v *useSequenceEffectVisitor) VisitUse(op effect.Use)        { v.record(op.Place, op.Node) }
func (v *useSequenceEffectVisitor) VisitBorrow(op effect.Borrow)  { v.record(op.Place, op.Node) }
func (*useSequenceEffectVisitor) VisitIterate(effect.Iterate)     {}
func (*useSequenceEffectVisitor) VisitDiscard(effect.Discard)     {}
func (*useSequenceEffectVisitor) VisitCallBegin(effect.CallBegin) {}
func (*useSequenceEffectVisitor) VisitCallEnd(effect.CallEnd)     {}

func mergeSymbolLiveSets(dst, src map[*symbols.Symbol]ast.Node) {
	for sym, site := range src {
		if previous, found := dst[sym]; !found {
			dst[sym] = site
		} else {
			dst[sym] = earlierNode(previous, site)
		}
	}
}

func trackedLiveSymbol(sym *symbols.Symbol) bool {
	return referenceHoldingSymbol(sym) || ownershipTrackedSymbol(sym)
}

func earlierNode(left, right ast.Node) ast.Node {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	leftLoc := ast.LocOf(left)
	rightLoc := ast.LocOf(right)
	if leftLoc == nil || leftLoc.Start == nil {
		return right
	}
	if rightLoc == nil || rightLoc.Start == nil || leftLoc.Start.Index <= rightLoc.Start.Index {
		return left
	}
	return right
}
