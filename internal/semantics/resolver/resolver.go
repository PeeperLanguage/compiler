package resolver

import (
	"fmt"
	"maps"
	"slices"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/module"
	"compiler/internal/problems"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
)

type resolver struct {
	ctx             *project.CompilerContext
	module          *module.Module
	pendingBindings map[symbols.SymbolID]struct{}
	// assigned holds the payload names assigned on the way to the statement
	// being resolved: the assignment went to the variable, and from there on
	// the name reads the variable too. Nil until a name is assigned.
	assigned map[*symbols.Symbol]struct{}
}

func (r *resolver) resolveModule() {
	if r == nil || r.module == nil || r.module.AST == nil {
		return
	}
	if r.module.SymbolIndex == nil {
		r.module.SymbolIndex = symbols.NewIndex()
	}
	r.markPendingTopLevelBindings()
	ast.ForEachDecl(r.module.AST, func(decl ast.Decl) bool {
		switch node := decl.(type) {
		case *ast.LetDecl:
			r.resolveTopLevelBinding(node.Name, node.Value)
		case *ast.ConstDecl:
			r.resolveTopLevelBinding(node.Name, node.Value)
		}
		return true
	})
	ast.ForEachDecl(r.module.AST, func(decl ast.Decl) bool {
		switch node := decl.(type) {
		case *ast.FnDecl:
			r.resolveFunction(node)
		}
		return true
	})
}

func (r *resolver) markPendingTopLevelBindings() {
	if r == nil || r.module == nil || r.module.ModuleScope == nil {
		return
	}
	for _, sym := range r.module.ModuleScope.Symbols() {
		if sym == nil {
			continue
		}
		switch sym.Kind {
		case symbols.SymbolVar, symbols.SymbolConst:
			r.pendingBindings[sym.ID] = struct{}{}
		}
	}
}

func (r *resolver) resolveTopLevelBinding(name *ast.Ident, value ast.Expr) {
	if r == nil || r.module == nil || r.module.ModuleScope == nil || name == nil || name.Name == "" {
		return
	}
	sym, ok := r.module.ModuleScope.LookupLocal(name.Name)
	if !ok || sym == nil {
		return
	}
	if value != nil {
		r.resolveExpr(r.module.ModuleScope, value)
	}
	delete(r.pendingBindings, sym.ID)
}

func (r *resolver) resolveFunction(fn *ast.FnDecl) {
	if r == nil || r.module == nil || fn == nil {
		return
	}
	sym := r.module.SymbolIndex.Symbol(fn.Name)
	if sym == nil || sym.Scope == nil {
		return
	}
	funcScope := sym.Scope
	params := fn.ParamsWithReceiver()
	for i, param := range params {
		if param.Name == nil || param.Name.Name == "" {
			if fn.Body != nil {
				r.ctx.Diagnostics.AddError(diagnostics.ErrMissingIdentifier, "parameter name required", param.Location, "")
				return
			}
			continue
		}
		// Resolve each default before declaring its parameter. This makes
		// receiver and earlier parameters visible while rejecting self and
		// later-parameter references at the declaration boundary.
		if param.Default != nil {
			ast.Inspect(param.Default, func(node ast.Node) bool {
				if fallback, isOrElse := node.(*ast.OrElseExpr); isOrElse && fallback.Block != nil {
					r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression,
						"a default value cannot leave the function", ast.LocOf(fallback.Block), "give `orelse` a fallback value")
				}
				return true
			})
			r.resolveExpr(funcScope, param.Default)
		}
		paramSym := symbols.New(symbols.SourceSymbolID(param.Name.ID()), param.Name.Name, symbols.SymbolParam, param.Name, ast.LocOf(param.Name))
		paramSym.AllowsMutation = param.IsMutable
		paramSym.MutableLocation = param.MutableLocation
		paramSym.IsReceiver = fn.Receiver != nil && i == 0
		if err := funcScope.Declare(paramSym); err != nil {
			problems.ReportRedeclaration(r.ctx.Diagnostics, funcScope, err.Error(), param.Name.Name, param.Name.Location)
			return
		}
		r.module.SymbolIndex.Bind(param.Name, paramSym)
	}
	if fn.ReturnOrigins != nil {
		for _, origin := range fn.ReturnOrigins.Sources {
			if origin == nil {
				continue
			}
			name := origin.Name
			if name == "self" && fn.Receiver != nil && fn.Receiver.Name != nil {
				name = fn.Receiver.Name.Name
			}
			if source, ok := funcScope.Lookup(name); ok && source != nil && source.Kind == symbols.SymbolParam {
				r.module.SymbolIndex.Bind(origin, source)
				r.module.SymbolIndex.MarkUsed(source)
			}
		}
	}
	if fn.Body != nil {
		r.resolveBlock(funcScope, fn.Body)
	}
}

