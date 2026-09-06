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
	// SequenceCarrier reports the hidden carrier a typed sequence loop keeps for
	// the loop lifetime. Range loops return found=false. This is typechecker
	// evidence; the effect producer must not rediscover iteration kind.
	SequenceCarrier func(ast.NodeID) (carrier *symbols.Symbol, found bool)
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
			b.value(site.ID, b.queries.Scopes[ast.NodeID(site.ScopeID)], condition, typeinfo.UseRead)
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
	for _, edge := range b.graph.SiteEdges.OutEdges(site.ID) {
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
		b.value(site, scope, node.Value, typeinfo.UseMove)
		b.writeTarget(site, scope, node.Target, node.ID(), node.Value)
	case *ast.ExprStmt:
		b.value(site, scope, node.Expr, typeinfo.UseRead)
		b.emit(site, Discard{
			Place:    b.placeOrTemporary(scope, node.Expr),
			Node:     node.Expr.ID(),
			Location: ast.LocOf(node.Expr),
		})
	case *ast.ReturnStmt:
		b.value(site, scope, node.Value, typeinfo.UseMove)
	case *ast.MatchStmt:
		// A match reaches this producer at its terminator site, and at a plain
		// statement site when semantic evidence was too incomplete for CFG to
		// decompose it. Publishing the subject here covers both.
		b.value(site, scope, node.Subject, typeinfo.UseRead)
	case *ast.ForStmt:
		// The condition is published from the terminator, which names it
		// directly. The iterated sequence is evaluated by the loop itself and
		// belongs here. A typed sequence loop additionally holds a shared access
		// to its iterable until the loop exit; publish that lifetime fact here
		// instead of making ownership recognize ForStmt.
		b.value(site, scope, node.Iterable, typeinfo.UseRead)
		if node.Iterable != nil && b.queries.SequenceCarrier != nil {
			if carrier, found := b.queries.SequenceCarrier(node.ID()); found && carrier != nil {
				b.emit(site, Iterate{
					Loop: node.ID(), Place: b.placeOrTemporary(scope, node.Iterable),
					Node: node.Iterable.ID(), Carrier: carrier, Location: ast.LocOf(node.Iterable),
				})
			}
		}
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
	b.value(site, scope, value, typeinfo.UseMove)
	if scope == nil {
		return
	}
	sym, found := scope.LookupNode(decl)
	if !found || sym == nil {
		return
	}
	valueID := ast.NodeID(0)
	if value != nil {
		valueID = value.ID()
	}
	b.emit(site, Define{Symbol: sym, Node: decl.ID(), Value: valueID, Initialized: value != nil})
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
func (b *builder) value(site cfg.SiteID, scope *symbols.Scope, expr ast.Expr, kind typeinfo.UseKind) {
	switch node := expr.(type) {
	case nil:
		return
	case *ast.Ident:
		if sym := b.queries.Symbols[node.ID()]; sym != nil {
			b.emit(site, Use{Place: Place{Root: sym}, Node: node.ID(), Location: ast.LocOf(node), Kind: kind})
		}
	case *ast.AddressExpr:
		b.borrow(site, scope, node, node.Expr, nil, node.Mode == ast.AddressMutable, node.Mode == ast.AddressRaw)
	case *ast.SelectorExpr:
		// A field of a place is itself a place, so the use lands on the
		// projection rather than on the whole aggregate. Structural projection
		// shape comes from place.Project, the canonical place grammar.
		projection, ok := place.Project(node)
		if !ok {
			return
		}
		b.projection(site, scope, node, projection.Base, projection.Step, kind)
	case *ast.IndexExpr:
		// Slicing does not read an element out; it borrows a run of the
		// sequence, mutably when the slice itself is a mutable reference.
		// A full range writes no bounds at all, so the index may be absent
		// rather than an empty range.
		_, ranged := node.Index.(*ast.RangeExpr)
		if ranged || node.Index == nil {
			b.borrow(site, scope, node, node.Expr, node.Index, b.mutableReference(node.ID()), false)
			return
		}
		projection, ok := place.Project(node)
		if !ok {
			return
		}
		b.projection(site, scope, node, projection.Base, projection.Step, kind)
	case *ast.RangeExpr:
		b.value(site, scope, node.Start, typeinfo.UseRead)
		b.value(site, scope, node.End, typeinfo.UseRead)
	case *ast.StructLit:
		for _, field := range node.Fields {
			b.value(site, scope, field.Value, typeinfo.UseMove)
		}
	case *ast.VariantLit:
		b.value(site, scope, node.Payload, typeinfo.UseMove)
	case *ast.ArrayLit:
		for _, element := range node.Values {
			b.value(site, scope, element, typeinfo.UseMove)
		}
	case *ast.CallExpr:
		b.emit(site, CallBegin{Node: node.ID(), Location: ast.LocOf(node)})
		if selector, method := node.Callee.(*ast.SelectorExpr); method && selector != nil {
			// A method callee names a method, not storage. The receiver is the
			// value the call uses, and it is used the way the receiver
			// parameter demands, borrow included, exactly like any argument.
			b.argument(site, scope, selector.Expr)
		} else {
			b.value(site, scope, node.Callee, typeinfo.UseRead)
		}
		arguments := node.Args
		if b.queries.CallArguments != nil {
			arguments = b.queries.CallArguments(node)
		}
		for _, argument := range arguments {
			b.argument(site, scope, argument)
		}
		b.emit(site, CallEnd{Node: node.ID()})
	case *ast.FreeExpr:
		b.value(site, scope, node.Expr, typeinfo.UseMove)
	case *ast.PrintExpr:
		b.value(site, scope, node.Expr, typeinfo.UseRead)
	case *ast.UnaryExpr:
		b.value(site, scope, node.Expr, typeinfo.UseRead)
	case *ast.BinaryExpr:
		if b.queries.StringConcatenation != nil && b.queries.StringConcatenation(node.ID()) {
			// Concatenation consumes the left operand into the result.
			b.value(site, scope, node.Left, typeinfo.UseMove)
			b.value(site, scope, node.Right, typeinfo.UseRead)
			return
		}
		b.value(site, scope, node.Left, typeinfo.UseRead)
		b.value(site, scope, node.Right, typeinfo.UseRead)
	case *ast.IsExpr:
		b.value(site, scope, node.Value, typeinfo.UseRead)
	case *ast.AsExpr:
		b.value(site, scope, node.Expr, typeinfo.UseMove)
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

// placeOf resolves a syntactic place through the canonical place structure.
// The place package owns selector/index decomposition; this producer only maps
// the root identifier to its already-resolved symbol.
func (b *builder) placeOf(scope *symbols.Scope, expr ast.Expr) (Place, bool) {
	root, projections, ok := place.Decompose(expr)
	if !ok {
		return Place{}, false
	}
	ident, ok := root.(*ast.Ident)
	if !ok || ident == nil {
		return Place{}, false
	}
	sym := b.queries.Symbols[ident.ID()]
	if sym == nil && scope != nil {
		sym, _ = scope.Lookup(ident.Name)
	}
	if sym == nil {
		return Place{}, false
	}
	return Place{Root: sym, Projections: append([]place.OriginProjection(nil), projections...)}, true
}

// placeOperands evaluates what is needed to reach storage, without charging a
// second use of the root binding. Decomposing a place alone loses the index
// expressions; they must run from the innermost base outward before the access.
func (b *builder) placeOperands(site cfg.SiteID, scope *symbols.Scope, expr ast.Expr) {
	if projection, projected := place.Project(expr); projected {
		if place.IsPlaceExpr(projection.Base) {
			b.placeOperands(site, scope, projection.Base)
		} else {
			// Preserve uses of intermediate temporary projections as well as the
			// evaluation that produces their base.
			b.value(site, scope, projection.Base, typeinfo.UseRead)
		}
		b.value(site, scope, projection.Index, typeinfo.UseRead)
		return
	}
	if _, binding := expr.(*ast.Ident); !binding {
		b.value(site, scope, expr, typeinfo.UseRead)
	}
}

// projection publishes a use of one projected place. When the base names
// storage the use roots at that binding; otherwise the base is a temporary,
// which still has its own effects and is walked before the projection is
// published.
func (b *builder) projection(site cfg.SiteID, scope *symbols.Scope, whole, base ast.Expr, step place.OriginProjection, kind typeinfo.UseKind) {
	b.placeOperands(site, scope, whole)
	if rooted, ok := b.placeOf(scope, whole); ok {
		b.emit(site, Use{Place: rooted, Node: whole.ID(), Location: ast.LocOf(whole), Kind: kind})
		return
	}
	b.emit(site, Use{
		Place:    Place{Temporary: base.ID(), Projections: []place.OriginProjection{step}},
		Node:     whole.ID(),
		Location: ast.LocOf(whole),
		Kind:     kind,
	})
}

// placeOrTemporary names what an expression denotes: the binding it reaches, or
// the expression itself when it produces a value that lives nowhere.
func (b *builder) placeOrTemporary(scope *symbols.Scope, expr ast.Expr) Place {
	if expr == nil {
		return Place{}
	}
	if rooted, ok := b.placeOf(scope, expr); ok {
		return rooted
	}
	return Place{Temporary: expr.ID()}
}

// writeTarget publishes the store an assignment performs.
//
// A projection target still evaluates the values inside it — an index is read
// to reach the element it selects — so those are published before the write
// itself.
func (b *builder) writeTarget(site cfg.SiteID, scope *symbols.Scope, target ast.Expr, owner ast.NodeID, value ast.Expr) {
	valueID := ast.NodeID(0)
	if value != nil {
		valueID = value.ID()
	}
	if ident, ok := target.(*ast.Ident); ok {
		if ident == nil || scope == nil {
			return
		}
		sym := b.queries.Symbols[ident.ID()]
		if sym == nil {
			sym, _ = scope.Lookup(ident.Name)
		}
		if sym != nil {
			b.emit(site, Write{Place: Place{Root: sym}, Node: ident.ID(), Owner: owner, Value: valueID, Location: ast.LocOf(ident)})
		}
		return
	}

	projection, projected := place.Project(target)
	if !projected {
		// Anything else is not a place; the typechecker rejects it as a target,
		// and its own effects are all it contributes here.
		b.value(site, scope, target, typeinfo.UseRead)
		return
	}
	b.writeProjection(site, scope, target, projection.Base, projection.Step, owner, valueID)
}

func (b *builder) writeProjection(site cfg.SiteID, scope *symbols.Scope, whole, base ast.Expr, step place.OriginProjection, owner, value ast.NodeID) {
	b.placeOperands(site, scope, whole)
	if rooted, ok := b.placeOf(scope, whole); ok {
		b.emit(site, Write{Place: rooted, Node: whole.ID(), Owner: owner, Value: value, Location: ast.LocOf(whole)})
		return
	}
	b.emit(site, Write{
		Place:    Place{Temporary: base.ID(), Projections: []place.OriginProjection{step}},
		Node:     whole.ID(),
		Owner:    owner,
		Value:    value,
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
func (b *builder) argument(site cfg.SiteID, scope *symbols.Scope, argument ast.Expr) {
	if argument == nil {
		return
	}
	mutable, borrows := false, false
	if b.queries.ReferenceArgument != nil {
		mutable, borrows = b.queries.ReferenceArgument(argument.ID())
	}
	if !borrows {
		b.value(site, scope, argument, b.argumentKind(argument))
		return
	}
	operand := argument
	if address, explicit := argument.(*ast.AddressExpr); explicit {
		operand = address.Expr
	}
	b.placeOperands(site, scope, operand)
	b.emit(site, Borrow{
		Place:    b.placeOrTemporary(scope, operand),
		Node:     argument.ID(),
		Operand:  operand.ID(),
		Location: ast.LocOf(argument),
		Mutable:  mutable,
		Argument: true,
	})
}

// borrow publishes a reference taken to a place. Values inside the operand that
// are evaluated to reach it, such as an index, are published first.
func (b *builder) borrow(site cfg.SiteID, scope *symbols.Scope, whole, operand, bounds ast.Expr, mutable, raw bool) {
	b.placeOperands(site, scope, operand)
	b.value(site, scope, bounds, typeinfo.UseRead)
	b.emit(site, Borrow{
		Place:    b.placeOrTemporary(scope, operand),
		Node:     whole.ID(),
		Operand:  operand.ID(),
		Location: ast.LocOf(whole),
		Mutable:  mutable,
		Raw:      raw,
	})
}
