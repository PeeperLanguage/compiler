package effect

import (
	"fmt"

	"compiler/internal/frontend/ast"
	"compiler/internal/ir/cfg"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

// BuildQueries supplies the published facts this producer needs. Like
// cfg.BuildQueries, it declares narrow accessors so this package does not
// import the artifacts that own them.
type BuildQueries struct {
	// Symbols resolves a referenced identifier to its binding. The resolver
	// indexes references only, so a declaration name and an assignment target
	// are absent here and resolve through Scopes instead.
	Symbols map[ast.NodeID]*symbols.Symbol
	// Scopes resolves a CFG site's lexical scope, which is how a definition
	// reaches its symbol. Definitions and references genuinely use two different
	// mechanisms today; this producer reproduces that rather than changing it.
	Scopes map[ast.NodeID]*symbols.Scope
	// CallArguments returns a call's effective arguments, including any the
	// typechecker expanded from a default.
	CallArguments func(*ast.CallExpr) []ast.Expr
	// ArmBindings returns the payload symbols one match arm binds.
	ArmBindings func(match ast.NodeID, caseIndex int) []*symbols.Symbol
	// StringConcatenation reports a binary expression the typechecker resolved
	// as string concatenation, which consumes its left operand.
	StringConcatenation func(ast.NodeID) bool
	// ValueUse returns the typechecker's decision for one expression, where it
	// made one. Call arguments have one; most positions do not, and the walk
	// decides those from the position itself.
	ValueUse func(ast.NodeID) (typeinfo.UseKind, bool)
	// ExprType is the flow-refined type of an expression, which decides whether
	// a slice borrows mutably.
	ExprType func(ast.NodeID) typeinfo.Type
	// ReferenceArgument reports an argument whose parameter is a reference, and
	// whether that reference is mutable. The borrow exists because of the
	// parameter type rather than because the source wrote an ampersand.
	ReferenceArgument func(ast.NodeID) (mutable bool, found bool)
}

// Build publishes the semantic effects of every reachable CFG site.
//
// This is the only place that inspects syntax to decide what a construct does
// to a binding. It runs after flow typing so published evidence is final.
func Build(graphs *cfg.Module, nodes map[ast.NodeID]ast.Node, queries BuildQueries) Result {
	if graphs == nil || queries.Symbols == nil || queries.Scopes == nil {
		return nil
	}
	result := make(Result, len(graphs.Functions))
	for _, graph := range graphs.Functions {
		if graph == nil {
			continue
		}
		fn, _ := nodes[ast.NodeID(graph.NodeID)].(*ast.FnDecl)
		if fn == nil {
			continue
		}
		b := &builder{nodes: nodes, queries: queries, graph: graph, ops: make(SiteOps)}
		b.buildFunction(fn)
		result[graph.NodeID] = b.ops
	}
	return result
}

type builder struct {
	nodes   map[ast.NodeID]ast.Node
	queries BuildQueries
	graph   *cfg.Graph
	ops     SiteOps
}

func (b *builder) emit(site cfg.SiteID, op Op) {
	b.ops[site] = append(b.ops[site], op)
}

func (b *builder) buildFunction(fn *ast.FnDecl) {
	if b.graph.Entry == nil || len(b.graph.Entry.Sites) == 0 {
		return
	}
	entry := b.graph.Entry.Sites[0].ID
	if fn.Body != nil {
		functionScope := b.queries.Scopes[fn.Body.ID()]
		for _, param := range fn.ParamsWithReceiver() {
			if param.Name == nil {
				continue
			}
			sym, found := functionScope.Lookup(param.Name.Name)
			if !found || sym == nil {
				continue
			}
			b.emit(entry, Define{Symbol: sym, Node: param.Name.ID(), Initialized: true, OnEntry: true})
		}
	}
	// Bindings that a site inherits on entry are published before that site's
	// own effects, because a site's operations are read in evaluation order and
	// an arm body may read the payload it binds.
	b.eachSite(func(block *cfg.Block, site *cfg.Site) {
		if site.Kind != cfg.SiteTerminator {
			return
		}
		if terminator, ok := block.Terminator.(*cfg.SwitchVariant); ok {
			b.buildMatchArms(site, terminator)
		}
	})
	b.eachSite(b.buildSite)
}

func (b *builder) eachSite(visit func(*cfg.Block, *cfg.Site)) {
	for _, block := range b.graph.Blocks {
		if block == nil || !block.Reachable {
			continue
		}
		for _, site := range block.Sites {
			if site == nil {
				continue
			}
			visit(block, site)
		}
	}
}

func (b *builder) buildSite(block *cfg.Block, site *cfg.Site) {
	stmt, _ := b.nodes[ast.NodeID(site.NodeID)].(ast.Stmt)
	if stmt != nil {
		b.publishStmt(site.ID, b.queries.Scopes[ast.NodeID(site.ScopeID)], stmt)
	}
	if site.Kind != cfg.SiteTerminator {
		return
	}
	switch terminator := block.Terminator.(type) {
	case *cfg.Branch:
		if condition, ok := b.nodes[ast.NodeID(terminator.ConditionID)].(ast.Expr); ok {
			b.value(site.ID, condition, typeinfo.UseRead)
		}
	case *cfg.SwitchVariant:
		// Arm payload bindings are published in the leading pass above; the
		// subject itself is read at the match statement's own site.
	case *cfg.Jump, *cfg.Return:
		// A jump carries no condition, and a return's value is read at the
		// return statement's own site.
	default:
		panic(fmt.Sprintf("effect: unhandled CFG terminator %T", block.Terminator))
	}
}

// buildMatchArms publishes each arm's payload bindings at the first site of
// that arm's body. CFG construction gives every arm a fresh block reached only
// by its own case edge, so a site-keyed define is equivalent to attaching it to
// the edge.
func (b *builder) buildMatchArms(site *cfg.Site, terminator *cfg.SwitchVariant) {
	if b.queries.ArmBindings == nil {
		return
	}
	for _, edge := range site.Successors {
		if edge.Kind != cfg.EdgeVariantCase {
			continue
		}
		for _, sym := range b.queries.ArmBindings(ast.NodeID(terminator.NodeID), edge.Case) {
			if sym == nil {
				continue
			}
			b.emit(edge.To, Define{Symbol: sym, Node: ast.NodeID(terminator.NodeID), Initialized: true, OnEntry: true})
		}
	}
}

// publishStmt publishes one statement's effects in evaluation order.
//
// CFG construction panics on a statement it does not place, and this is now the
// single producer of statement meaning, so it takes the same policy: a new kind
// must be handled here or declared inert in internal/contracts.
func (b *builder) publishStmt(site cfg.SiteID, scope *symbols.Scope, stmt ast.Stmt) {
	switch node := stmt.(type) {
	case *ast.LetDecl:
		b.buildBinding(site, scope, node, node.Value)
	case *ast.ConstDecl:
		b.buildBinding(site, scope, node, node.Value)
	case *ast.AssignStmt:
		b.value(site, node.Value, typeinfo.UseMove)
		b.writeTarget(site, scope, node.Target)
	case *ast.ExprStmt:
		b.value(site, node.Expr, typeinfo.UseRead)
		b.emit(site, Discard{
			Place:    b.placeOrTemporary(node.Expr),
			Node:     node.Expr.ID(),
			Location: ast.LocOf(node.Expr),
		})
	case *ast.ReturnStmt:
		b.value(site, node.Value, typeinfo.UseMove)
	case *ast.MatchStmt:
		// A match reaches this producer at its terminator site, and at a plain
		// statement site when semantic evidence was too incomplete for CFG to
		// decompose it. Publishing the subject here covers both.
		b.value(site, node.Subject, typeinfo.UseRead)
	case *ast.ForStmt:
		// The condition is published from the terminator, which names it
		// directly. The iterated sequence is evaluated by the loop itself and
		// belongs here.
		b.value(site, node.Iterable, typeinfo.UseRead)
	case *ast.IfStmt:
		// A branch condition is published from the terminator, which names it
		// directly, so a site carries exactly the reads that happen at it.
	case *ast.BlockStmt:
		// Blocks are decomposed by CFG construction; a scope-exit site names one
		// but evaluates nothing.
	case *ast.BreakStmt, *ast.ContinueStmt:
		// A transfer is a CFG edge and evaluates no expression.
	case *ast.BadStmt, *ast.BadDecl:
		// Recovery nodes carry no evidence.
	case *ast.ImportDecl, *ast.FnDecl, *ast.TypeAliasDecl,
		*ast.StructDecl, *ast.InterfaceDecl, *ast.EnumDecl:
		// Declarations reach a CFG site after the resolver reports them as
		// unsupported statements. They evaluate nothing here.
	default:
		panic(fmt.Sprintf("effect: unhandled AST statement %T", stmt))
	}
}

// buildBinding publishes a declaration's initializer reads before the define
// they initialize, so `let x = x` reads an outer binding rather than itself.
func (b *builder) buildBinding(site cfg.SiteID, scope *symbols.Scope, decl ast.Stmt, value ast.Expr) {
	b.value(site, value, typeinfo.UseMove)
	if scope == nil {
		return
	}
	sym, found := scope.LookupNode(decl)
	if !found || sym == nil {
		return
	}
	b.emit(site, Define{Symbol: sym, Node: decl.ID(), Initialized: value != nil})
}

// value publishes what one expression does to the bindings it names.
//
// It mirrors the propagation ownership used to perform itself: a position
// decides the kind, and the kind travels down to the identifier that carries
// the value. A projection reads its base, a literal moves what it stores, and a
// call argument takes the typechecker's published decision.
//
// This is the expression dispatch site for published effects. A new expression
// kind must be handled here or declared inert in internal/contracts.
func (b *builder) value(site cfg.SiteID, expr ast.Expr, kind typeinfo.UseKind) {
	switch node := expr.(type) {
	case nil:
		return
	case *ast.Ident:
		if sym := b.queries.Symbols[node.ID()]; sym != nil {
			b.emit(site, Use{Place: Place{Root: sym}, Node: node.ID(), Location: ast.LocOf(node), Kind: kind})
		}
	case *ast.AddressExpr:
		b.borrow(site, node, node.Expr, node.Mode == ast.AddressMutable, node.Mode == ast.AddressRaw)
	case *ast.SelectorExpr:
		// A field of a place is itself a place, so the use lands on the
		// projection rather than on the whole aggregate. A consumer that only
		// cares which binding was touched still reads the root.
		b.projection(site, node, node.Expr, place.OriginProjection{
			Kind: place.OriginField, Field: fieldName(node),
		}, kind)
	case *ast.IndexExpr:
		// Slicing does not read an element out; it borrows a run of the
		// sequence, mutably when the slice itself is a mutable reference.
		// A full range writes no bounds at all, so the index may be absent
		// rather than an empty range.
		_, ranged := node.Index.(*ast.RangeExpr)
		if ranged || node.Index == nil {
			b.value(site, node.Index, typeinfo.UseRead)
			b.borrow(site, node, node.Expr, b.mutableReference(node.ID()), false)
			return
		}
		b.projection(site, node, node.Expr, place.OriginProjection{Kind: place.OriginIndex}, kind)
		// The index is a separate value, not part of the place.
		b.value(site, node.Index, typeinfo.UseRead)
	case *ast.RangeExpr:
		b.value(site, node.Start, typeinfo.UseRead)
		b.value(site, node.End, typeinfo.UseRead)
	case *ast.StructLit:
		for _, field := range node.Fields {
			b.value(site, field.Value, typeinfo.UseMove)
		}
	case *ast.VariantLit:
		b.value(site, node.Payload, typeinfo.UseMove)
	case *ast.ArrayLit:
		for _, element := range node.Values {
			b.value(site, element, typeinfo.UseMove)
		}
	case *ast.CallExpr:
		b.emit(site, CallBegin{Node: node.ID(), Location: ast.LocOf(node)})
		if selector, method := node.Callee.(*ast.SelectorExpr); method && selector != nil {
			// A method callee names a method, not storage. The receiver is the
			// value the call uses, and it is used the way the receiver
			// parameter demands, borrow included, exactly like any argument.
			b.argument(site, selector.Expr)
		} else {
			b.value(site, node.Callee, typeinfo.UseRead)
		}
		arguments := node.Args
		if b.queries.CallArguments != nil {
			arguments = b.queries.CallArguments(node)
		}
		for _, argument := range arguments {
			b.argument(site, argument)
		}
		b.emit(site, CallEnd{Node: node.ID()})
	case *ast.FreeExpr:
		b.value(site, node.Expr, typeinfo.UseMove)
	case *ast.PrintExpr:
		b.value(site, node.Expr, typeinfo.UseRead)
	case *ast.UnaryExpr:
		b.value(site, node.Expr, typeinfo.UseRead)
	case *ast.BinaryExpr:
		if b.queries.StringConcatenation != nil && b.queries.StringConcatenation(node.ID()) {
			// Concatenation consumes the left operand into the result.
			b.value(site, node.Left, typeinfo.UseMove)
			b.value(site, node.Right, typeinfo.UseRead)
			return
		}
		b.value(site, node.Left, typeinfo.UseRead)
		b.value(site, node.Right, typeinfo.UseRead)
	case *ast.IsExpr:
		b.value(site, node.Value, typeinfo.UseRead)
	case *ast.AsExpr:
		b.value(site, node.Expr, typeinfo.UseMove)
	case *ast.ScopeResolution, *ast.NumberLit, *ast.StringLit, *ast.ByteLit,
		*ast.CharLit, *ast.BoolLit, *ast.NoneLit, *ast.BadExpr:
		// These name no binding whose value is used.
	default:
		panic(fmt.Sprintf("effect: unhandled AST expression %T", expr))
	}
}

// argumentKind takes the typechecker's decision where it made one. An absent
// decision means the call did not resolve, which is invalid source continuing
// through the phase, and a read is the answer that claims least.
func (b *builder) argumentKind(argument ast.Expr) typeinfo.UseKind {
	if argument == nil || b.queries.ValueUse == nil {
		return typeinfo.UseRead
	}
	if kind, found := b.queries.ValueUse(argument.ID()); found {
		return kind
	}
	return typeinfo.UseRead
}

// placeOf resolves an expression to the storage it names, following the same
// shapes the canonical place walk recognises. It reports false for anything
// that produces a value without naming storage, such as a call result.
func (b *builder) placeOf(expr ast.Expr) (Place, bool) {
	switch node := expr.(type) {
	case *ast.Ident:
		sym := b.queries.Symbols[node.ID()]
		if sym == nil {
			return Place{}, false
		}
		return Place{Root: sym}, true
	case *ast.SelectorExpr:
		if node.Name == nil {
			return Place{}, false
		}
		return b.project(node.Expr, place.OriginProjection{
			Kind:  place.OriginField,
			Field: node.Name.Name,
		})
	case *ast.IndexExpr:
		return b.project(node.Expr, place.OriginProjection{Kind: place.OriginIndex})
	default:
		return Place{}, false
	}
}

func (b *builder) project(base ast.Expr, step place.OriginProjection) (Place, bool) {
	rooted, ok := b.placeOf(base)
	if !ok {
		return Place{}, false
	}
	// Copy rather than append in place: sibling projections off one base must
	// not share backing storage.
	projections := make([]place.OriginProjection, 0, len(rooted.Projections)+1)
	projections = append(projections, rooted.Projections...)
	projections = append(projections, step)
	rooted.Projections = projections
	return rooted, true
}

func fieldName(selector *ast.SelectorExpr) string {
	if selector == nil || selector.Name == nil {
		return ""
	}
	return selector.Name.Name
}

// projection publishes a use of one projected place. When the base names
// storage the use roots at that binding; otherwise the base is a temporary,
// which still has its own effects and is walked before the projection is
// published.
func (b *builder) projection(site cfg.SiteID, whole, base ast.Expr, step place.OriginProjection, kind typeinfo.UseKind) {
	if rooted, ok := b.project(base, step); ok {
		b.emit(site, Use{Place: rooted, Node: whole.ID(), Location: ast.LocOf(whole), Kind: kind})
		return
	}
	b.value(site, base, typeinfo.UseRead)
	b.emit(site, Use{
		Place:    Place{Temporary: base.ID(), Projections: []place.OriginProjection{step}},
		Node:     whole.ID(),
		Location: ast.LocOf(whole),
		Kind:     kind,
	})
}

// placeOrTemporary names what an expression denotes: the binding it reaches, or
// the expression itself when it produces a value that lives nowhere.
func (b *builder) placeOrTemporary(expr ast.Expr) Place {
	if expr == nil {
		return Place{}
	}
	if rooted, ok := b.placeOf(expr); ok {
		return rooted
	}
	return Place{Temporary: expr.ID()}
}

// writeTarget publishes the store an assignment performs.
//
// A projection target still evaluates the values inside it — an index is read
// to reach the element it selects — so those are published before the write
// itself.
func (b *builder) writeTarget(site cfg.SiteID, scope *symbols.Scope, target ast.Expr) {
	switch node := target.(type) {
	case *ast.Ident:
		sym, found := scope.Lookup(node.Name)
		if !found || sym == nil {
			return
		}
		b.emit(site, Write{Place: Place{Root: sym}, Node: node.ID(), Location: ast.LocOf(node)})
	case *ast.SelectorExpr:
		b.writeProjection(site, node, node.Expr, place.OriginProjection{
			Kind: place.OriginField, Field: fieldName(node),
		})
	case *ast.IndexExpr:
		b.value(site, node.Index, typeinfo.UseRead)
		b.writeProjection(site, node, node.Expr, place.OriginProjection{Kind: place.OriginIndex})
	default:
		// Anything else is not a place; the typechecker rejects it as a target,
		// and its own effects are all it contributes here.
		b.value(site, target, typeinfo.UseRead)
	}
}

func (b *builder) writeProjection(site cfg.SiteID, whole, base ast.Expr, step place.OriginProjection) {
	if rooted, ok := b.project(base, step); ok {
		b.emit(site, Write{Place: rooted, Node: whole.ID(), Location: ast.LocOf(whole)})
		return
	}
	b.value(site, base, typeinfo.UseRead)
	b.emit(site, Write{
		Place:    Place{Temporary: base.ID(), Projections: []place.OriginProjection{step}},
		Node:     whole.ID(),
		Location: ast.LocOf(whole),
	})
}

// mutableReference reports whether an expression's own type is a mutable
// reference, which is what makes a slice of it a mutable borrow.
func (b *builder) mutableReference(id ast.NodeID) bool {
	if b.queries.ExprType == nil {
		return false
	}
	_, mutable, reference := typeinfo.ReferenceTarget(typeinfo.Underlying(b.queries.ExprType(id)))
	return reference && mutable
}

// argument publishes what one call argument does.
//
// A reference parameter borrows, whether or not the source wrote an ampersand,
// and that borrow is the argument's whole effect: publishing a read beside it
// would charge the same place twice. Everything else is an ordinary value use.
func (b *builder) argument(site cfg.SiteID, argument ast.Expr) {
	if argument == nil {
		return
	}
	mutable, borrows := false, false
	if b.queries.ReferenceArgument != nil {
		mutable, borrows = b.queries.ReferenceArgument(argument.ID())
	}
	if !borrows {
		b.value(site, argument, b.argumentKind(argument))
		return
	}
	operand := argument
	if address, explicit := argument.(*ast.AddressExpr); explicit {
		operand = address.Expr
	}
	// Values evaluated to reach the place, such as an index, still happen.
	if index, indexed := operand.(*ast.IndexExpr); indexed {
		b.value(site, index.Index, typeinfo.UseRead)
	}
	b.emit(site, Borrow{
		Place:    b.placeOrTemporary(operand),
		Node:     argument.ID(),
		Location: ast.LocOf(argument),
		Mutable:  mutable,
		Argument: true,
	})
}

// borrow publishes a reference taken to a place. Values inside the operand that
// are evaluated to reach it, such as an index, are published first.
func (b *builder) borrow(site cfg.SiteID, whole, operand ast.Expr, mutable, raw bool) {
	if index, indexed := operand.(*ast.IndexExpr); indexed {
		b.value(site, index.Index, typeinfo.UseRead)
	}
	b.emit(site, Borrow{
		Place:    b.placeOrTemporary(operand),
		Node:     whole.ID(),
		Location: ast.LocOf(whole),
		Mutable:  mutable,
		Raw:      raw,
	})
}