func (r *resolver) resolveBlock(scope *symbols.Scope, block *ast.BlockStmt) {
	if block == nil {
		return
	}
	r.module.SymbolIndex.SetScope(block, scope)
	for _, stmt := range block.Stmts {
		r.resolveStmt(scope, stmt)
		// After a guard that leaves when its subject holds no payload, the rest
		// of the block reads the payload under the subject's name. The rest gets
		// its own scope, kept under the guard, for that name to live in.
		guard, isIf := stmt.(*ast.IfStmt)
		if !isIf || guard.Else != nil || !alwaysLeaves(guard.Then) {
			continue
		}
		if subject, _, otherwise := r.testedPayloads(guard.Cond); otherwise {
			rest := symbols.NewScope(scope)
			if r.declarePayloadName(scope, rest, guard, subject) {
				// Declared first: the payload name is the one name that may
				// repeat a name of the block.
				rest.ContinueParent()
				r.module.SymbolIndex.SetScope(guard, rest)
				scope = rest
			}
		}
	}
}

func (r *resolver) resolveStmt(scope *symbols.Scope, stmt ast.Stmt) {
	if stmt == nil {
		return
	}
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		r.resolveBlock(symbols.NewScope(scope), node)
	case *ast.LetDecl:
		r.resolveLocalBinding(scope, node.Name, symbols.SymbolVar, node.Value, node, node.Location)
	case *ast.ConstDecl:
		r.resolveLocalBinding(scope, node.Name, symbols.SymbolConst, node.Value, node, node.Location)
	case *ast.DestructureStmt:
		// The value cannot name the locals it is taken apart into.
		r.resolveExpr(scope, node.Value)
		for _, field := range node.Fields {
			r.resolveLocalBinding(scope, field.Binding, symbols.SymbolVar, nil, field.Binding, field.Location)
		}
	case *ast.ReturnStmt:
		if node.Value != nil {
			r.resolveExpr(scope, node.Value)
		}
	case *ast.IfStmt:
		if node.Cond == nil {
			r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidStatement, "if condition required", ast.LocOf(node), "")
			return
		}
		r.resolveExpr(scope, node.Cond)
		subject, then, otherwise := r.testedPayloads(node.Cond)
		thenScope := symbols.NewScope(scope)
		if then {
			r.declarePayloadName(scope, thenScope, node.Then, subject)
		}
		r.resolveBranches(2, func(branch int) {
			if branch == 0 {
				r.resolveBlock(thenScope, node.Then)
				return
			}
			elseBlock, ok := node.Else.(*ast.BlockStmt)
			if !ok {
				r.resolveStmt(scope, node.Else)
				return
			}
			elseScope := symbols.NewScope(scope)
			if otherwise {
				r.declarePayloadName(scope, elseScope, elseBlock, subject)
			}
			r.resolveBlock(elseScope, elseBlock)
		})
	case *ast.ForStmt:
		if node.Iterable != nil {
			r.resolveExpr(scope, node.Iterable)
			bodyScope := symbols.NewScope(scope)
			if node.Index != nil {
				r.resolveLocalBinding(bodyScope, node.Index, symbols.SymbolVar, nil, node.Index, ast.LocOf(node.Index))
			}
			if node.Value != nil {
				r.resolveLocalBinding(bodyScope, node.Value, symbols.SymbolVar, nil, node.Value, ast.LocOf(node.Value))
			}
			r.resolveBlock(bodyScope, node.Body)
			return
		}
		bodyScope := symbols.NewScope(scope)
		if node.Cond != nil {
			r.resolveExpr(scope, node.Cond)
			// Each pass runs after the test, so the body reads a tested
			// variable's payload. (A borrowed subject would need a reference
			// made on every pass; write the test inside the loop for that.)
			subject, then, _ := r.testedPayloads(node.Cond)
			if _, borrowed := subject.(*ast.AddressExpr); then && !borrowed && node.Body != nil {
				r.declarePayloadName(scope, bodyScope, node.Body, subject)
			}
		}
		r.resolveBlock(bodyScope, node.Body)
	case *ast.BreakStmt, *ast.ContinueStmt:
		// Legality (inside a loop) is diagnosed by the typechecker.
	case *ast.MatchStmt:
		r.resolveExpr(scope, node.Subject)
		r.resolveBranches(len(node.Arms), func(index int) {
			arm := node.Arms[index]
			casePaths := []*ast.ScopeResolution{arm.Case}
			if arm.Pattern != nil {
				casePaths = append(casePaths, arm.Pattern.Case)
			}
			for _, casePath := range casePaths {
				if casePath != nil && !r.resolveVariantPath(scope, casePath) {
					r.resolveScopeResolution(casePath, false)
				}
			}
			// An arm reads the payload under the names its pattern declares,
			// or with none under the subject's name.
			armScope := symbols.NewScope(scope)
			names := arm.Pattern.Bindings()
			for _, name := range names {
				r.resolveLocalBinding(armScope, name, symbols.SymbolVar, nil, name, ast.LocOf(name))
			}
			if len(names) == 0 && r.armHasPayload(arm) {
				r.declarePayloadName(scope, armScope, arm, node.Subject)
			}
			r.resolveBlock(armScope, arm.Body)
		})
	case *ast.ExprStmt:
		r.resolveExpr(scope, node.Expr)
	case *ast.AssignStmt:
		// The value is read before the target is written: it still sees a
		// payload name that the assignment ends.
		r.resolveExpr(scope, node.Value)
		r.resolveAssignTarget(scope, node.Target)
	case *ast.BadStmt, *ast.BadDecl, *ast.ImportDecl, *ast.FnDecl,
		*ast.TypeAliasDecl, *ast.StructDecl, *ast.InterfaceDecl, *ast.EnumDecl:
		r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidStatement, "unsupported statement", ast.LocOf(node), "")
	default:
		panic(fmt.Sprintf("resolver: unhandled statement %T", stmt))
	}
}

