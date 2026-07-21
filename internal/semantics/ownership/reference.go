package ownership

import (
	"maps"
	"slices"

	"compiler/internal/constvalue"
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/graph"
	"compiler/internal/project"
	"compiler/internal/semantics/consteval"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
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
}

type referenceUse struct {
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

func (a *analyzer) newLoanContext(node *flowNode, st state) *loanContext {
	ctx := &loanContext{
		remaining: make(map[*symbols.Symbol]int),
		liveOut:   a.referenceLiveOut[node.id],
	}
	for _, use := range a.referenceUseSequence(node) {
		ctx.remaining[use.symbol]++
	}
	for sym, keepingAlive := range a.referenceLiveIn[node.id] {
		value, tracked := st.references[sym]
		if !tracked {
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
	scope *table.Scope,
	expr ast.Expr,
	st state,
	loans *loanContext,
	access storageAccess,
) {
	if a == nil || expr == nil || loans == nil {
		return
	}
	a.reportLoanConflict(
		a.originsForExpr(scope, expr, st),
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

func (a *analyzer) activateCallReservations(call *ast.CallExpr, mark int, loans *loanContext) {
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
	if a == nil || a.module == nil || a.module.Semantics == nil {
		return nil
	}
	for {
		switch node := expr.(type) {
		case *ast.AddressExpr:
			if node == nil {
				return nil
			}
			expr = node.Expr
		case *ast.SelectorExpr:
			if node == nil {
				return nil
			}
			expr = node.Expr
		case *ast.IndexExpr:
			if node == nil {
				return nil
			}
			expr = node.Expr
		case *ast.Ident:
			if node == nil {
				return nil
			}
			sym := a.module.Semantics.ResolvedSymbols[node.ID()]
			if _, reference := referenceMutability(sym); reference {
				return sym
			}
			return nil
		default:
			return nil
		}
	}
}

func (a *analyzer) referenceValueForExpr(scope *table.Scope, expr ast.Expr, st state) ([]referenceLoan, bool) {
	if a == nil || scope == nil || expr == nil {
		return []referenceLoan{}, false
	}
	_, mutable, ok := typeinfo.ReferenceValueTarget(a.exprType(expr))
	if !ok {
		return []referenceLoan{}, false
	}
	if ident, ok := expr.(*ast.Ident); ok {
		sym := a.module.Semantics.ResolvedSymbols[ident.ID()]
		if value, tracked := st.references[sym]; tracked {
			return copyReferenceLoans(value), true
		}
	}
	origins := a.originsForExpr(scope, expr, st)
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

func (a *analyzer) originsForExpr(scope *table.Scope, expr ast.Expr, st state) []place.Origin {
	if a == nil || scope == nil || expr == nil {
		return nil
	}
	return place.Origins(scope, expr, place.OriginOptions{
		ExprType: a.exprType,
		ReferenceOrigins: func(sym *symbols.Symbol) []place.Origin {
			return referenceOrigins(st.references[sym])
		},
		CallOrigins: func(call *ast.CallExpr) []place.Origin {
			return a.callReturnOrigins(scope, call, st)
		},
		ConstantIndex: func(index ast.Expr) (string, bool) {
			expected := a.exprType(index)
			if !typeinfo.IsIntegral(expected) {
				expected = typeinfo.DefaultIntegerType()
			}
			value, evaluated := consteval.EvaluateExpr(a.ctx, a.module, scope, index, expected)
			integer, integral := value.(*constvalue.IntConst)
			if !evaluated || !integral || integer == nil {
				return "", false
			}
			return integer.Value, true
		},
	})
}

func (a *analyzer) callReturnOrigins(scope *table.Scope, call *ast.CallExpr, st state) []place.Origin {
	if a == nil || call == nil || call.Callee == nil {
		return nil
	}
	fnType, _ := typeinfo.Underlying(a.exprType(call.Callee)).(*typeinfo.FuncType)
	if fnType == nil || fnType.ReturnOrigins == nil {
		return nil
	}
	var origins []place.Origin
	for _, source := range typeinfo.ReturnOriginSources(call, fnType) {
		origins = place.MergeOrigins(origins, a.originsForExpr(scope, source, st))
	}
	return origins
}

func (a *analyzer) validateReferenceReturn(scope *table.Scope, stmt *ast.ReturnStmt, st state) {
	if a == nil || a.function == nil || scope == nil || stmt == nil || stmt.Value == nil {
		return
	}
	if _, _, reference := typeinfo.ReferenceValueTarget(a.exprType(stmt.Value)); !reference {
		return
	}
	value, found := a.referenceValueForExpr(scope, stmt.Value, st)
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
	if !reference || !hasValue {
		delete(st.references, sym)
		return
	}
	value = copyReferenceLoans(value)
	for i := range value {
		value[i].mutable = mutable
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
		if leftLoan.mutable != rightLoan.mutable || leftLoan.site != rightLoan.site ||
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

func (a *analyzer) computeReferenceLiveness() {
	if a == nil || a.flow == nil || a.flow.graph == nil {
		return
	}
	a.referenceLiveIn = make(map[graph.NodeID]map[*symbols.Symbol]ast.Node, len(a.flow.order))
	a.referenceLiveOut = make(map[graph.NodeID]map[*symbols.Symbol]ast.Node, len(a.flow.order))
	queue := make([]graph.NodeID, 0, len(a.flow.order))
	queued := make(map[graph.NodeID]bool, len(a.flow.order))
	for _, id := range slices.Backward(a.flow.order) {
		queue = append(queue, id)
		queued[id] = true
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		queued[id] = false

		out := make(map[*symbols.Symbol]ast.Node)
		for _, succ := range a.flow.graph.Successors(id) {
			mergeReferenceLiveSets(out, a.referenceLiveIn[succ])
		}
		uses, definitions := a.referenceUsesAndDefinitions(a.flow.nodes[id])
		in := maps.Clone(out)
		for sym := range definitions {
			delete(in, sym)
		}
		mergeReferenceLiveSets(in, uses)

		if maps.Equal(a.referenceLiveIn[id], in) && maps.Equal(a.referenceLiveOut[id], out) {
			continue
		}
		a.referenceLiveIn[id] = in
		a.referenceLiveOut[id] = out
		for _, pred := range a.flow.graph.Predecessors(id) {
			if !queued[pred] {
				queue = append(queue, pred)
				queued[pred] = true
			}
		}
	}
}

func (a *analyzer) referenceUsesAndDefinitions(node *flowNode) (map[*symbols.Symbol]ast.Node, map[*symbols.Symbol]struct{}) {
	uses := make(map[*symbols.Symbol]ast.Node)
	definitions := make(map[*symbols.Symbol]struct{})
	if a == nil || node == nil || node.kind != nodeStmt || node.stmt == nil {
		return uses, definitions
	}
	addDefinition := func(binding ast.Node) {
		if node.scope == nil || binding == nil {
			return
		}
		if sym, found := node.scope.LookupNode(binding); found {
			if _, reference := referenceMutability(sym); reference {
				definitions[sym] = struct{}{}
			}
		}
	}

	switch stmt := node.stmt.(type) {
	case *ast.LetDecl:
		addDefinition(stmt)
	case *ast.ConstDecl:
		addDefinition(stmt)
	case *ast.AssignStmt:
		if target, ok := stmt.Target.(*ast.Ident); ok && node.scope != nil {
			if sym, found := node.scope.Lookup(target.Name); found {
				if _, reference := referenceMutability(sym); reference {
					definitions[sym] = struct{}{}
				}
			}
		}
	}
	for _, use := range a.referenceUseSequence(node) {
		if previous, found := uses[use.symbol]; !found {
			uses[use.symbol] = use.site
		} else {
			uses[use.symbol] = earlierNode(previous, use.site)
		}
	}
	return uses, definitions
}

func (a *analyzer) referenceUseSequence(node *flowNode) []referenceUse {
	if a == nil || node == nil || node.kind != nodeStmt || node.stmt == nil ||
		a.module == nil || a.module.Semantics == nil {
		return nil
	}
	var expressions []ast.Expr
	switch stmt := node.stmt.(type) {
	case *ast.LetDecl:
		expressions = append(expressions, stmt.Value)
	case *ast.ConstDecl:
		expressions = append(expressions, stmt.Value)
	case *ast.AssignStmt:
		expressions = append(expressions, stmt.Value)
		if _, binding := stmt.Target.(*ast.Ident); !binding {
			expressions = append(expressions, stmt.Target)
		}
	case *ast.ReturnStmt:
		expressions = append(expressions, stmt.Value)
	case *ast.ExprStmt:
		expressions = append(expressions, stmt.Expr)
	case *ast.IfStmt:
		expressions = append(expressions, stmt.Cond)
	case *ast.ForStmt:
		expressions = append(expressions, stmt.Cond)
	}

	var uses []referenceUse
	for _, expr := range expressions {
		if expr == nil {
			continue
		}
		ast.Inspect(expr, func(current ast.Node) bool {
			ident, ok := current.(*ast.Ident)
			if !ok || ident == nil {
				return true
			}
			sym := a.module.Semantics.ResolvedSymbols[ident.ID()]
			if _, reference := referenceMutability(sym); reference {
				uses = append(uses, referenceUse{symbol: sym, site: ident})
			}
			return true
		})
	}
	return uses
}

func mergeReferenceLiveSets(dst, src map[*symbols.Symbol]ast.Node) {
	for sym, site := range src {
		if previous, found := dst[sym]; !found {
			dst[sym] = site
		} else {
			dst[sym] = earlierNode(previous, site)
		}
	}
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