// alwaysLeaves reports whether control never reaches the end of stmt: it ends
// in `return`, `break` or `continue` on every path.
func alwaysLeaves(stmt ast.Stmt) bool {
	switch node := stmt.(type) {
	case *ast.ReturnStmt, *ast.BreakStmt, *ast.ContinueStmt:
		return true
	case *ast.BlockStmt:
		return node != nil && len(node.Stmts) > 0 && alwaysLeaves(node.Stmts[len(node.Stmts)-1])
	case *ast.IfStmt:
		return node.Else != nil && alwaysLeaves(node.Then) && alwaysLeaves(node.Else)
	case *ast.MatchStmt:
		if len(node.Arms) == 0 {
			return false
		}
		for _, arm := range node.Arms {
			if !alwaysLeaves(arm.Body) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// resolveBranches resolves the count branches of one statement. Each starts
// from the payload names assigned before the statement; after it, a name
// assigned in any branch counts as assigned.
func (r *resolver) resolveBranches(count int, resolve func(branch int)) {
	before := r.assigned
	after := maps.Clone(before)
	for branch := range count {
		r.assigned = maps.Clone(before)
		resolve(branch)
		if len(r.assigned) > len(before) {
			if after == nil {
				after = make(map[*symbols.Symbol]struct{})
			}
			maps.Copy(after, r.assigned)
		}
	}
	r.assigned = after
}

// declarePayloadName declares the name under which a block reads the payload
// of subject, for a block that runs only when subject holds one case. scope
// is where the test stands, into the block's scope, and owner what the name
// is bound to: the arm, the block, or for the rest of a block the guard.
//
// A tested constant or local variable is read in place under its own name; flow analysis
// verifies each such read, as the block can still change the variable (by
// assigning the name, or by handing the whole variable to a call). A borrowed
// subject (`&place`) gets a reference named after its last field. Any other
// subject gets no name.
func (r *resolver) declarePayloadName(scope, into *symbols.Scope, owner ast.Node, subject ast.Expr) bool {
	// The name is the subject's own, or the last one on its path:
	// `&holder.items[0]` gives `items`.
	address, borrowed := subject.(*ast.AddressExpr)
	if borrowed {
		subject = address.Expr
	}
	var name *ast.Ident
	for part := subject; name == nil; {
		switch named := part.(type) {
		case *ast.Ident:
			name = named
		case *ast.SelectorExpr:
			name = named.Name
		case *ast.IndexExpr:
			part = named.Expr
		default:
			return false
		}
	}
	outside, _ := scope.Lookup(name.Name)
	if _, isVariable := subject.(*ast.Ident); !borrowed && (!isVariable || !r.keepsItsCase(outside)) {
		return false
	}
	sym := symbols.New(symbols.SourceSymbolID(owner.ID()), name.Name, symbols.SymbolVar, owner, ast.LocOf(name))
	sym.IsPayloadName = true
	if !borrowed {
		// It is the same variable under a narrower type.
		sym.AliasOf, sym.Kind, sym.AllowsMutation = outside, outside.Kind, outside.AllowsMutation
	}
	if err := into.Declare(sym); err != nil {
		panic(err)
	}
	r.module.SymbolIndex.Bind(owner, sym)
	return true
}

// keepsItsCase reports whether only the function being resolved can change
// sym between a test and a read: a constant, or a variable or parameter of the
// function. A module variable can be changed by any call.
func (r *resolver) keepsItsCase(sym *symbols.Symbol) bool {
	if sym == nil || sym.Kind != symbols.SymbolVar && sym.Kind != symbols.SymbolParam {
		return sym != nil && sym.Kind == symbols.SymbolConst
	}
	global, isGlobal := r.module.ModuleScope.LookupLocal(sym.Name)
	return !isGlobal || global != sym
}

// testedPayloads reads a condition that tests which case its subject holds
// (`subject is Enum::Case`, `subject != none`, `subject == none`) and reports
// whether the subject holds a payload when the test is true (then) and when
// it is false (otherwise). Any other condition has no subject.
func (r *resolver) testedPayloads(cond ast.Expr) (subject ast.Expr, then, otherwise bool) {
	isNegated := false
	for not, isNot := cond.(*ast.UnaryExpr); isNot && not.Op == "!"; not, isNot = cond.(*ast.UnaryExpr) {
		cond, isNegated = not.Expr, !isNegated
	}
	switch test := cond.(type) {
	case *ast.IsExpr:
		subject = test.Value
		then, otherwise = r.casePayloads(test.Case)
	case *ast.BinaryExpr:
		_, leftIsNone := test.Left.(*ast.NoneLit)
		_, rightIsNone := test.Right.(*ast.NoneLit)
		if leftIsNone == rightIsNone || test.Op != "==" && test.Op != "!=" {
			return nil, false, false
		}
		subject = test.Left
		if leftIsNone {
			subject = test.Right
		}
		then, otherwise = test.Op == "!=", test.Op == "=="
	}
	if isNegated {
		then, otherwise = otherwise, then
	}
	return subject, then, otherwise
}

// armHasPayload reports whether what arm selects carries a payload: its case,
// or with a pattern that tests the payload the payload case tested.
func (r *resolver) armHasPayload(arm *ast.MatchArm) bool {
	switch {
	case arm.Pattern == nil:
		has, _ := r.casePayloads(arm.Case)
		return has
	case arm.Pattern.Case != nil:
		has, _ := r.casePayloads(arm.Pattern.Case)
		return has
	default:
		return false
	}
}

// casePayloads reports whether the enum case path names carries a payload,
// and whether the only other case of a two-case enum does.
func (r *resolver) casePayloads(path *ast.ScopeResolution) (has, otherHas bool) {
	if path == nil {
		return false, false
	}
	variant := r.module.SymbolIndex.Symbol(path)
	if variant == nil || variant.Kind != symbols.SymbolVariant {
		return false, false
	}
	descriptor, _ := typeinfo.VariantDescriptorOf(variant.Type)
	index := slices.IndexFunc(descriptor.Cases, func(candidate typeinfo.VariantCase) bool { return candidate.Name == variant.Name })
	if index < 0 {
		return false, false
	}
	return descriptor.Cases[index].Payload != nil, len(descriptor.Cases) == 2 && descriptor.Cases[1-index].Payload != nil
}

func (r *resolver) resolveLocalBinding(scope *symbols.Scope, name *ast.Ident, kind symbols.Kind, value ast.Expr, node ast.Node, loc *source.Location) {
	sym := symbols.New(symbols.SourceSymbolID(name.ID()), name.Name, kind, node, ast.LocOf(name))
	if err := scope.Declare(sym); err != nil {
		problems.ReportRedeclaration(r.ctx.Diagnostics, scope, err.Error(), name.Name, loc)
		return
	}
	r.module.SymbolIndex.Bind(name, sym)
	r.pendingBindings[sym.ID] = struct{}{}
	if value != nil {
		r.resolveExpr(scope, value)
	}
	delete(r.pendingBindings, sym.ID)
}

func (r *resolver) resolveExpr(scope *symbols.Scope, expr ast.Expr) {
	if expr == nil {
		return
	}
	switch node := expr.(type) {
	case *ast.NumberLit:
		return
	case *ast.StringLit:
		return
	case *ast.ByteLit:
		return
	case *ast.CharLit:
		return
	case *ast.BoolLit:
		return
	case *ast.NoneLit:
		return
	case *ast.Ident:
		sym, ok := scope.Lookup(node.Name)
		// Once assigned, the name reads the variable again, not the payload
		// the variable held before.
		for ok && sym != nil && sym.AliasOf != nil {
			if _, assigned := r.assigned[sym]; !assigned {
				break
			}
			sym = sym.AliasOf
		}
		if ok && sym != nil {
			r.module.SymbolIndex.Bind(node, sym)
			r.module.SymbolIndex.MarkUsed(sym)
			if sym.Kind == symbols.SymbolImport {
				r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression, "import alias must be qualified with `::`", ast.LocOf(node), "")
				return
			}
			if _, pending := r.pendingBindings[sym.ID]; pending {
				msg := "symbol `" + node.Name + "` used before it's defined"
				r.ctx.Diagnostics.Add(
					diagnostics.NewError(msg).
						WithCode(diagnostics.ErrUseBeforeDecl).
						WithPrimaryLabel(ast.LocOf(node), msg).
						Help("rename binding or use earlier value"),
				)
				return
			}
			return
		}
		reportUnresolved(scope, node, r.ctx.Diagnostics)
	case *ast.ScopeResolution:
		if r.resolveVariantPath(scope, node) {
			return
		}
		if r.resolveScopeResolution(node, false) {
			return
		}
	case *ast.SelectorExpr:
		r.resolveExpr(scope, node.Expr)
	case *ast.IndexExpr:
		r.resolveExpr(scope, node.Expr)
		r.resolveExpr(scope, node.Index)
	case *ast.RangeExpr:
		r.resolveExpr(scope, node.Start)
		r.resolveExpr(scope, node.End)
	case *ast.StructLit:
		if scopedType, ok := node.Type.(*ast.ScopeResolution); ok {
			r.resolveScopeResolution(scopedType, true)
		}
		for _, field := range node.Fields {
			r.resolveExpr(scope, field.Value)
		}
	case *ast.VariantLit:
		if !r.resolveVariantPath(scope, node.Case) {
			r.resolveScopeResolution(node.Case, false)
		}
		r.resolveExpr(scope, node.Payload)
	case *ast.ArrayLit:
		if scopedType, ok := node.Type.(*ast.ScopeResolution); ok {
			r.resolveScopeResolution(scopedType, true)
		}
		for _, value := range node.Values {
			r.resolveExpr(scope, value)
		}
	case *ast.UnaryExpr:
		r.resolveExpr(scope, node.Expr)
	case *ast.AddressExpr:
		r.resolveExpr(scope, node.Expr)
	case *ast.BinaryExpr:
		r.resolveExpr(scope, node.Left)
		r.resolveExpr(scope, node.Right)
	case *ast.OrElseExpr:
		r.resolveExpr(scope, node.Value)
		r.resolveExpr(scope, node.Fallback)
		r.resolveBlock(symbols.NewScope(scope), node.Block)
		if node.Block != nil && !alwaysLeaves(node.Block) {
			r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidStatement,
				"the block after `orelse` must leave", ast.LocOf(node.Block), "end it with `return`, `break` or `continue`")
		}
	case *ast.IsExpr:
		r.resolveExpr(scope, node.Value)
		if !r.resolveVariantPath(scope, node.Case) {
			r.resolveScopeResolution(node.Case, false)
		}
	case *ast.CallExpr:
		r.resolveExpr(scope, node.Callee)
		for _, arg := range node.Args {
			r.resolveExpr(scope, arg)
		}
	case *ast.FreeExpr:
		r.resolveExpr(scope, node.Expr)
	case *ast.PrintExpr:
		r.resolveExpr(scope, node.Expr)
	case *ast.AsExpr:
		r.resolveExpr(scope, node.Expr)
	case *ast.BadExpr:
		return // Parser already diagnosed this error node.
	default:
		panic(fmt.Sprintf("resolver: unhandled expression %T", expr))
	}
}

func Resolve(ctx *project.CompilerContext, module *module.Module) {
	if module == nil || ctx == nil {
		return
	}
	r := &resolver{module: module, ctx: ctx, pendingBindings: make(map[symbols.SymbolID]struct{})}
	r.resolveModule()
}

func (r *resolver) resolveAssignTarget(scope *symbols.Scope, expr ast.Expr) {
	switch node := expr.(type) {
	case *ast.Ident:
		sym, ok := scope.Lookup(node.Name)
		// Assigning a payload name assigns its variable, and ends the name:
		// the new value may hold another case.
		for ok && sym != nil && sym.AliasOf != nil {
			if r.assigned == nil {
				r.assigned = make(map[*symbols.Symbol]struct{})
			}
			r.assigned[sym] = struct{}{}
			sym = sym.AliasOf
		}
		if ok && sym != nil {
			r.module.SymbolIndex.Bind(node, sym)
			r.module.SymbolIndex.MarkUsed(sym)
			return
		}
		reportUnresolved(scope, node, r.ctx.Diagnostics)
	case *ast.SelectorExpr:
		r.resolveExpr(scope, node.Expr)
	default:
		r.resolveExpr(scope, expr)
	}
}

func (r *resolver) resolveScopeResolution(node *ast.ScopeResolution, allowTypeArguments bool) bool {
	if r == nil || r.module == nil || node == nil {
		return false
	}
	qualifierNode, memberNode, imported := node.ImportMember()
	if !imported {
		r.ctx.Diagnostics.AddError(diagnostics.ErrUndefinedSymbol, "unsupported qualified path `"+node.TypeText()+"`", ast.LocOf(node), "qualified values currently use `module::member`")
		return false
	}
	if !allowTypeArguments && len(node.Segments[1].TypeArgs) != 0 {
		r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidType, "type arguments are not allowed on value paths", ast.LocOf(node), "generic functions and values are not supported")
		return false
	}
	resolved, ok := r.lookupImportedMember(qualifierNode, memberNode, node)
	if !ok {
		return false
	}
	r.module.SymbolIndex.Bind(node, resolved)
	return true
}

func (r *resolver) resolveVariantPath(scope *symbols.Scope, path *ast.ScopeResolution) bool {
	typePath, caseName, variantPath := path.EnumVariantMember()
	if !variantPath {
		return false
	}

	var qualifierSymbol *symbols.Symbol
	var enumName *ast.Ident
	switch enumPath := typePath.(type) {
	case *ast.NamedType:
		enumName = path.Segments[0].Name
		resolved, ok := scope.Lookup(enumPath.Name)
		if !ok || resolved == nil || resolved.Kind != symbols.SymbolType {
			return false
		}
		qualifierSymbol = resolved
	case *ast.AppliedType:
		enumName = enumPath.Name
		resolved, ok := scope.Lookup(enumPath.Name.Name)
		if !ok || resolved == nil || resolved.Kind != symbols.SymbolType {
			return false
		}
		qualifierSymbol = resolved
	case *ast.ScopeResolution:
		qualifier, member, ok := enumPath.ImportMember()
		if !ok {
			return false
		}
		enumName = member
		resolved, found := r.lookupImportedMember(qualifier, member, path)
		if !found {
			return true
		}
		qualifierSymbol = resolved
	default:
		return false
	}

	qualifierType, _ := symbols.GetSymbolType(qualifierSymbol)
	_, enumSymbol, declaredEnum := r.ctx.TypeResolver.CanonicalEnumDeclaration(qualifierType)
	if !declaredEnum {
		r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression, "variant qualifier must resolve to a named enum", ast.LocOf(enumName), "use an enum declaration or transparent enum alias")
		return true
	}
	variant, ok := enumSymbol.Scope.LookupLocal(caseName.Name)
	if !ok || variant == nil || variant.Kind != symbols.SymbolVariant {
		r.ctx.Diagnostics.AddError(diagnostics.ErrUndefinedSymbol, "unknown variant `"+caseName.Name+"` in enum `"+enumSymbol.Name+"`", ast.LocOf(caseName), "")
		return true
	}
	r.module.SymbolIndex.MarkUsed(qualifierSymbol)
	r.module.SymbolIndex.MarkUsed(enumSymbol)
	r.module.SymbolIndex.MarkUsed(variant)
	r.module.SymbolIndex.Bind(enumName, qualifierSymbol)
	r.module.SymbolIndex.Bind(path, variant)
	r.module.SymbolIndex.Bind(caseName, variant)
	return true
}

func (r *resolver) lookupImportedMember(qualifierNode, memberNode *ast.Ident, site ast.Node) (*symbols.Symbol, bool) {
	qualifier := qualifierNode.Name
	member := memberNode.Name
	resolved, ok := r.ctx.TypeResolver.LookupImportedSymbol(r.module, qualifier, member)
	if !ok || resolved.Symbol == nil {
		if _, exists := r.module.Imports[qualifier]; !exists {
			r.ctx.Diagnostics.AddError(diagnostics.ErrModuleNotFound, "unknown import alias `"+qualifier+"`", ast.LocOf(site), "")
		} else if resolved.Module == nil || resolved.Module.ModuleScope == nil {
			r.ctx.Diagnostics.AddError(diagnostics.ErrModuleNotFound, "imported module not loaded for `"+qualifier+"`", ast.LocOf(site), "")
		} else {
			r.ctx.Diagnostics.AddError(diagnostics.ErrUndefinedSymbol, "unknown identifier `"+member+"` in module `"+qualifier+"`", ast.LocOf(site), "")
		}
		return nil, false
	}
	if !resolved.Symbol.IsPub {
		r.ctx.Diagnostics.AddError(diagnostics.ErrSymbolNotExported, "`"+member+"` is not exported from `"+qualifier+"`", ast.LocOf(site), "use of unexported symbol").
			WithSecondaryLabel(resolved.Symbol.Location, "defined here").
			Note("symbols with uppercase are exported otherwise private")
		r.reportGlobalQualifier(resolved.Symbol, qualifier, site)
		return nil, false
	}
	r.module.RecordImportedUse(qualifier, resolved.Symbol)
	r.reportGlobalQualifier(resolved.Symbol, qualifier, site)
	return resolved.Symbol, true
}

// reportGlobalQualifier notes when a qualified name is the exact symbol already
// injected into global scope, so the `alias::` prefix adds nothing. Comparing
// symbol identity rather than name keeps a same-named unrelated export quiet.
func (r *resolver) reportGlobalQualifier(sym *symbols.Symbol, qualifier string, site ast.Node) {
	if sym == nil || r.ctx == nil || r.ctx.GlobalScope == nil {
		return
	}
	global, found := r.ctx.GlobalScope.Lookup(sym.Name)
	if !found || global == nil || global.ID != sym.ID {
		return
	}
	r.ctx.Diagnostics.Add(diagnostics.NewInfo("`"+sym.Name+"` is already in scope without `"+qualifier+"::`").
		WithCode(diagnostics.InfoRedundantGlobalQualifier).
		WithPrimaryLabel(ast.LocOf(site), "drop the `"+qualifier+"::` prefix").
		Note("global symbols are always in scope"))
}
